// Package enrollment defines the two byte strings that cross between an
// instrument device and the space owner's authority node when the device
// is attached (QI-M0, ADR-026):
//
//	instrument.enrollment.v1   device → authority   "here is who I am"
//	instrument.provision.v1    authority → device   "here is what you need"
//
// Neither is a space-log frame. They ride whatever the integration phase
// chooses — QR, serial, BLE, LAN — and that choice must never leak into
// these forms: they are plain canonical CBOR, self-contained, and
// verifiable offline.
//
// AN ENROLLMENT IS PROVEN OWNERSHIP, NOT A BUNDLE OF PUBLIC KEYS (owner's
// QI-M amendment 2). An instrument has three cryptographic identities —
// the device Ed25519 key, the device X25519 key and the terminal Ed25519
// key that signs its manifest — and an authority certifying "these three
// belong together" must be shown exactly that. So the same body is signed
// TWICE: by the device key (this device requested this enrollment and
// this manifest) and by the terminal key (this terminal consents to be
// bound to this device). The manifest inside is signed by the terminal
// key as well; Decode verifies all three and refuses anything less.
package enrollment

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
)

const (
	// Version is the current enrollment form.
	Version = 1
	// NonceSize is the enrollment nonce length.
	NonceSize = 16
	// MaxLabel bounds the human label.
	MaxLabel = 64
	// MaxManifestBytes bounds the embedded manifest frame — a firmware's
	// declaration is small by construction (24 channels at most).
	MaxManifestBytes = 8 * 1024
	// MaxEpochFrames bounds what a provision carries: the current
	// instrument epoch, and at most a few predecessors for delayed opens.
	MaxEpochFrames = 8
)

// Enrollment key table — append-only forever.
const (
	keyVersion     = 1
	keyDevicePub   = 2
	keyX25519Pub   = 3
	keyTerminalPub = 4
	keyManifestSum = 5
	keyLabel       = 6
	keyNonce       = 7
	keyManifest    = 8
	keyDeviceSig   = 9
	keyTerminalSig = 10
)

// Enrollment is what the device hands over.
type Enrollment struct {
	Version      uint64
	Device       id.DeviceID
	X25519Pub    [32]byte
	Terminal     id.TerminalID
	ManifestHash id.Hash
	Label        string
	Nonce        [NonceSize]byte
	// ManifestFrame is the signed manifest (signed by the TERMINAL key);
	// the authority republishes it on the device's behalf.
	ManifestFrame []byte
	DeviceSig     []byte
	TerminalSig   []byte
}

func (e *Enrollment) body() []byte {
	buf := codec.AppendMap(nil, 8)
	buf = codec.AppendUint(buf, keyVersion)
	buf = codec.AppendUint(buf, e.Version)
	buf = codec.AppendUint(buf, keyDevicePub)
	buf = codec.AppendBytes(buf, e.Device[:])
	buf = codec.AppendUint(buf, keyX25519Pub)
	buf = codec.AppendBytes(buf, e.X25519Pub[:])
	buf = codec.AppendUint(buf, keyTerminalPub)
	buf = codec.AppendBytes(buf, e.Terminal[:])
	buf = codec.AppendUint(buf, keyManifestSum)
	buf = codec.AppendBytes(buf, e.ManifestHash[:])
	buf = codec.AppendUint(buf, keyLabel)
	buf = codec.AppendText(buf, e.Label)
	buf = codec.AppendUint(buf, keyNonce)
	buf = codec.AppendBytes(buf, e.Nonce[:])
	buf = codec.AppendUint(buf, keyManifest)
	buf = codec.AppendBytes(buf, e.ManifestFrame)
	return buf
}

// SigningBytes is the canonical body both signatures cover: the map of
// keys 1–8 exactly as Encode emits it. A second implementation signs
// these bytes and nothing else.
func (e *Enrollment) SigningBytes() []byte { return e.body() }

// Sign fills both signatures. The terminal key must be the one the
// manifest was signed with; Sign refuses otherwise so a mismatch is caught
// on the device, not at the authority.
func (e *Enrollment) Sign(devPriv, termPriv ed25519.PrivateKey) error {
	if e.Version == 0 {
		e.Version = Version
	}
	if !bytes.Equal(devPriv.Public().(ed25519.PublicKey), e.Device[:]) {
		return errors.New("enrollment: device key does not match device id")
	}
	if !bytes.Equal(termPriv.Public().(ed25519.PublicKey), e.Terminal[:]) {
		return errors.New("enrollment: terminal key does not match terminal id")
	}
	if err := e.validateShape(); err != nil {
		return err
	}
	body := e.body()
	e.DeviceSig = ed25519.Sign(devPriv, body)
	e.TerminalSig = ed25519.Sign(termPriv, body)
	return nil
}

func (e *Enrollment) validateShape() error {
	if e.Version != Version {
		return fmt.Errorf("enrollment: unsupported version %d", e.Version)
	}
	if len(e.Label) > MaxLabel {
		return errors.New("enrollment: label too long")
	}
	if len(e.ManifestFrame) == 0 || len(e.ManifestFrame) > MaxManifestBytes {
		return errors.New("enrollment: manifest frame missing or too large")
	}
	if sha256.Sum256(e.ManifestFrame) != [32]byte(e.ManifestHash) {
		return errors.New("enrollment: manifest hash does not match the frame")
	}
	return nil
}

// Encode returns the canonical bytes: the body plus both signatures.
func (e *Enrollment) Encode() ([]byte, error) {
	if len(e.DeviceSig) != ed25519.SignatureSize || len(e.TerminalSig) != ed25519.SignatureSize {
		return nil, errors.New("enrollment: not signed")
	}
	if err := e.validateShape(); err != nil {
		return nil, err
	}
	body := e.body()
	// Same splice discipline as the envelope: re-emit with count+2.
	out := codec.AppendMap(nil, 10)
	out = append(out, body[1:]...)
	out = codec.AppendUint(out, keyDeviceSig)
	out = codec.AppendBytes(out, e.DeviceSig)
	out = codec.AppendUint(out, keyTerminalSig)
	out = codec.AppendBytes(out, e.TerminalSig)
	return out, nil
}

// Decode parses and VERIFIES an enrollment: both signatures over the body,
// the manifest hash, the manifest's own signature, and that the manifest
// names the enrolling terminal. Anything short of all four is refused —
// an authority never certifies a binding it has not seen proven.
func Decode(b []byte) (*Enrollment, error) {
	d := codec.NewDecoder(b)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	e := &Enrollment{}
	var sawDevice, sawX, sawTerm, sawSum, sawNonce bool
	for {
		k, ok, err := mr.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case keyVersion:
			if e.Version, err = d.ReadUint(); err != nil {
				return nil, err
			}
		case keyDevicePub:
			if err := read32(d, e.Device[:]); err != nil {
				return nil, err
			}
			sawDevice = true
		case keyX25519Pub:
			if err := read32(d, e.X25519Pub[:]); err != nil {
				return nil, err
			}
			sawX = true
		case keyTerminalPub:
			if err := read32(d, e.Terminal[:]); err != nil {
				return nil, err
			}
			sawTerm = true
		case keyManifestSum:
			if err := read32(d, e.ManifestHash[:]); err != nil {
				return nil, err
			}
			sawSum = true
		case keyLabel:
			if e.Label, err = d.ReadText(); err != nil {
				return nil, err
			}
		case keyNonce:
			nb, err := d.ReadBytes()
			if err != nil {
				return nil, err
			}
			if len(nb) != NonceSize {
				return nil, errors.New("enrollment: bad nonce size")
			}
			copy(e.Nonce[:], nb)
			sawNonce = true
		case keyManifest:
			mb, err := d.ReadBytes()
			if err != nil {
				return nil, err
			}
			e.ManifestFrame = append([]byte(nil), mb...)
		case keyDeviceSig:
			sb, err := d.ReadBytes()
			if err != nil {
				return nil, err
			}
			e.DeviceSig = append([]byte(nil), sb...)
		case keyTerminalSig:
			sb, err := d.ReadBytes()
			if err != nil {
				return nil, err
			}
			e.TerminalSig = append([]byte(nil), sb...)
		default:
			if err := d.SkipItem(); err != nil {
				return nil, err
			}
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if !sawDevice || !sawX || !sawTerm || !sawSum || !sawNonce {
		return nil, errors.New("enrollment: required field missing")
	}
	if err := e.validateShape(); err != nil {
		return nil, err
	}
	if len(e.DeviceSig) != ed25519.SignatureSize || len(e.TerminalSig) != ed25519.SignatureSize {
		return nil, errors.New("enrollment: signature missing")
	}
	body := e.body()
	if !ed25519.Verify(ed25519.PublicKey(e.Device[:]), body, e.DeviceSig) {
		return nil, errors.New("enrollment: device signature invalid")
	}
	if !ed25519.Verify(ed25519.PublicKey(e.Terminal[:]), body, e.TerminalSig) {
		return nil, errors.New("enrollment: terminal signature invalid")
	}
	m, err := manifest.Decode(e.ManifestFrame)
	if err != nil {
		return nil, fmt.Errorf("enrollment: manifest: %w", err)
	}
	if err := manifest.VerifyFrame(e.ManifestFrame, m); err != nil {
		return nil, fmt.Errorf("enrollment: manifest: %w", err)
	}
	if m.Terminal != e.Terminal {
		return nil, errors.New("enrollment: manifest is signed by a different terminal")
	}
	return e, nil
}

func read32(d *codec.Decoder, dst []byte) error {
	b, err := d.ReadBytes()
	if err != nil {
		return err
	}
	if len(b) != 32 {
		return errors.New("enrollment: expected 32 bytes")
	}
	copy(dst, b)
	return nil
}

// Provision key table — append-only forever.
const (
	provSpace       = 1
	provPrincipal   = 2
	provCert        = 3
	provEpochFrames = 4
	provManifestAck = 5
)

// Provision is what the authority hands back: everything the device needs
// to speak on the instrument plane of ONE space, and nothing it does not
// (no conversation epoch, no root, no sibling secrets — by construction
// there is no field for them).
type Provision struct {
	Space     id.TerminalID
	Principal id.PrincipalID
	// CertFrame is identity.device_certified.v1 payload bytes — the
	// device's own certificate, for its records.
	CertFrame []byte
	// EpochFrames are complete signed membership.instrument_epoch.v1
	// ENVELOPE frames, oldest first, ending with the current epoch. Whole
	// frames so the device absorbs them exactly as a replica would — and
	// learns the Lamport clock from them.
	EpochFrames [][]byte
	// ManifestAck is the hash of the manifest the authority published.
	ManifestAck id.Hash
}

func (p *Provision) Encode() ([]byte, error) {
	// The certificate is always required. Epoch frames are required only
	// by the plane's existence: a PUBLIC space is plaintext, has no
	// instrument plane, and its provision honestly carries none (QI-B1
	// Ф3) — the device sees the empty array and knows exactly what it
	// was not handed.
	if len(p.CertFrame) == 0 {
		return nil, errors.New("provision: certificate is required")
	}
	if len(p.EpochFrames) > MaxEpochFrames {
		return nil, errors.New("provision: too many epoch frames")
	}
	buf := codec.AppendMap(nil, 5)
	buf = codec.AppendUint(buf, provSpace)
	buf = codec.AppendBytes(buf, p.Space[:])
	buf = codec.AppendUint(buf, provPrincipal)
	buf = codec.AppendBytes(buf, p.Principal[:])
	buf = codec.AppendUint(buf, provCert)
	buf = codec.AppendBytes(buf, p.CertFrame)
	buf = codec.AppendUint(buf, provEpochFrames)
	buf = codec.AppendArray(buf, len(p.EpochFrames))
	for _, f := range p.EpochFrames {
		buf = codec.AppendBytes(buf, f)
	}
	buf = codec.AppendUint(buf, provManifestAck)
	buf = codec.AppendBytes(buf, p.ManifestAck[:])
	return buf, nil
}

func DecodeProvision(b []byte) (*Provision, error) {
	d := codec.NewDecoder(b)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &Provision{}
	for {
		k, ok, err := mr.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case provSpace:
			if err := read32(d, p.Space[:]); err != nil {
				return nil, err
			}
		case provPrincipal:
			if err := read32(d, p.Principal[:]); err != nil {
				return nil, err
			}
		case provCert:
			cb, err := d.ReadBytes()
			if err != nil {
				return nil, err
			}
			p.CertFrame = append([]byte(nil), cb...)
		case provEpochFrames:
			cnt, err := d.ReadArray()
			if err != nil {
				return nil, err
			}
			if cnt > MaxEpochFrames {
				return nil, errors.New("provision: too many epoch frames")
			}
			for j := 0; j < cnt; j++ {
				fb, err := d.ReadBytes()
				if err != nil {
					return nil, err
				}
				p.EpochFrames = append(p.EpochFrames, append([]byte(nil), fb...))
			}
		case provManifestAck:
			if err := read32(d, p.ManifestAck[:]); err != nil {
				return nil, err
			}
		default:
			if err := d.SkipItem(); err != nil {
				return nil, err
			}
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	// Symmetric with Encode: the certificate is the one hard requirement;
	// an empty epoch list is a public space's honest freight.
	if len(p.CertFrame) == 0 {
		return nil, errors.New("provision: certificate is required")
	}
	return p, nil
}
