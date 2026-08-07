package publication

import (
	"bytes"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// A space-card may carry what the authoring client SAW when it looked at
// the target: kind_hint (CAT-0b, document key 13).
//
// It is a hint and settles nothing. The card's author signs it, so it says
// "when I listed this, it said it was a directory" — never what the target
// says now, and never anything about authorization. Inspecting the target
// is what settles the question; this only lets a listing draw the right
// affordance without probing every row, which is the fan-out CAT-0a
// refused.

func spaceCard(hint string) *Document {
	return &Document{
		DocumentID: [16]byte{1},
		Kind:       "space",
		Title:      "Experimental music",
		Visibility: "public-intent",
		KindHint:   hint,
		Blocks: []Block{{
			ID: "l1", Type: "link",
			RawProps: EncodeTextProps(TextProps{Text: "qs:abc"}),
		}},
	}
}

// countKey walks the top-level map and counts how often a key appears —
// the only way to prove "encoded ONCE" rather than assume it.
func countKey(t *testing.T, raw []byte, want uint64) int {
	t.Helper()
	d := codec.NewDecoder(raw)
	mr, err := d.ReadMapHeader()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		k, ok, err := mr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return n
		}
		if k == want {
			n++
		}
		if err := d.SkipItem(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAKindHintRoundTripsOnTheWire(t *testing.T) {
	doc := spaceCard("directory")
	if err := Validate(doc, func(string) bool { return true }); err != nil {
		t.Fatalf("a hinted space-card is a legal document: %v", err)
	}
	raw := doc.Encode()
	if n := countKey(t, raw, docKeyKindHint); n != 1 {
		t.Fatalf("key 13 appears %d times", n)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.KindHint != "directory" {
		t.Fatalf("the hint did not survive: %q", back.KindHint)
	}
	if len(back.RawExtra) != 0 {
		t.Fatal("a key this build knows was also kept as an unknown")
	}
}

func TestACardWithoutAHintIsUnchangedOnTheWire(t *testing.T) {
	doc := spaceCard("")
	if n := countKey(t, doc.Encode(), docKeyKindHint); n != 0 {
		t.Fatal("an unhinted card emitted key 13")
	}
}

// THE INVARIANT TEST. retainableExtra's own comment promises that "when a
// later build learns key 13, that key stops being retainable here and
// starts being written from its typed field — never both". This is the
// build that learns it.
//
// The shape is what an older build produced: it decoded a newer document,
// did not know key 13, and kept the bytes in RawExtra. A document carrying
// both must still encode the key exactly once, from the typed field, or the
// map goes out of ascending order and nobody can decode it at all.
func TestAKeyThirteenPassengerIsNotEncodedTwice(t *testing.T) {
	doc := spaceCard("directory")
	doc.RawExtra = []Extra{{
		Key: docKeyKindHint,
		Raw: codec.AppendText(nil, "space"), // what the older build kept
	}}

	raw := doc.Encode()
	if n := countKey(t, raw, docKeyKindHint); n != 1 {
		t.Fatalf("key 13 was encoded %d times — the passenger was not filtered", n)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatalf("the document no longer decodes: %v", err)
	}
	if back.KindHint != "directory" {
		t.Fatalf("the passenger won over the typed field: %q", back.KindHint)
	}
}

// A key ABOVE the known range is still kept, so learning key 13 did not
// close the door behind it.
func TestAKeyFourteenPassengerStillSurvives(t *testing.T) {
	doc := spaceCard("directory")
	doc.RawExtra = []Extra{{Key: 14, Raw: codec.AppendText(nil, "from the future")}}

	back, err := Decode(doc.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(back.RawExtra) != 1 || back.RawExtra[0].Key != 14 {
		t.Fatalf("a later key was dropped: %+v", back.RawExtra)
	}
	if !bytes.Equal(back.RawExtra[0].Raw, codec.AppendText(nil, "from the future")) {
		t.Fatal("a later key's bytes were altered")
	}
}

// The hint is only meaningful on a card that points somewhere. Without this
// key 13 becomes a general-purpose annotation slot, and it will be used as
// one.
func TestAKindHintIsOnlyForASpaceCard(t *testing.T) {
	doc := spaceCard("directory")
	doc.Kind = "article"
	if err := Validate(doc, func(string) bool { return true }); err == nil {
		t.Fatal("an article was allowed to carry a hint about a target it has not got")
	}
}

// Strict on write, tolerant on read — the same asymmetry qp.kind uses, for
// the same reason: garbage is never signed, and garbage that arrives
// already signed from a later build is ignored rather than fatal.
func TestAnUnknownHintIsRefusedAtAuthoringAndSurvivesDecode(t *testing.T) {
	if err := Validate(spaceCard("gallery"), func(string) bool { return true }); err == nil {
		t.Fatal("an unknown hint was accepted on the way in")
	}
	// Hand-built, as a later build would have written it.
	raw := codec.AppendMap(nil, 5)
	raw = codec.AppendUint(raw, docKeyID)
	raw = codec.AppendBytes(raw, make([]byte, 16))
	raw = codec.AppendUint(raw, docKeyKind)
	raw = codec.AppendText(raw, "space")
	raw = codec.AppendUint(raw, docKeyTitle)
	raw = codec.AppendText(raw, "somewhere")
	raw = codec.AppendUint(raw, docKeyVisibility)
	raw = codec.AppendText(raw, "public-intent")
	raw = codec.AppendUint(raw, docKeyKindHint)
	raw = codec.AppendText(raw, "gallery")

	back, err := Decode(raw)
	if err != nil {
		t.Fatalf("an unknown hint made the whole card unreadable: %v", err)
	}
	if back.KindHint != "gallery" {
		t.Fatalf("the bytes were altered on the way through: %q", back.KindHint)
	}
}
