// Package identity implements the Identity Manager (ADR-002): Principal and
// Device keys, depth-1 device certification, revocation, fingerprints, and
// encrypted recovery bundles. No usernames, no registries, no recovery
// backdoor.
package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"golang.org/x/crypto/curve25519"
)

// Principal holds a root keypair. The private key is used only rarely:
// certify/revoke devices, countersign terminal genesis manifests, export
// recovery bundles (ADR-002).
type Principal struct {
	ID   id.PrincipalID
	priv ed25519.PrivateKey
}

// Device holds one device keypair plus the X25519 key derived for HPKE-style
// wrapping (ADR-005). Devices sign every Signal.
type Device struct {
	ID        id.DeviceID
	priv      ed25519.PrivateKey
	x25519    [32]byte // private scalar
	X25519Pub [32]byte
}

// NewPrincipal generates a Principal from the given entropy source.
func NewPrincipal(rng io.Reader) (*Principal, error) {
	pub, priv, err := ed25519.GenerateKey(rng)
	if err != nil {
		return nil, err
	}
	p := &Principal{priv: priv}
	copy(p.ID[:], pub)
	return p, nil
}

// NewDevice generates a Device keypair.
func NewDevice(rng io.Reader) (*Device, error) {
	pub, priv, err := ed25519.GenerateKey(rng)
	if err != nil {
		return nil, err
	}
	d := &Device{priv: priv}
	copy(d.ID[:], pub)
	if _, err := io.ReadFull(rng, d.x25519[:]); err != nil {
		return nil, err
	}
	xpub, err := curve25519.X25519(d.x25519[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	copy(d.X25519Pub[:], xpub)
	return d, nil
}

// NewPrincipalFromSeed builds a deterministic principal (tests, vectors).
func NewPrincipalFromSeed(seed []byte) *Principal {
	priv := ed25519.NewKeyFromSeed(seed)
	p := &Principal{priv: priv}
	copy(p.ID[:], priv.Public().(ed25519.PublicKey))
	return p
}

// Sign signs with the root key. Reserved for certificates, revocations, and
// genesis countersignatures — never for Signals.
func (p *Principal) Sign(msg []byte) []byte { return ed25519.Sign(p.priv, msg) }

// SignKey exposes the device private key for envelope signing.
func (d *Device) SignKey() ed25519.PrivateKey { return d.priv }

// X25519Priv exposes the device's key-agreement scalar for unwrapping epoch
// keys (ADR-005). Handle with the same care as the signing key.
func (d *Device) X25519Priv() [32]byte { return d.x25519 }

// Fingerprint of the principal's public key for manual verification.
func (p *Principal) Fingerprint() string { return id.Fingerprint(p.ID[:]) }

// ---- Device certificates ----

// Certificate wire key table v0.
const (
	certKeyPrincipal = 1
	certKeyDevice    = 2
	certKeyX25519    = 3
	certKeyIssuedAt  = 4
	certKeyExpiresAt = 5
	certKeySignature = 6
)

// Certificate binds a device (and its wrapping key) to a principal, signed
// by the principal root key. Depth is always exactly 1 (ADR-002).
type Certificate struct {
	Principal id.PrincipalID
	Device    id.DeviceID
	X25519Pub [32]byte
	IssuedAt  uint64 // logical time
	ExpiresAt uint64 // 0 = no expiry
	Signature []byte
}

func (c *Certificate) signingBytes() []byte {
	n := 4
	if c.ExpiresAt != 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, certKeyPrincipal)
	buf = codec.AppendBytes(buf, c.Principal[:])
	buf = codec.AppendUint(buf, certKeyDevice)
	buf = codec.AppendBytes(buf, c.Device[:])
	buf = codec.AppendUint(buf, certKeyX25519)
	buf = codec.AppendBytes(buf, c.X25519Pub[:])
	buf = codec.AppendUint(buf, certKeyIssuedAt)
	buf = codec.AppendUint(buf, c.IssuedAt)
	if c.ExpiresAt != 0 {
		buf = codec.AppendUint(buf, certKeyExpiresAt)
		buf = codec.AppendUint(buf, c.ExpiresAt)
	}
	return buf
}

// Certify issues a certificate for a device.
func (p *Principal) Certify(d *Device, issuedAt, expiresAt uint64) *Certificate {
	c := &Certificate{
		Principal: p.ID,
		Device:    d.ID,
		X25519Pub: d.X25519Pub,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
	}
	c.Signature = p.Sign(c.signingBytes())
	return c
}

// Encode returns the canonical certificate bytes (payload of
// identity.device_certified.v1).
func (c *Certificate) Encode() ([]byte, error) {
	if len(c.Signature) != ed25519.SignatureSize {
		return nil, errors.New("identity: certificate not signed")
	}
	buf := c.signingBytes()
	// Append signature entry: rebuild with count+1. Entries are already in
	// ascending key order; signature key is the largest.
	d := codec.NewDecoder(buf)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	for {
		_, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if err := d.SkipItem(); err != nil {
			return nil, err
		}
	}
	body := buf[1:] // strip the 1-byte map header (n<=5 pairs)
	out := codec.AppendMap(nil, m.Len()+1)
	out = append(out, body...)
	out = codec.AppendUint(out, certKeySignature)
	out = codec.AppendBytes(out, c.Signature)
	return out, nil
}

// DecodeCertificate parses and structurally validates certificate bytes.
func DecodeCertificate(b []byte) (*Certificate, error) {
	d := codec.NewDecoder(b)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	c := &Certificate{}
	var seenP, seenD, seenX, seenT, seenSig bool
	read32 := func(dst []byte) error {
		v, err := d.ReadBytes()
		if err != nil {
			return err
		}
		if len(v) != 32 {
			return fmt.Errorf("identity: field must be 32 bytes, got %d", len(v))
		}
		copy(dst, v)
		return nil
	}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case certKeyPrincipal:
			err = read32(c.Principal[:])
			seenP = true
		case certKeyDevice:
			err = read32(c.Device[:])
			seenD = true
		case certKeyX25519:
			err = read32(c.X25519Pub[:])
			seenX = true
		case certKeyIssuedAt:
			c.IssuedAt, err = d.ReadUint()
			seenT = true
		case certKeyExpiresAt:
			c.ExpiresAt, err = d.ReadUint()
		case certKeySignature:
			var v []byte
			v, err = d.ReadBytes()
			if err == nil && len(v) != ed25519.SignatureSize {
				return nil, errors.New("identity: signature must be 64 bytes")
			}
			c.Signature = v
			seenSig = true
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if !seenP || !seenD || !seenX || !seenT || !seenSig {
		return nil, errors.New("identity: certificate missing required fields")
	}
	return c, nil
}

// Verify checks the root-key signature over the certificate.
func (c *Certificate) Verify() error {
	if !ed25519.Verify(ed25519.PublicKey(c.Principal[:]), c.signingBytes(), c.Signature) {
		return errors.New("identity: invalid certificate signature")
	}
	return nil
}

// ---- Revocation ----

// Revocation wire key table v0.
const (
	revKeyPrincipal = 1
	revKeyDevice    = 2
	revKeyAt        = 3
	revKeySignature = 4
)

// Revocation invalidates a device from a logical time onward (ADR-002:
// events with a later logical clock are rejected; earlier history stands).
type Revocation struct {
	Principal id.PrincipalID
	Device    id.DeviceID
	RevokedAt uint64 // logical time
	Signature []byte
}

func (r *Revocation) signingBytes() []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, revKeyPrincipal)
	buf = codec.AppendBytes(buf, r.Principal[:])
	buf = codec.AppendUint(buf, revKeyDevice)
	buf = codec.AppendBytes(buf, r.Device[:])
	buf = codec.AppendUint(buf, revKeyAt)
	buf = codec.AppendUint(buf, r.RevokedAt)
	return buf
}

// Revoke issues a revocation for a device.
func (p *Principal) Revoke(device id.DeviceID, revokedAtLogical uint64) *Revocation {
	r := &Revocation{Principal: p.ID, Device: device, RevokedAt: revokedAtLogical}
	r.Signature = p.Sign(r.signingBytes())
	return r
}

// Encode returns canonical revocation bytes (payload of
// identity.device_revoked.v1).
func (r *Revocation) Encode() ([]byte, error) {
	if len(r.Signature) != ed25519.SignatureSize {
		return nil, errors.New("identity: revocation not signed")
	}
	body := r.signingBytes()[1:] // strip 1-byte map header
	out := codec.AppendMap(nil, 4)
	out = append(out, body...)
	out = codec.AppendUint(out, revKeySignature)
	out = codec.AppendBytes(out, r.Signature)
	return out, nil
}

// DecodeRevocation parses revocation bytes.
func DecodeRevocation(b []byte) (*Revocation, error) {
	d := codec.NewDecoder(b)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	r := &Revocation{}
	var seenP, seenD, seenT, seenSig bool
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case revKeyPrincipal:
			var v []byte
			v, err = d.ReadBytes()
			if err == nil && len(v) == 32 {
				copy(r.Principal[:], v)
				seenP = true
			} else if err == nil {
				return nil, errors.New("identity: principal must be 32 bytes")
			}
		case revKeyDevice:
			var v []byte
			v, err = d.ReadBytes()
			if err == nil && len(v) == 32 {
				copy(r.Device[:], v)
				seenD = true
			} else if err == nil {
				return nil, errors.New("identity: device must be 32 bytes")
			}
		case revKeyAt:
			r.RevokedAt, err = d.ReadUint()
			seenT = true
		case revKeySignature:
			var v []byte
			v, err = d.ReadBytes()
			if err == nil && len(v) != ed25519.SignatureSize {
				return nil, errors.New("identity: signature must be 64 bytes")
			}
			r.Signature = v
			seenSig = true
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if !seenP || !seenD || !seenT || !seenSig {
		return nil, errors.New("identity: revocation missing required fields")
	}
	return r, nil
}

// Verify checks the root-key signature over the revocation.
func (r *Revocation) Verify() error {
	if !ed25519.Verify(ed25519.PublicKey(r.Principal[:]), r.signingBytes(), r.Signature) {
		return errors.New("identity: invalid revocation signature")
	}
	return nil
}

// ---- Trust store ----

// Store tracks certificates and revocations for signature admission. It is
// pure state; persistence is the caller's job (rebuildable from the event
// log, ADR-006).
type Store struct {
	certs map[id.DeviceID]*Certificate
	revs  map[id.DeviceID]*Revocation
}

func NewStore() *Store {
	return &Store{certs: map[id.DeviceID]*Certificate{}, revs: map[id.DeviceID]*Revocation{}}
}

// AddCertificate verifies and records a certificate.
func (s *Store) AddCertificate(c *Certificate) error {
	if err := c.Verify(); err != nil {
		return err
	}
	if existing, ok := s.certs[c.Device]; ok && !bytes.Equal(existing.Principal[:], c.Principal[:]) {
		return errors.New("identity: device already certified by a different principal")
	}
	s.certs[c.Device] = c
	return nil
}

// AddRevocation verifies and records a revocation; it must match the
// certifying principal.
func (s *Store) AddRevocation(r *Revocation) error {
	if err := r.Verify(); err != nil {
		return err
	}
	if c, ok := s.certs[r.Device]; ok && !bytes.Equal(c.Principal[:], r.Principal[:]) {
		return errors.New("identity: revocation principal does not match certificate")
	}
	if old, ok := s.revs[r.Device]; !ok || r.RevokedAt < old.RevokedAt {
		s.revs[r.Device] = r
	}
	return nil
}

// Certificate returns the known certificate for a device, if any.
func (s *Store) Certificate(dev id.DeviceID) (*Certificate, bool) {
	c, ok := s.certs[dev]
	return c, ok
}

// Admit decides whether an event from (principal, device) at the given
// logical time is acceptable: certified, principal matches, not revoked at
// that time. Unknown devices fail closed (invariant §2.7).
func (s *Store) Admit(principal id.PrincipalID, device id.DeviceID, logicalClock uint64) error {
	c, ok := s.certs[device]
	if !ok {
		return errors.New("identity: device not certified")
	}
	if !bytes.Equal(c.Principal[:], principal[:]) {
		return errors.New("identity: principal does not match device certificate")
	}
	if r, ok := s.revs[device]; ok && logicalClock > r.RevokedAt {
		return errors.New("identity: device revoked at this logical time")
	}
	return nil
}

// NewRand returns the canonical entropy source for key generation.
func NewRand() io.Reader { return rand.Reader }
