package quicklink

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The list is a wire format in the most literal sense: somebody may have a
// token written on paper. Reordering it, or replacing a single word, silently
// turns their link into a different one — it will parse, derive a different
// key, and fail with "link not found" rather than anything that points at the
// cause. This test is the tripwire.
func TestWordlistIsFrozen(t *testing.T) {
	h := sha256.Sum256([]byte(strings.Join(Words[:], "\n")))
	const want = "8bee6f9ef0e60f78569032b91f2e1b709c1e81e81bb4b0d403d023ccd302b6a9"
	if got := hex.EncodeToString(h[:]); got != want {
		t.Fatalf("the wordlist changed.\n got %s\nwant %s\n\n"+
			"If this was deliberate, every quick link ever written down is now "+
			"invalid. Bump the hint/key domain strings too, so an old token "+
			"fails loudly instead of resolving to the wrong payload.", got, want)
	}
}

// The two properties that make a spoken token usable.
func TestWordsAreUnambiguous(t *testing.T) {
	if len(Words) != 1<<BitsPerWord {
		t.Fatalf("wordlist is %d words; %d bits per word needs %d",
			len(Words), BitsPerWord, 1<<BitsPerWord)
	}
	seen := map[string]string{}
	for _, w := range Words {
		if len(w) < 3 {
			t.Fatalf("%q is too short to hear", w)
		}
		for _, r := range w {
			if r < 'a' || r > 'z' {
				t.Fatalf("%q is not plain lowercase ascii", w)
			}
		}
		k := w
		if len(k) > 4 {
			k = k[:4]
		}
		if prev, dup := seen[k]; dup {
			t.Fatalf("%q and %q share the prefix %q, so neither can be typed short",
				prev, w, k)
		}
		seen[k] = w
	}
	// Sorted, because Suggest binary-searches it and because index == value
	// must be stable and reviewable.
	for i := 1; i < len(Words); i++ {
		if Words[i-1] >= Words[i] {
			t.Fatalf("wordlist is not sorted at %d: %q then %q", i, Words[i-1], Words[i])
		}
	}
	// No word may be a proper prefix of another, or a short word swallows a
	// long one when somebody abbreviates.
	for _, a := range Words {
		for _, b := range Words {
			if a != b && strings.HasPrefix(b, a) {
				t.Fatalf("%q is a prefix of %q", a, b)
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for range 2000 {
		tok, err := New()
		if err != nil {
			t.Fatal(err)
		}
		back, err := Parse(tok.String())
		if err != nil {
			t.Fatalf("%s: %v", tok, err)
		}
		if back.Secret != tok.Secret {
			t.Fatalf("%s: secret changed on the way back: %x → %x",
				tok, tok.Secret, back.Secret)
		}
		if back.Words != tok.Words {
			t.Fatalf("%s: words changed: %v → %v", tok, tok.Words, back.Words)
		}
	}
}

// The bit the encoding cannot carry must be cleared at mint, or two tokens
// that read identically would derive different keys — a failure that would
// look exactly like a lost link and be nearly impossible to diagnose.
func TestSecretHasNoBitTheWordsCannotCarry(t *testing.T) {
	for range 500 {
		tok, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if tok.Secret[0]&0x80 != 0 {
			t.Fatalf("%s: secret carries a bit the words drop: %x", tok, tok.Secret)
		}
	}
}

func TestParseForgivesHowPeopleActuallyWriteIt(t *testing.T) {
	tok, err := New()
	if err != nil {
		t.Fatal(err)
	}
	w := tok.Words
	shortened := make([]string, WordCount)
	for i, x := range w {
		shortened[i] = x
		if len(x) > 4 {
			shortened[i] = x[:4]
		}
	}
	for _, form := range []string{
		tok.String(),
		strings.ToUpper(tok.String()),
		strings.Join(w[:], " "),
		strings.Join(w[:], "-"),
		"  " + strings.Join(w[:], ".") + "  ",
		"quiet:" + strings.Join(w[:], "."),
		strings.Join(shortened, "."), // abbreviated to four letters
	} {
		got, err := Parse(form)
		if err != nil {
			t.Fatalf("%q: %v", form, err)
		}
		if got.Secret != tok.Secret {
			t.Fatalf("%q decoded to a different token", form)
		}
	}
}

// Refusing is the whole point: a nearest-match would derive the wrong key and
// surface as "link not found", sending the user to debug the wrong thing.
func TestParseNeverGuesses(t *testing.T) {
	for _, bad := range []string{
		"", "quiet://", "moss ember", "moss.ember.tide.wren.notaword",
		"moss.ember.tide.wren", "moss.ember.tide.wren.moss.moss",
		"xylophone.ember.tide.wren.moss",
	} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
		if Valid(bad) {
			t.Fatalf("%q reported valid", bad)
		}
	}
}

// A fast hint and a slow key are different jobs; they must not collide, and
// the hint must not be derivable into the key.
func TestHintAndSaltAreDistinct(t *testing.T) {
	a, _ := New()
	b, _ := New()
	if string(a.Hint()) == string(b.Hint()) {
		t.Fatal("two tokens share a hint")
	}
	if string(a.Hint()) == string(a.KDFSalt()) {
		t.Fatal("hint and key salt are the same value")
	}
	if len(a.Hint()) != 16 {
		t.Fatalf("hint is %d bytes, relay hints are 16", len(a.Hint()))
	}
	// Same token, same hint — otherwise the guest could never find it.
	again, err := Parse(a.String())
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Hint()) != string(a.Hint()) {
		t.Fatal("a re-parsed token points somewhere else")
	}
}

func TestSuggestHelpsWithoutChoosing(t *testing.T) {
	got := Suggest("emb", 5)
	if len(got) == 0 {
		t.Fatal("no suggestions for a real prefix")
	}
	for _, w := range got {
		if !strings.HasPrefix(w, "emb") {
			t.Fatalf("%q does not start with the prefix", w)
		}
	}
	if s := Suggest("zzzz", 5); len(s) != 0 {
		t.Fatalf("invented suggestions for a nonexistent prefix: %v", s)
	}
	if s := Suggest("", 5); len(s) != 0 {
		t.Fatal("suggested something for an empty prefix")
	}
}
