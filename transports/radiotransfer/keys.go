// The key that protects the MECHANISM, and the honest statement of what it
// does not protect.
//
// A DATA frame's payload is already sealed under Quiet's epoch keys before it
// reaches this layer, and nothing here weakens or replaces that. What is NOT
// covered by anything above is the machinery itself: SACK, COMMIT, CANCEL and
// REPAIR sit below the envelope and say nothing secret, but acting on a
// forged one is expensive. A neighbour who can send a CANCEL can stop any
// transfer on the segment at will, and one who can send a SACK can make a
// sender believe fragments arrived that did not.
//
// On Meshtastic that risk is currently masked, because the channel PSK
// happens to cover everything on the channel. That is an accident of the
// carrier, and RNode has no such layer — so relying on it would make the
// security of this protocol a property of a carrier we intend to replace.
//
// What this key claims, exactly:
//
//	it does     prove a control frame was written by somebody holding the
//	            segment seed, and bind DATA framing to the same segment
//	it does NOT provide confidentiality — the fact that a radio exchange is
//	            happening is observable to anyone within range, and this
//	            layer never pretends otherwise
//	it does NOT authenticate a PERSON: everyone in the segment holds the
//	            same key, so a member can impersonate another member's
//	            control frames. Content authorship stays with the epoch
//	            signature inside the payload, where it always was.
package radiotransfer

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
)

// KDFVersion travels beside the segment seed from day one.
//
// Not because a second version is planned, but because the day one is needed
// is the day every radio in a segment must agree on which derivation it is
// using — and adding the field then means a flag day. It costs one byte in a
// descriptor and removes a category of migration.
const KDFVersion = 1

// kdfInfo names what the derived key is for. Domain separation is the whole
// job: the same seed must never produce this key and a channel key that are
// interchangeable.
const kdfInfo = "quiet-radio-transfer-auth-v1"

// MinSeedLen is the shortest segment seed this will derive from. A seed
// shorter than this is a mistake somewhere upstream, and deriving from it
// would produce a key that looks exactly as strong as a real one.
const MinSeedLen = 16

// TransferKey authenticates Radio Transfer frames within one segment.
type TransferKey struct {
	k []byte
}

// ErrSeedTooShort is a segment seed with too little entropy to derive from.
var ErrSeedTooShort = errors.New("radiotransfer: the segment seed is too short")

// DeriveTransferKey produces the segment's frame-authentication key.
//
// Every radio in a segment derives the same key from the same seed, which is
// what makes the segment a segment. The seed itself never travels here.
func DeriveTransferKey(segmentSeed []byte, kdfVersion uint32) (*TransferKey, error) {
	if len(segmentSeed) < MinSeedLen {
		return nil, fmt.Errorf("%w: %d bytes, minimum is %d",
			ErrSeedTooShort, len(segmentSeed), MinSeedLen)
	}
	if kdfVersion != KDFVersion {
		return nil, fmt.Errorf("radiotransfer: this build derives keys with "+
			"version %d, the segment asks for %d — every radio in a segment must "+
			"agree, so nothing is derived rather than deriving a key nobody else "+
			"holds", KDFVersion, kdfVersion)
	}
	k, err := hkdf.Key(sha256.New, segmentSeed, nil, kdfInfo, sha256.Size)
	if err != nil {
		return nil, err
	}
	return &TransferKey{k: k}, nil
}

// Tag authenticates a frame body, truncated to MACLen.
//
// Truncation is a deliberate trade on a carrier where one packet costs about
// two seconds of airtime: eight bytes is roughly 4% of a frame. It leaves a
// forgery attempt succeeding about once in 2^64 tries, and each try costs the
// attacker a full packet on a shared band — which is a far harder budget than
// the arithmetic alone suggests.
func (t *TransferKey) Tag(body []byte) []byte {
	h := hmac.New(sha256.New, t.k)
	h.Write(body)
	return h.Sum(nil)[:MACLen]
}

// Verify checks a tag in constant time.
func (t *TransferKey) Verify(body, mac []byte) bool {
	if len(mac) != MACLen {
		return false
	}
	return hmac.Equal(t.Tag(body), mac)
}

// MessageDigest is the proof that a reassembly is the message that was sent.
//
// Separate from the per-fragment CRC, and not a substitute for it: a CRC
// catches a mangled fragment before its airtime is wasted on assembly, and
// this catches a message assembled from fragments that were each individually
// fine — a spliced reassembly, a stale fragment from a previous transfer, a
// bug in this file.
func MessageDigest(msg []byte) [DigestLen]byte {
	sum := sha256.Sum256(msg)
	var out [DigestLen]byte
	copy(out[:], sum[:])
	return out
}
