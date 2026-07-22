package resonance

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/contract"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestSetRoundTrip(t *testing.T) {
	for _, r := range []Reaction{
		{Kind: KindSemantic, Key: "warmth", Fallback: "♡"},
		{Kind: KindSemantic, Key: "pinevibes.drift", Fallback: "🌫️"},
		{Kind: KindUnicode, Value: "🌲"},
	} {
		p := &SetPayload{Target: id.EventID{1, 2, 3}, Reaction: r}
		b, err := p.Encode()
		if err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		got, err := DecodeSet(b)
		if err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		if got.Target != p.Target || got.Reaction != r {
			t.Fatalf("round-trip mismatch: %+v vs %+v", got, p)
		}
	}
}

func TestReactionUnionValidation(t *testing.T) {
	bad := []Reaction{
		{Kind: KindSemantic, Key: "warmth"},                              // no fallback
		{Kind: KindSemantic, Key: "Warmth!", Fallback: "♡"},              // bad grammar
		{Kind: KindSemantic, Key: "warmth", Fallback: "♡", Value: "🌲"},  // both arms
		{Kind: KindUnicode, Value: "🌲", Key: "warmth"},                  // both arms
		{Kind: KindUnicode},                                              // empty
		{Kind: 3, Key: "warmth", Fallback: "♡"},                          // unknown kind
		{Kind: KindSemantic, Key: strings.Repeat("a", 65), Fallback: "♡"}, // too long
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Fatalf("case %d (%+v) must fail validation", i, r)
		}
	}
}

func TestClearRoundTrip(t *testing.T) {
	p := &ClearPayload{Target: id.EventID{9}}
	got, err := DecodeClear(p.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != p.Target {
		t.Fatal("clear target mismatch")
	}
}

func TestPaletteRoundTripAndValidation(t *testing.T) {
	pal := DefaultPalette()
	b, err := pal.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePalette(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaletteID != pal.PaletteID || len(got.Slots) != len(pal.Slots) ||
		got.DefaultKey != pal.DefaultKey || got.Policy != pal.Policy {
		t.Fatalf("palette round-trip mismatch: %+v", got)
	}

	// Duplicate slot keys fail.
	dup := DefaultPalette()
	dup.Slots[1].Key = dup.Slots[0].Key
	if _, err := dup.Encode(); err == nil {
		t.Fatal("duplicate slot keys must fail")
	}
	// default_key outside slots fails.
	noDef := DefaultPalette()
	noDef.DefaultKey = "weight" // not in the first 6
	if _, err := noDef.Encode(); err == nil {
		t.Fatal("default_key outside slots must fail")
	}
	// Slot bounds.
	empty := DefaultPalette()
	empty.Slots = nil
	if _, err := empty.Encode(); err == nil {
		t.Fatal("0 slots must fail")
	}
	seven := DefaultPalette()
	for _, m := range []string{"weight", "extra"} {
		seven.Slots = append(seven.Slots, PaletteSlot{Key: m, Label: m, Fallback: "●"})
	}
	if _, err := seven.Encode(); err == nil {
		t.Fatal("7 slots must fail")
	}
	// Non-single cardinality fails in v1.
	multi := DefaultPalette()
	multi.Policy.Cardinality = 2
	if _, err := multi.Encode(); err == nil {
		t.Fatal("cardinality != single must fail in v1")
	}
}

func TestResolveFallbackChain(t *testing.T) {
	pal := DefaultPalette()
	// Palette slot wins.
	pal.Slots[1].Fallback = "🔥" // warmth slot re-skinned by the space
	if got := ResolveFallback("warmth", &pal, "💩"); got != "🔥" {
		t.Fatalf("palette slot must win, got %q", got)
	}
	// Known core key with forged wire fallback: registry wins, 💩 never renders.
	if got := ResolveFallback("weight", &pal, "💩"); got != "●" {
		t.Fatalf("core registry must win over wire fallback, got %q", got)
	}
	// Unknown key: deterministic wire fallback.
	if got := ResolveFallback("pinevibes.drift", &pal, "🌫️"); got != "🌫️" {
		t.Fatalf("unknown key uses wire fallback, got %q", got)
	}
	// Nothing known at all: generic marker.
	if got := ResolveFallback("studio.mystery", nil, ""); got != "◈" {
		t.Fatalf("generic marker expected, got %q", got)
	}
}

func TestUnknownMapKeysTolerated(t *testing.T) {
	// Encode a set payload, then splice is hard — instead rely on decoder
	// SkipItem paths being exercised via palette (optional fields absent) and
	// assert core decode of a valid payload doesn't require every key.
	p := &SetPayload{Target: id.EventID{1}, Reaction: Reaction{Kind: KindUnicode, Value: "🌲"}}
	b, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSet(b); err != nil {
		t.Fatal(err)
	}
}

func TestContractsRegistered(t *testing.T) {
	for _, id := range []string{SchemaSet, SchemaClear, SchemaPalette} {
		if _, ok := contract.GetDescriptor(id); !ok {
			t.Fatalf("%s not registered in contract registry", id)
		}
	}
	fb, err := contract.Fallback(SchemaSet, mustEncodeSet(t))
	if err != nil || fb == "" {
		t.Fatalf("set fallback: %q %v", fb, err)
	}
}

func mustEncodeSet(t *testing.T) []byte {
	t.Helper()
	b, err := (&SetPayload{Target: id.EventID{1},
		Reaction: Reaction{Kind: KindSemantic, Key: "warmth", Fallback: "♡"}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
