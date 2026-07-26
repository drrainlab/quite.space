package quicklink

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSealRoundTrip(t *testing.T) {
	tok, err := New()
	if err != nil {
		t.Fatal(err)
	}
	want := Payload{PassLink: "relay.example:7411\nAAAAsomepassenvelope", From: "gleb", Space: "line"}
	sealed, err := Seal(tok, want)
	if err != nil {
		t.Fatal(err)
	}
	// The guest has nothing but the words.
	spoken, err := Parse(tok.Phrase())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(spoken, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("payload changed: %+v → %+v", want, got)
	}
}

// The whole security argument rests on the relay learning nothing. If the
// pass link ever appeared in the bytes handed to Put, everything else here
// would be theatre.
func TestSealedBytesRevealNothing(t *testing.T) {
	tok, _ := New()
	p := Payload{PassLink: "relay.example:7411\nSECRETPASSENVELOPE", From: "gleb", Space: "the quiet line"}
	sealed, err := Seal(tok, p)
	if err != nil {
		t.Fatal(err)
	}
	body := string(sealed)
	for _, leak := range []string{p.PassLink, "SECRETPASSENVELOPE", p.From, p.Space} {
		if strings.Contains(body, leak) {
			t.Fatalf("the sealed body contains %q in the clear", leak)
		}
	}
	// Nor may the words or the raw secret be recoverable from what is stored.
	for _, w := range tok.Words {
		if strings.Contains(body, w) {
			t.Fatalf("the sealed body contains the word %q", w)
		}
	}
	if strings.Contains(body, string(tok.Secret[:])) {
		t.Fatal("the sealed body contains the token secret")
	}
	// The hint is what the relay indexes by; it must not be inside the body
	// too, or a dump of stored bodies would be self-indexing.
	if strings.Contains(body, string(tok.Hint())) {
		t.Fatal("the sealed body repeats its own hint")
	}
}

func TestWrongTokenDoesNotOpen(t *testing.T) {
	a, _ := New()
	b, _ := New()
	sealed, err := Seal(a, Payload{PassLink: "x\ny"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(b, sealed); !errors.Is(err, ErrWrongToken) {
		t.Fatalf("a different token opened the box, or failed oddly: %v", err)
	}
	// Tampering fails the same way — the AEAD is the authority, not a
	// length check or a version byte.
	bad := append([]byte(nil), sealed...)
	bad[len(bad)-1] ^= 0x01
	if _, err := Open(a, bad); !errors.Is(err, ErrWrongToken) {
		t.Fatalf("a tampered box opened, or failed oddly: %v", err)
	}
}

// A near-miss must be indistinguishable from any other miss. If "wrong words"
// and "corrupt bytes" reported differently, an attacker grinding the 55-bit
// space would learn when they were close.
func TestFailureIsIndistinguishable(t *testing.T) {
	a, _ := New()
	sealed, _ := Seal(a, Payload{PassLink: "x\ny"})
	wrong, _ := New()
	_, e1 := Open(wrong, sealed)
	_, e2 := Open(a, []byte{0xa1, 0x01, 0x01})
	if e1.Error() != e2.Error() {
		t.Fatalf("a wrong token and corrupt bytes report differently:\n %v\n %v", e1, e2)
	}
}

// The KDF is the part of the budget we bought. If someone tunes it down to
// make a test suite faster, this says so out loud.
func TestKeyDerivationIsActuallySlow(t *testing.T) {
	if testing.Short() {
		t.Skip("timing check")
	}
	tok, _ := New()
	start := time.Now()
	if _, err := tokenAEAD(tok); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	// Generous, because CI machines vary wildly — this is a tripwire for
	// "someone dropped N by an order of magnitude", not a benchmark.
	if took < 50*time.Millisecond {
		t.Fatalf("key derivation took %v, which is too fast to be costing an "+
			"attacker anything; a 55-bit secret cannot afford a cheap KDF", took)
	}
	t.Logf("scrypt N=2^17 took %v on this machine", took)
}
