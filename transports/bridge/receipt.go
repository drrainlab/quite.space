// Custody receipts (TN-B, ADR-015 §7): a bridge holds a local operational
// CUSTODIAN keypair — never a space/terminal identity, never a member,
// never an event author. It signs "I durably hold these frames" and the
// receiving node honors it ONLY for a key pinned to the ingress link
// domain (TOFU forbidden). An ACK is sent strictly after append+fsync.
package bridge

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// CustodyReceipt is the signed custody claim.
type CustodyReceipt struct {
	FrameIDs   []id.EventID
	StoreID    string // custody store instance
	AcceptedAt uint64
	ExpiresAt  uint64 // custody horizon, not delivery promise
	Instance   string // bridge instance label
	PublicKey  []byte // 32B ed25519 custodian key
	Signature  []byte // 64B over the encoding with the signature absent
}

func (r *CustodyReceipt) encode(withSig bool) []byte {
	n := 6
	if withSig {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendArray(buf, len(r.FrameIDs))
	for _, f := range r.FrameIDs {
		buf = codec.AppendBytes(buf, f[:])
	}
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, r.StoreID)
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendUint(buf, r.AcceptedAt)
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendUint(buf, r.ExpiresAt)
	buf = codec.AppendUint(buf, 5)
	buf = codec.AppendText(buf, r.Instance)
	buf = codec.AppendUint(buf, 6)
	buf = codec.AppendBytes(buf, r.PublicKey)
	if withSig {
		buf = codec.AppendUint(buf, 7)
		buf = codec.AppendBytes(buf, r.Signature)
	}
	return buf
}

// Sign finalizes the receipt with the custodian key.
func (r *CustodyReceipt) Sign(priv ed25519.PrivateKey) []byte {
	r.PublicKey = append([]byte(nil), priv.Public().(ed25519.PublicKey)...)
	r.Signature = ed25519.Sign(priv, r.encode(false))
	return r.encode(true)
}

// DecodeReceipt parses and signature-checks a receipt (the PIN check —
// "is this key trusted for this link domain" — is the caller's).
func DecodeReceipt(raw []byte) (*CustodyReceipt, error) {
	d := codec.NewDecoder(raw)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	r := &CustodyReceipt{}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			var n int
			n, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range n {
				b, e := d.ReadBytes()
				if e != nil {
					return nil, e
				}
				if len(b) != id.Size {
					return nil, errors.New("bridge: bad frame id in receipt")
				}
				var eid id.EventID
				copy(eid[:], b)
				r.FrameIDs = append(r.FrameIDs, eid)
			}
		case 2:
			r.StoreID, er = d.ReadText()
		case 3:
			r.AcceptedAt, er = d.ReadUint()
		case 4:
			r.ExpiresAt, er = d.ReadUint()
		case 5:
			r.Instance, er = d.ReadText()
		case 6:
			var b []byte
			b, er = d.ReadBytes()
			r.PublicKey = append([]byte(nil), b...)
		case 7:
			var b []byte
			b, er = d.ReadBytes()
			r.Signature = append([]byte(nil), b...)
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if len(r.PublicKey) != ed25519.PublicKeySize || len(r.Signature) != ed25519.SignatureSize {
		return nil, errors.New("bridge: receipt missing key or signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(r.PublicKey), r.encode(false), r.Signature) {
		return nil, errors.New("bridge: receipt signature invalid")
	}
	return r, nil
}

// LoadCustodianKey loads (or mints on first run) the bridge's operational
// keypair. It is NOT an identity: it certifies custody, nothing else.
func LoadCustodianKey(dataDir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dataDir, "custodian.key")
	if b, err := os.ReadFile(path); err == nil && len(b) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(b), nil
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
