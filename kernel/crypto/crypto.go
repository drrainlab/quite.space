// Package crypto implements the group-encryption profile of ADR-005: a
// per-terminal symmetric epoch key, payloads sealed with XChaCha20-Poly1305
// (AAD binds terminal, epoch, and schema), and the epoch key wrapped per
// member device with HPKE (RFC 9180, base mode,
// DHKEM(X25519,HKDF-SHA256) + HKDF-SHA256 + ChaCha20-Poly1305).
//
// No construction here is homemade: primitives come from x/crypto and
// cloudflare/circl. Honest limits (also in the threat model): forward
// secrecy is epoch-granular — compromising an epoch key exposes that
// epoch's messages; rotation happens on every membership change.
package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/hpke"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the epoch key length.
const KeySize = 32

// EpochKey is one terminal epoch's symmetric key.
type EpochKey struct {
	N   uint64
	Key [KeySize]byte
}

// NewEpochKey generates epoch n's key.
func NewEpochKey(n uint64) (EpochKey, error) {
	k := EpochKey{N: n}
	if _, err := rand.Read(k.Key[:]); err != nil {
		return EpochKey{}, err
	}
	return k, nil
}

func suite() hpke.Suite {
	return hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_ChaCha20Poly1305)
}

// hpkeInfo binds a wrap to its terminal and epoch so a wrap cannot be
// replayed into another context.
func hpkeInfo(terminal id.TerminalID, epoch uint64) []byte {
	info := append([]byte("quiet-places-epoch-v0:"), terminal[:]...)
	return codec.AppendUint(info, epoch)
}

// Wrap re-exports the wire type: one member device's encrypted epoch key.
type Wrap = schemas.EpochWrap

// WrapEpoch seals the epoch key for every member device (X25519 public keys
// from their certificates, ADR-002).
func WrapEpoch(terminal id.TerminalID, key EpochKey, members map[id.DeviceID][32]byte) ([]Wrap, error) {
	kem := hpke.KEM_X25519_HKDF_SHA256.Scheme()
	info := hpkeInfo(terminal, key.N)
	wraps := make([]Wrap, 0, len(members))
	for dev, xpub := range members {
		pub, err := kem.UnmarshalBinaryPublicKey(xpub[:])
		if err != nil {
			return nil, fmt.Errorf("crypto: bad member key for %s: %w", dev, err)
		}
		sender, err := suite().NewSender(pub, info)
		if err != nil {
			return nil, err
		}
		enc, sealer, err := sender.Setup(rand.Reader)
		if err != nil {
			return nil, err
		}
		ct, err := sealer.Seal(key.Key[:], nil)
		if err != nil {
			return nil, err
		}
		wraps = append(wraps, Wrap{Device: dev, Enc: enc, CT: ct})
	}
	return wraps, nil
}

// ErrNotAddressed means no wrap targets this device — it is not a member of
// the epoch (fail closed; also exactly what a removed member sees).
var ErrNotAddressed = errors.New("crypto: epoch not wrapped for this device")

// UnwrapEpoch recovers the epoch key using this device's X25519 private
// scalar.
func UnwrapEpoch(terminal id.TerminalID, epochN uint64, wraps []Wrap,
	device id.DeviceID, xpriv [32]byte) (EpochKey, error) {

	kem := hpke.KEM_X25519_HKDF_SHA256.Scheme()
	priv, err := kem.UnmarshalBinaryPrivateKey(xpriv[:])
	if err != nil {
		return EpochKey{}, err
	}
	info := hpkeInfo(terminal, epochN)
	for _, w := range wraps {
		if w.Device != device {
			continue
		}
		receiver, err := suite().NewReceiver(priv, info)
		if err != nil {
			return EpochKey{}, err
		}
		opener, err := receiver.Setup(w.Enc)
		if err != nil {
			return EpochKey{}, err
		}
		raw, err := opener.Open(w.CT, nil)
		if err != nil {
			return EpochKey{}, fmt.Errorf("crypto: unwrap failed: %w", err)
		}
		if len(raw) != KeySize {
			return EpochKey{}, errors.New("crypto: unwrapped key has wrong size")
		}
		k := EpochKey{N: epochN}
		copy(k.Key[:], raw)
		return k, nil
	}
	return EpochKey{}, ErrNotAddressed
}

// ---- Payload sealing (ADR-005) ----

// Sealed payload key table v0.
const (
	sealedKeyEpoch = 1
	sealedKeyNonce = 2
	sealedKeyCT    = 3
)

// aad binds ciphertext to terminal, epoch, and schema so it cannot be
// replayed across any of those.
func aad(terminal id.TerminalID, epoch uint64, schema string) []byte {
	out := append([]byte{}, terminal[:]...)
	out = codec.AppendUint(out, epoch)
	return append(out, schema...)
}

// SealPayload encrypts plaintext under the epoch key.
func SealPayload(key EpochKey, terminal id.TerminalID, schema string, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key.Key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plaintext, aad(terminal, key.N, schema))
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, sealedKeyEpoch)
	buf = codec.AppendUint(buf, key.N)
	buf = codec.AppendUint(buf, sealedKeyNonce)
	buf = codec.AppendBytes(buf, nonce)
	buf = codec.AppendUint(buf, sealedKeyCT)
	buf = codec.AppendBytes(buf, ct)
	return buf, nil
}

// SealedEpoch peeks at which epoch a sealed payload needs, without keys.
func SealedEpoch(payload []byte) (uint64, error) {
	epoch, _, _, err := parseSealed(payload)
	return epoch, err
}

func parseSealed(payload []byte) (epoch uint64, nonce, ct []byte, err error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return 0, nil, nil, err
	}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return 0, nil, nil, er
		}
		if !ok {
			break
		}
		switch k {
		case sealedKeyEpoch:
			epoch, er = d.ReadUint()
		case sealedKeyNonce:
			nonce, er = d.ReadBytes()
		case sealedKeyCT:
			ct, er = d.ReadBytes()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return 0, nil, nil, er
		}
	}
	if err := d.Done(); err != nil {
		return 0, nil, nil, err
	}
	if len(nonce) != chacha20poly1305.NonceSizeX || len(ct) == 0 {
		return 0, nil, nil, errors.New("crypto: malformed sealed payload")
	}
	return epoch, nonce, ct, nil
}

// OpenPayload decrypts a sealed payload with the matching epoch key.
func OpenPayload(key EpochKey, terminal id.TerminalID, schema string, payload []byte) ([]byte, error) {
	epoch, nonce, ct, err := parseSealed(payload)
	if err != nil {
		return nil, err
	}
	if epoch != key.N {
		return nil, fmt.Errorf("crypto: payload epoch %d, key epoch %d", epoch, key.N)
	}
	aead, err := chacha20poly1305.NewX(key.Key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, aad(terminal, epoch, schema))
	if err != nil {
		return nil, errors.New("crypto: decryption failed (wrong key, schema, or tampered payload)")
	}
	return pt, nil
}

// Epoch distribution travels as membership.epoch.v1 payloads; the wire
// codec lives in protocol/schemas (EpochPayload). The payload itself is
// plaintext — every wrap inside is already encrypted to its device.
