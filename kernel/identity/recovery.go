// Recovery bundles (ADR-002): encrypted export of the principal root key and
// device certificate list. scrypt passphrase KDF + XChaCha20-Poly1305.
// Losing all devices and the bundle means the identity is gone — by design.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

// scrypt parameters: interactive-strength, fixed for v0 bundles.
const (
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
	saltLen = 16
)

// Bundle wire key table v0.
const (
	bundleKeyVersion = 1
	bundleKeySalt    = 2
	bundleKeyNonce   = 3
	bundleKeyBox     = 4
)

// inner plaintext key table.
const (
	innerKeySeed  = 1
	innerKeyCerts = 2
)

// ExportRecoveryBundle encrypts the principal seed and its certificates
// under a passphrase.
func ExportRecoveryBundle(p *Principal, certs []*Certificate, passphrase []byte) ([]byte, error) {
	if len(passphrase) < 8 {
		return nil, errors.New("identity: passphrase must be at least 8 bytes")
	}
	inner := codec.AppendMap(nil, 2)
	inner = codec.AppendUint(inner, innerKeySeed)
	inner = codec.AppendBytes(inner, p.priv.Seed())
	inner = codec.AppendUint(inner, innerKeyCerts)
	inner = codec.AppendArray(inner, len(certs))
	for _, c := range certs {
		enc, err := c.Encode()
		if err != nil {
			return nil, err
		}
		inner = codec.AppendBytes(inner, enc)
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := scrypt.Key(passphrase, salt, scryptN, scryptR, scryptP, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	box := aead.Seal(nil, nonce, inner, []byte("quiet-places-recovery-v0"))

	out := codec.AppendMap(nil, 4)
	out = codec.AppendUint(out, bundleKeyVersion)
	out = codec.AppendUint(out, 0)
	out = codec.AppendUint(out, bundleKeySalt)
	out = codec.AppendBytes(out, salt)
	out = codec.AppendUint(out, bundleKeyNonce)
	out = codec.AppendBytes(out, nonce)
	out = codec.AppendUint(out, bundleKeyBox)
	out = codec.AppendBytes(out, box)
	return out, nil
}

// ImportRecoveryBundle decrypts a bundle and reconstructs the principal and
// its certificates.
func ImportRecoveryBundle(bundle, passphrase []byte) (*Principal, []*Certificate, error) {
	d := codec.NewDecoder(bundle)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, nil, err
	}
	var salt, nonce, box []byte
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			break
		}
		switch k {
		case bundleKeyVersion:
			if _, err = d.ReadUint(); err != nil {
				return nil, nil, err
			}
		case bundleKeySalt:
			salt, err = d.ReadBytes()
		case bundleKeyNonce:
			nonce, err = d.ReadBytes()
		case bundleKeyBox:
			box, err = d.ReadBytes()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, nil, err
	}
	if len(salt) != saltLen || len(nonce) != chacha20poly1305.NonceSizeX || len(box) == 0 {
		return nil, nil, errors.New("identity: malformed recovery bundle")
	}
	key, err := scrypt.Key(passphrase, salt, scryptN, scryptR, scryptP, chacha20poly1305.KeySize)
	if err != nil {
		return nil, nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	inner, err := aead.Open(nil, nonce, box, []byte("quiet-places-recovery-v0"))
	if err != nil {
		return nil, nil, errors.New("identity: wrong passphrase or corrupted bundle")
	}

	di := codec.NewDecoder(inner)
	mi, err := di.ReadMapHeader()
	if err != nil {
		return nil, nil, err
	}
	var seed []byte
	var certs []*Certificate
	for {
		k, ok, err := mi.Next()
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			break
		}
		switch k {
		case innerKeySeed:
			seed, err = di.ReadBytes()
		case innerKeyCerts:
			var n int
			n, err = di.ReadArray()
			if err != nil {
				return nil, nil, err
			}
			for range n {
				cb, e := di.ReadBytes()
				if e != nil {
					return nil, nil, e
				}
				c, e := DecodeCertificate(cb)
				if e != nil {
					return nil, nil, e
				}
				certs = append(certs, c)
			}
		default:
			err = di.SkipItem()
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if err := di.Done(); err != nil {
		return nil, nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, nil, errors.New("identity: bundle missing principal seed")
	}
	return NewPrincipalFromSeed(seed), certs, nil
}
