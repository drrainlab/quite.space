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

// ReceiptKind says what a receipt asserts. Encoded as the old lapsed flag's
// key and values, so 0 and 1 mean on the wire exactly what they meant
// before; `expired` is new.
type ReceiptKind uint8

const (
	// ReceiptAccepted: the frames are held to the stated horizon.
	ReceiptAccepted ReceiptKind = 0
	// ReceiptLapsed: the claim is WITHDRAWN early — the gateway can no
	// longer keep the frames to the time it promised.
	ReceiptLapsed ReceiptKind = 1
	// ReceiptExpired: custody ran to its horizon and ended there.
	ReceiptExpired ReceiptKind = 2
)

// Lapsed reports whether a kind ends custody. Both a withdrawal and an
// expiry do; they are distinguished so a reader can tell "the gateway gave
// up early" from "the promise ran out", which are different stories to tell
// a person and different inputs to a retry policy.
func (k ReceiptKind) Lapsed() bool { return k != ReceiptAccepted }

// CustodyReceipt is the signed custody claim.
//
// It binds a hand-off, not merely an event. Without Attempt a receipt says
// "I hold event E" — true of every attempt ever made to deliver E, so a
// receipt from an abandoned attempt would be indistinguishable from one
// answering the current attempt. With it, a receipt answers exactly one
// hand-off and the sender can ignore the rest.
type CustodyReceipt struct {
	FrameIDs   []id.EventID
	StoreID    string // custody store instance
	AcceptedAt uint64
	ExpiresAt  uint64 // custody horizon, not delivery promise
	Instance   string // bridge instance label
	PublicKey  []byte // 32B ed25519 custodian key
	Signature  []byte // 64B over the encoding with the signature absent

	Kind ReceiptKind

	// Attempt echoes the SENDER's responsibility token, taken from the
	// frames message that delivered these frames and stored with the
	// custody record before this receipt was signed. A receipt without one
	// cannot release responsibility — see node.handleCustodyReceipt.
	Attempt []byte

	// Lease names the custody record inside this gateway, so repeats of the
	// same claim are recognisable as repeats and diagnostics can point at
	// one row in one store. It refines Attempt; it never replaces it,
	// because only the sender knows which attempt is current.
	Lease string

	// IngressLink and LoopDomain record WHERE the gateway took the frames.
	// A node pins custodians per link domain, so a receipt that does not
	// say which boundary it is speaking about cannot be checked against
	// the right pin.
	IngressLink string
	LoopDomain  string
}

func (r *CustodyReceipt) encode(withSig bool) []byte {
	// Keys 1..8 always present; 9..12 only when set, so a receipt from
	// before RB-1 and one issued now differ by presence, not by shape.
	n := 7
	if withSig {
		n++
	}
	if len(r.Attempt) > 0 {
		n++
	}
	if r.Lease != "" {
		n++
	}
	if r.IngressLink != "" {
		n++
	}
	if r.LoopDomain != "" {
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
	// Keys 8..12 come last: the deterministic subset wants ascending keys,
	// and the signed encoding simply omits 7.
	buf = codec.AppendUint(buf, 8)
	buf = codec.AppendUint(buf, uint64(r.Kind))
	if len(r.Attempt) > 0 {
		buf = codec.AppendUint(buf, 9)
		buf = codec.AppendBytes(buf, r.Attempt)
	}
	if r.Lease != "" {
		buf = codec.AppendUint(buf, 10)
		buf = codec.AppendText(buf, r.Lease)
	}
	if r.IngressLink != "" {
		buf = codec.AppendUint(buf, 11)
		buf = codec.AppendText(buf, r.IngressLink)
	}
	if r.LoopDomain != "" {
		buf = codec.AppendUint(buf, 12)
		buf = codec.AppendText(buf, r.LoopDomain)
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
		case 8:
			var v uint64
			v, er = d.ReadUint()
			r.Kind = ReceiptKind(v)
		case 9:
			var b []byte
			b, er = d.ReadBytes()
			r.Attempt = append([]byte(nil), b...)
		case 10:
			r.Lease, er = d.ReadText()
		case 11:
			r.IngressLink, er = d.ReadText()
		case 12:
			r.LoopDomain, er = d.ReadText()
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
