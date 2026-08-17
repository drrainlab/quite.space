package pairing

import (
	"bytes"
	"strings"
	"testing"
)

func fixedSecret(b byte) [32]byte {
	var s [32]byte
	for i := range s {
		s[i] = b
	}
	return s
}

func fixedBinding(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

// Both screens show the same six digits — that is the whole ceremony.
func TestSameSessionSameSecretSameDigits(t *testing.T) {
	secret := fixedSecret(0xA1)
	binding := fixedBinding(0x42)
	parent, err := ConfirmDigits(secret, binding)
	if err != nil {
		t.Fatal(err)
	}
	child, err := ConfirmDigits(secret, binding)
	if err != nil {
		t.Fatal(err)
	}
	if parent != child {
		t.Fatalf("two ends of ONE session disagree: %s vs %s", parent, child)
	}
	if len(parent) != 6 || strings.Trim(parent, "0123456789") != "" {
		t.Fatalf("not six digits: %q", parent)
	}
}

// THE MITM DEFENCE, as arithmetic: an interceptor holds two TLS sessions,
// each exporting different material, so the two screens show different
// digits and the humans see it at a glance.
func TestAManInTheMiddleShowsDifferentDigitsOnTheTwoScreens(t *testing.T) {
	secret := fixedSecret(0xA1)
	left, err := ConfirmDigits(secret, fixedBinding(0x42))
	if err != nil {
		t.Fatal(err)
	}
	right, err := ConfirmDigits(secret, fixedBinding(0x43))
	if err != nil {
		t.Fatal(err)
	}
	if left == right {
		t.Fatal("two DIFFERENT sessions produced the same digits — the MITM defence is gone")
	}
}

// THE REPLAY DEFENCE: a recording of the tones replayed inside the window
// carries the same secret, but the replay negotiates a NEW session — new
// binding, different digits.
func TestAReplayedOfferShowsDifferentDigits(t *testing.T) {
	secret := fixedSecret(0xA1) // same recorded offer, same secret
	original, err := ConfirmDigits(secret, fixedBinding(0x42))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ConfirmDigits(secret, fixedBinding(0x99))
	if err != nil {
		t.Fatal(err)
	}
	if original == replayed {
		t.Fatal("a replayed offer showed the original's digits")
	}
}

// A leading zero is a third of all first digits: the digits are a STRING,
// and truncating "064213" to "64213" would teach people that five digits
// can be right.
func TestDigitsKeepTheirLeadingZeros(t *testing.T) {
	secret := fixedSecret(0x07)
	// Scan bindings until one yields a leading zero — deterministic inputs,
	// so this terminates at the same place every run.
	for b := byte(0); ; b++ {
		digits, err := ConfirmDigits(secret, fixedBinding(b))
		if err != nil {
			t.Fatal(err)
		}
		if len(digits) != 6 {
			t.Fatalf("%q is not six characters", digits)
		}
		if digits[0] == '0' {
			return // found one, and it kept its zero
		}
		if b == 255 {
			t.Skip("no leading zero in 256 deterministic tries — astronomically unlikely")
		}
	}
}

// DERIVING FROM NOTHING MUST BE IMPOSSIBLE. SessionBinding returns nil when
// the export fails; digits derived from a nil binding would be the same for
// EVERY session — which is precisely the property the digits exist to deny.
func TestNoDigitsWithoutARealSessionBinding(t *testing.T) {
	secret := fixedSecret(0xA1)
	if _, err := ConfirmDigits(secret, nil); err == nil {
		t.Fatal("digits derived from a nil session binding")
	}
	if _, err := ConfirmDigits(secret, []byte{1, 2, 3}); err == nil {
		t.Fatal("digits derived from a truncated session binding")
	}
	if _, err := ConfirmDigits([32]byte{}, fixedBinding(0x42)); err == nil {
		t.Fatal("digits derived from a zero secret")
	}
}

// The freight key is bound to the session the humans actually approved AND
// to the transcript of the ceremony that led there. Any of the three inputs
// changing means a different key — and a key derived from the secret alone
// would leave the six digits protecting the ceremony while the identity
// travelled under a key a recording could reconstruct.
func TestFreightKeyBindsSecretSessionAndTranscript(t *testing.T) {
	secret := fixedSecret(0xA1)
	binding := fixedBinding(0x42)
	transcript := TranscriptHash([]byte("offer"), []byte("hello"))

	base, err := FreightKey(secret, binding, transcript)
	if err != nil {
		t.Fatal(err)
	}
	same, err := FreightKey(secret, binding, transcript)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base, same) {
		t.Fatal("the two ends of one confirmed ceremony derived different keys")
	}
	for name, k := range map[string]func() ([]byte, error){
		"different session": func() ([]byte, error) {
			return FreightKey(secret, fixedBinding(0x43), transcript)
		},
		"different transcript": func() ([]byte, error) {
			return FreightKey(secret, binding, TranscriptHash([]byte("offer"), []byte("tampered")))
		},
		"different secret": func() ([]byte, error) {
			return FreightKey(fixedSecret(0xB2), binding, transcript)
		},
	} {
		other, err := k()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(base, other) {
			t.Fatalf("%s produced the SAME freight key", name)
		}
	}
}

// Domain separation: the digits and the freight key must never be the same
// bytes wearing two hats, or showing one would leak the other.
func TestDigitsAndFreightKeyAreDomainSeparated(t *testing.T) {
	secret := fixedSecret(0xA1)
	binding := fixedBinding(0x42)
	k, err := FreightKey(secret, binding, TranscriptHash())
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != 32 {
		t.Fatalf("freight key is %d bytes, want 32", len(k))
	}
	// Not a proof of independence — that is HKDF's job — but a regression
	// tripwire against someone unifying the two labels "for simplicity".
	if confirmInfo == freightInfo {
		t.Fatal("the confirmation and freight derivations share one label")
	}
}

// TranscriptHash must be UNAMBIGUOUS about part boundaries: ("ab","c") and
// ("a","bc") describe different ceremonies and must hash differently, or a
// party able to shift a boundary could equate two transcripts.
func TestTranscriptHashSeparatesPartBoundaries(t *testing.T) {
	a := TranscriptHash([]byte("ab"), []byte("c"))
	b := TranscriptHash([]byte("a"), []byte("bc"))
	if bytes.Equal(a, b) {
		t.Fatal("part boundaries vanish in the transcript hash")
	}
	if len(a) != 32 {
		t.Fatalf("transcript hash is %d bytes, want 32", len(a))
	}
}
