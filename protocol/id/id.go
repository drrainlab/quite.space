// Package id defines the identifier types shared by all protocol packages.
// Definitions follow the canonical glossary in ADR-001: Principal, Device,
// and Terminal IDs are raw Ed25519 public keys; EventID is the SHA-256 of a
// full canonical frame (ADR-004).
package id

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Size is the byte length of every identifier.
const Size = 32

type (
	// PrincipalID is the Principal's root Ed25519 public key.
	PrincipalID [Size]byte
	// DeviceID is a device's Ed25519 public key.
	DeviceID [Size]byte
	// TerminalID is a terminal's Ed25519 public key.
	TerminalID [Size]byte
	// EventID is the SHA-256 hash of a full canonical envelope frame.
	EventID [Size]byte
	// Hash is a SHA-256 content hash (blobs, manifests).
	Hash [Size]byte
)

// EventIDOf hashes a canonical frame into its derived identity (ADR-004).
func EventIDOf(frame []byte) EventID { return EventID(sha256.Sum256(frame)) }

// HashOf hashes arbitrary content.
func HashOf(b []byte) Hash { return Hash(sha256.Sum256(b)) }

func hexShort(b []byte) string { return hex.EncodeToString(b[:6]) }

func (p PrincipalID) String() string { return "principal:" + hexShort(p[:]) }
func (d DeviceID) String() string    { return "device:" + hexShort(d[:]) }
func (t TerminalID) String() string  { return "terminal:" + hexShort(t[:]) }
func (e EventID) String() string     { return "event:" + hexShort(e[:]) }
func (h Hash) String() string        { return "sha256:" + hexShort(h[:]) }

func (p PrincipalID) Hex() string { return hex.EncodeToString(p[:]) }
func (d DeviceID) Hex() string    { return hex.EncodeToString(d[:]) }
func (t TerminalID) Hex() string  { return hex.EncodeToString(t[:]) }
func (e EventID) Hex() string     { return hex.EncodeToString(e[:]) }
func (h Hash) Hex() string        { return hex.EncodeToString(h[:]) }

// Fingerprint renders a key ID for manual verification (ADR-002): the
// SHA-256 of the public key, shown as ten groups of four hex characters.
func Fingerprint(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	h := hex.EncodeToString(sum[:20])
	out := make([]byte, 0, 49)
	for i := 0; i < 40; i += 4 {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, h[i:i+4]...)
	}
	return string(out)
}

func parse32(dst []byte, s string) error {
	b, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	if len(b) != Size {
		return fmt.Errorf("id: want %d bytes, got %d", Size, len(b))
	}
	copy(dst, b)
	return nil
}

// ParseTerminalID parses a 64-char hex terminal id.
func ParseTerminalID(s string) (TerminalID, error) {
	var t TerminalID
	err := parse32(t[:], s)
	return t, err
}

// ParsePrincipalID parses a 64-char hex principal id.
func ParsePrincipalID(s string) (PrincipalID, error) {
	var p PrincipalID
	err := parse32(p[:], s)
	return p, err
}

// ParseDeviceID parses a 64-char hex device id.
func ParseDeviceID(s string) (DeviceID, error) {
	var d DeviceID
	err := parse32(d[:], s)
	return d, err
}
