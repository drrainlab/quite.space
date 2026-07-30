// Quick links (SP-1): a Space Pass you can say out loud.
//
// A pass carries 32-byte secrets. Five words carry 55 bits. So the words
// cannot BE a pass — they can only point at one. That single arithmetic fact
// determines everything else here, and it is worth stating plainly because it
// is the difference between this and SP-0:
//
//	SP-0 (audio)  — the whole pass, sung. Air-gapped. Nothing is stored
//	                anywhere. ~39 seconds.
//	SP-1 (words)  — a pointer. The pass itself waits on the relay, encrypted
//	                under a key derived from the words, so the relay stays as
//	                blind as it is everywhere else. One second to say.
//
// 55 bits is not enough on its own and is not asked to be. It is defended by
// four things at once, and the design is honest about which does the work:
//
//	a slow KDF        makes each guess expensive rather than impossible
//	a short TTL       gives an attacker minutes, not months
//	single use        the relay's Collect drains on first read
//	relay rate limits bound online guessing
//
// An attacker who captures the ciphertext off the relay and grinds it offline
// is the case the KDF is for; the TTL is what makes that grind pointless.
// None of this is a substitute for the pass's own expiry and use count, which
// still apply after the words have done their job.
package quicklink

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	// WordCount is fixed at five. Four would be 44 bits, which the plan
	// weighed and rejected: the protection would then rest almost entirely
	// on the relay rather than on the token. Six is 66 bits and harder to
	// say. Five is the deliberate middle, and it is not configurable —
	// a token whose length varies is a token whose strength varies.
	WordCount = 5
	// BitsPerWord follows from len(Words) == 2048.
	BitsPerWord = 11
	// Bits is the whole token's entropy.
	Bits = WordCount * BitsPerWord // 55

	// Scheme is what a quick link looks like when written down. The words
	// are dot-separated because that reads as one address rather than five
	// loose words, and because a space is the one character every chat
	// client will break a line on.
	Scheme = "quiet://"
)

// index resolves a spoken or typed word to its value, forgiving the things
// people actually get wrong: case, surrounding punctuation, and the tail of a
// long word (the first four letters are unique by construction).
var index map[string]int

func init() {
	index = make(map[string]int, 2048*2)
	for i, w := range Words {
		index[w] = i
		// The list holds a few three-letter words, and no word is a prefix
		// of another, so the short key is simply the word itself there.
		if len(w) > 4 {
			index[w[:4]] = i
		}
	}
}

// Token is five words and the 55 bits they carry.
type Token struct {
	Words [WordCount]string
	// Secret is the raw entropy, big-endian, left-padded into 7 bytes.
	// The low bit of the first byte is unused: 55 bits do not fill 7 bytes,
	// and padding is cheaper than a bit-packed type nobody can read.
	Secret [7]byte
}

// New mints a fresh token from the system CSPRNG.
func New() (Token, error) {
	var raw [7]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Token{}, fmt.Errorf("quicklink: no randomness available: %w", err)
	}
	// Clear the bit that the encoding cannot carry, so Secret and Words are
	// always exact inverses of each other. Without this, a round trip would
	// quietly drop a bit and two equal-looking tokens would derive different
	// keys.
	raw[0] &= 0x7f
	return fromSecret(raw), nil
}

func fromSecret(raw [7]byte) Token {
	t := Token{Secret: raw}
	// 55 bits, big-endian, five 11-bit groups. Read the whole thing as one
	// number and peel from the bottom. The 56th bit — the top bit of the
	// first byte — is the one New clears; keeping the spare bit at the TOP
	// is what makes Secret and Words exact inverses.
	var n uint64
	for _, b := range raw {
		n = n<<8 | uint64(b)
	}
	for i := WordCount - 1; i >= 0; i-- {
		t.Words[i] = Words[n&0x7ff]
		n >>= BitsPerWord
	}
	return t
}

// String renders the token as a quick link.
func (t Token) String() string { return Scheme + strings.Join(t.Words[:], ".") }

// Phrase renders just the words, for saying out loud or writing on paper.
func (t Token) Phrase() string { return strings.Join(t.Words[:], " ") }

// ErrNotAToken means the input was not five recognisable words.
var ErrNotAToken = errors.New("quicklink: not a quick link")

// Parse reads a quick link back, in whatever shape a person hands it over:
// with or without the scheme, dot- or space- or dash-separated, any case,
// any surrounding whitespace, and words abbreviated to their first four
// letters. What it will NOT do is guess: an unrecognised word is an error,
// never a nearest match, because silently accepting the wrong word would
// derive the wrong key and report it as "link not found".
func Parse(s string) (Token, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, Scheme)
	s = strings.TrimPrefix(s, "quiet:")
	s = strings.Trim(s, "/")
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '.' || r == ' ' || r == '-' || r == '_' || r == '\t' || r == '\n'
	})
	if len(fields) != WordCount {
		return Token{}, fmt.Errorf("%w: expected %d words, found %d",
			ErrNotAToken, WordCount, len(fields))
	}
	var n uint64
	var t Token
	for i, f := range fields {
		f = strings.Trim(f, ".,;:!?'\"")
		v, ok := index[f]
		if !ok && len(f) > 4 {
			v, ok = index[f[:4]]
		}
		if !ok {
			return Token{}, fmt.Errorf("%w: %q is not one of the words", ErrNotAToken, f)
		}
		t.Words[i] = Words[v]
		n = n<<BitsPerWord | uint64(v)
	}
	for i := 6; i >= 0; i-- {
		t.Secret[i] = byte(n)
		n >>= 8
	}
	return t, nil
}

// A link is one of two kinds of door, and the guest has to know WHICH
// before it can choose how to read it — a personal door is collected once,
// a shared one is fetched without being emptied. But the kind lives inside
// the sealed payload, so deciding from it would be circular.
//
// Resolve it in the ADDRESS instead. The same secret derives two mailboxes
// under different labels; a link lives at exactly one. The guest tries the
// shared address first (a non-destructive read, so a wrong guess costs
// nothing) and falls back to collecting the personal one.
//
// This deliberately adds NO bit to the token: the five words, the 55 bits,
// the frozen list and the exact secret↔words inverse are all untouched, and
// there is no version to bump. Flipping the intended kind simply addresses
// an empty mailbox — the property a flag inside the token would have had to
// be bound into the hint to achieve anyway.
const (
	// DoorPersonal is spent by the first person who opens it.
	DoorPersonal = "single"
	// DoorShared stays readable until it expires; how many devices are
	// actually admitted is decided by the owner, not by the door.
	DoorShared = "shared"
)

// Hint is where the relay stores this link's payload: a fast hash, because
// the relay must be able to index it and an attacker learns nothing from a
// hint alone that the ciphertext does not already tell them. The SLOW
// derivation is Key's job, and the two must never be confused — a slow hint
// would make honest fetches expensive and a fast key would make guessing
// cheap.
func (t Token) Hint() []byte { return t.HintFor(DoorPersonal) }

// HintFor is the address of this link's mailbox for one kind of door.
func (t Token) HintFor(door string) []byte {
	return CollectHint(t.CapFor(door))
}

// Cap is the drain capability for this link's mailbox: the same fast digest,
// untruncated. Only a holder of the token secret can compute it, which is
// what stops a passer-by who saw the hint from emptying the box.
func (t Token) Cap() []byte { return t.CapFor(DoorPersonal) }

// CapFor derives the capability for one kind of door.
func CapForSecret(secret [7]byte, door string) []byte {
	h := sha256.New()
	h.Write([]byte("qs.quicklink.hint.v1"))
	h.Write(secret[:])
	h.Write([]byte(door))
	return h.Sum(nil)
}

// CapFor derives this token's capability for one kind of door.
func (t Token) CapFor(door string) []byte { return CapForSecret(t.Secret, door) }

// CollectHint mirrors the relay's address derivation. It lives here rather
// than importing transports/relay because protocol packages never depend on
// transports; the relay's PH-1 test pins the two against each other.
func CollectHint(cap []byte) []byte {
	h := sha256.New()
	h.Write([]byte("qp-relay-collect-v1:"))
	h.Write(cap)
	return h.Sum(nil)[:16]
}

// KDFSalt is the salt for the slow key derivation, bound to the hint so that
// two links that somehow shared entropy still would not share a key.
func (t Token) KDFSalt() []byte {
	return append([]byte("qs.quicklink.key.v1"), t.Hint()...)
}

// Valid reports whether every word is in the list. Cheap enough to run as the
// user types, so the UI can say "that is not one of the words" before anyone
// waits on a network round trip.
func Valid(s string) bool { _, err := Parse(s); return err == nil }

// Suggest offers completions for a partially typed word. It exists so the UI
// can help without ever guessing on the user's behalf — the choice stays with
// the person, which is the same rule Parse follows.
func Suggest(prefix string, limit int) []string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" || limit <= 0 {
		return nil
	}
	i := sort.SearchStrings(Words[:], prefix)
	var out []string
	for ; i < len(Words) && len(out) < limit; i++ {
		if !strings.HasPrefix(Words[i], prefix) {
			break
		}
		out = append(out, Words[i])
	}
	return out
}
