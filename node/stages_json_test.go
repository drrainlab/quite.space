package node

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/publication"
)

func sampleSequence() *publication.Atmosphere {
	return &publication.Atmosphere{
		Visual: publication.Visual{
			Stages: []publication.Stage{
				{Anchor: "b1", Image: strings.Repeat("a1", 32)},
				{Anchor: "b2", Image: strings.Repeat("b2", 32),
					Transition: publication.TransitionFade, DurationMs: 900},
				{Anchor: "b3", Image: strings.Repeat("c3", 32),
					Audio: strings.Repeat("d4", 32), Transition: publication.TransitionCut},
			},
			Palette: []publication.PaletteToken{{Name: "ground", Hex: "#0b1020"}},
		},
		Fall: publication.Fallback{Text: "Three photographs, in order."},
	}
}

// The composer edits THIS shape and posts it back, so a field the round trip
// drops is a field the next save deletes.
func TestASequenceSurvivesTheAuthoringRoundTrip(t *testing.T) {
	doc := &publication.Document{
		Kind: "article", Title: "Three photographs", Visibility: "space",
		Atmosphere: sampleSequence(),
	}
	b, err := json.Marshal(documentToJSON(doc))
	if err != nil {
		t.Fatal(err)
	}
	var dj documentJSON
	if err := json.Unmarshal(b, &dj); err != nil {
		t.Fatal(err)
	}
	back, err := documentFromJSON(dj)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Atmosphere.Visual.Stages
	if len(got) != 3 {
		t.Fatalf("expected three stages, got %d — the round trip ate the story", len(got))
	}
	if got[1].Anchor != "b2" || got[1].Transition != publication.TransitionFade ||
		got[1].DurationMs != 900 {
		t.Fatalf("stage 2 lost its shape: %+v", got[1])
	}
	// Not rendered yet, and carried anyway: dropping it here would delete it
	// on the next save, long before anything could play it.
	if got[2].Audio != strings.Repeat("d4", 32) {
		t.Fatalf("stage audio was dropped: %+v", got[2])
	}
	// A sequence has no scene, and this layer must not invent one.
	if back.Atmosphere.Visual.Scene != "" || back.Atmosphere.Visual.Seed != 0 {
		t.Fatal("the round trip gave a sequence a scene")
	}
}

// The contract retains unknown wire keys, but the composer does not edit wire
// bytes — it edits the JSON. Without carriage here the wire-level promise is
// true and useless.
func TestUnknownAtmosphereKeysSurviveTheAuthoringRoundTrip(t *testing.T) {
	a := sampleSequence()
	a.Visual.RawExtra = []publication.Extra{
		{Key: 40, Raw: codec.AppendText(nil, "from a newer client")},
	}
	a.RawExtra = []publication.Extra{{Key: 20, Raw: codec.AppendUint(nil, 7)}}
	doc := &publication.Document{
		Kind: "article", Title: "Three photographs", Visibility: "space", Atmosphere: a,
	}
	b, err := json.Marshal(documentToJSON(doc))
	if err != nil {
		t.Fatal(err)
	}
	var dj documentJSON
	if err := json.Unmarshal(b, &dj); err != nil {
		t.Fatal(err)
	}
	back, err := documentFromJSON(dj)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Atmosphere.Visual.RawExtra) != 1 ||
		back.Atmosphere.Visual.RawExtra[0].Key != 40 {
		t.Fatalf("the visual passenger did not survive: %+v",
			back.Atmosphere.Visual.RawExtra)
	}
	if len(back.Atmosphere.RawExtra) != 1 || back.Atmosphere.RawExtra[0].Key != 20 {
		t.Fatalf("the atmosphere passenger did not survive: %+v",
			back.Atmosphere.RawExtra)
	}
}

// A mangled passenger would be signed into a permanently undecodable post, so
// this is the one thing this layer checks for itself.
func TestAMangledPassengerIsRefusedRatherThanSigned(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"not base64", "!!!!"},
		{"not a CBOR item", base64.StdEncoding.EncodeToString([]byte{0xff, 0xff})},
		{"two items", base64.StdEncoding.EncodeToString(
			codec.AppendUint(codec.AppendUint(nil, 1), 2))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aj := &atmosphereJSON{
				Visual: atmoVisualJSON{
					Extra: []atmoExtraJSON{{Key: 40, Raw: tc.raw}},
				},
				Fallback: atmoFallbackJSON{Text: "t"},
			}
			if _, err := atmosphereFromJSON(aj); err == nil {
				t.Fatal("a mangled passenger was accepted for signing")
			}
		})
	}
}

// A post with no atmosphere still carries no atmosphere key, and one with a
// scene still carries no stages key — this gate adds nothing to either.
func TestSceneRecipesKeepTheirJSONShape(t *testing.T) {
	doc := &publication.Document{
		Kind: "article", Title: "With weather", Visibility: "space",
		Atmosphere: sampleAtmosphere(),
	}
	b, err := json.Marshal(documentToJSON(doc))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "stages") || strings.Contains(string(b), "\"extra\"") {
		t.Fatalf("a scene recipe grew sequence keys: %s", b)
	}
}
