// The ceremony's two derivations (MD-1), and the one idea behind both:
// EVERYTHING HANGS OFF THE LIVE SESSION.
//
// The six confirmation digits derive from HKDF(secret, session binding) —
// so a recording replayed later negotiates a different session and shows
// different digits, and a person-in-the-middle holds two sessions whose two
// screens visibly disagree. The freight key derives from the same secret
// AND the same binding AND the transcript of the ceremony — deriving it
// from the secret alone would leave the six digits protecting the ceremony
// while the identity itself travelled under a key a recording could
// reconstruct. Both halves hang off the session the humans actually
// approved, or neither is worth anything.
//
// These are PURE FUNCTIONS over bytes: the TLS layer produces the binding
// (lan.Conn.SessionBinding, 32 bytes of RFC 9266-style exported keying
// material under BindingLabel), the ceremony layer produces the transcript,
// and nothing here touches a connection — which is what makes every
// property below provable in a table-driven test.
package pairing

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// BindingLabel is the TLS exporter label both ends pass to
	// SessionBinding. One name, exported, so the two sides cannot drift.
	BindingLabel = "qs.pair.v1"

	// confirmInfo and freightInfo are the HKDF domain labels. They are
	// distinct BY CONSTRUCTION and tested to stay so: the digits are shown
	// on screens and spoken across a room, and must never be the freight
	// key's bytes wearing a friendlier hat.
	confirmInfo = "qs.pair.confirm.v1"
	freightInfo = "qs.pair.freight.v1"

	// bindingLen is what SessionBinding exports. Anything else is not a
	// session binding, whatever it claims to be.
	bindingLen = 32
)

var (
	errNoBinding = errors.New("pairing: no session binding — refusing to derive session-independent material")
	errNoSecret  = errors.New("pairing: zero secret")
)

// ConfirmDigits derives the six digits both screens show. Same secret, same
// live session → same digits; anything else → visibly different ones.
//
// The nil-binding refusal is the load-bearing check: SessionBinding returns
// nil when the export fails, and digits derived from nothing would be the
// same for EVERY session — precisely the property the digits exist to deny.
func ConfirmDigits(secret [32]byte, binding []byte) (string, error) {
	if err := checkInputs(secret, binding); err != nil {
		return "", err
	}
	okm, err := hkdf.Key(sha256.New, secret[:], binding, confirmInfo, 8)
	if err != nil {
		return "", err
	}
	// Reducing 64 uniform bits mod 10^6 biases each value by less than
	// 2^-44 — irrelevant against a six-digit space an attacker cannot grind
	// anyway (one wrong confirmation ends the ceremony).
	n := binary.BigEndian.Uint64(okm) % 1_000_000
	return fmt.Sprintf("%06d", n), nil
}

// FreightKey derives the key the identity freight travels under, bound to
// all three things at once: the offer's secret, the TLS session the humans
// confirmed, and the transcript of the ceremony that led there. Change any
// one and the key is different — a tampered ceremony cannot decrypt what a
// clean one sealed.
func FreightKey(secret [32]byte, binding, transcriptHash []byte) ([]byte, error) {
	if err := checkInputs(secret, binding); err != nil {
		return nil, err
	}
	if len(transcriptHash) != sha256.Size {
		return nil, errors.New("pairing: transcript hash must be the 32-byte TranscriptHash output")
	}
	salt := make([]byte, 0, len(binding)+len(transcriptHash))
	salt = append(salt, binding...)
	salt = append(salt, transcriptHash...)
	return hkdf.Key(sha256.New, secret[:], salt, freightInfo, 32)
}

// TranscriptHash hashes the ceremony's messages UNAMBIGUOUSLY: each part is
// length-prefixed, so ("ab","c") and ("a","bc") — two different ceremonies —
// can never hash alike. The ceremony layer feeds it the offer bytes and the
// wire messages, in order, from both sides' identical view.
func TranscriptHash(parts ...[]byte) []byte {
	h := sha256.New()
	var n [8]byte
	for _, p := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write(p)
	}
	return h.Sum(nil)
}

func checkInputs(secret [32]byte, binding []byte) error {
	if secret == ([32]byte{}) {
		return errNoSecret
	}
	if len(binding) != bindingLen {
		return errNoBinding
	}
	return nil
}
