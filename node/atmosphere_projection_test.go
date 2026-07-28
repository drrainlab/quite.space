package node

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/publication"
)

func sampleAtmosphere() *publication.Atmosphere {
	return &publication.Atmosphere{
		Visual: publication.Visual{
			Scene: "drift@1", Seed: 0xfeedface,
			Params: []publication.Param{
				{Name: "density", Value: 300},
				{Name: "bloom", Value: 850},
			},
			Palette: []publication.PaletteToken{
				{Name: "ground", Hex: "#0b1020"},
				{Name: "accent", Hex: "#7ab4e0"},
			},
		},
		Audio: &publication.Audio{
			Asset: strings.Repeat("ab", 16), Mode: "loop",
			Gain: 700, FadeInMs: 400, FadeOutMs: 900,
		},
		React: &publication.Reactive{Audio: true},
		Fall:  publication.Fallback{Text: "Slow motes over a dark field."},
		Derived: &publication.Derived{
			RecipeHash:    []byte(strings.Repeat("\x11", 32)),
			PublicationID: strings.Repeat("cd", 16),
			RevisionHash:  strings.Repeat("ef", 32),
		},
	}
}

// A publication without an atmosphere must keep exactly the JSON shape it had
// before AM-1 existed. Not an empty object, not a null — no key at all. The
// reader treats the field's presence as "this post has another layer", so an
// empty object would answer yes and then render a region with nothing in it.
func TestADocumentWithoutAtmosphereCarriesNoSuchKey(t *testing.T) {
	doc := &publication.Document{
		Kind: "article", Title: "An ordinary post", Visibility: "space",
	}
	b, err := json.Marshal(documentToJSON(doc))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "atmosphere") {
		t.Fatalf("an ordinary post grew an atmosphere key: %s", b)
	}
	// And the round trip must not conjure one either — an editor that saves a
	// post it did not change must not add a field to it.
	var dj documentJSON
	if err := json.Unmarshal(b, &dj); err != nil {
		t.Fatal(err)
	}
	back, err := documentFromJSON(dj)
	if err != nil {
		t.Fatal(err)
	}
	if back.Atmosphere != nil {
		t.Fatal("the round trip invented an atmosphere")
	}
}

// The projection must hand over what the validated Document holds, with no
// renaming, defaulting or re-derivation. Anything this layer changed would be
// a second opinion about a recipe that has already been checked once.
func TestTheProjectionPassesTheRecipeThroughUnchanged(t *testing.T) {
	a := sampleAtmosphere()
	doc := &publication.Document{
		Kind: "article", Title: "With weather", Visibility: "space", Atmosphere: a,
	}
	b, err := json.MarshalIndent(documentToJSON(doc), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Atmosphere struct {
			Visual struct {
				Scene   string `json:"scene"`
				Seed    uint64 `json:"seed"`
				Params  []struct {
					Name  string `json:"name"`
					Value uint64 `json:"value"`
				} `json:"params"`
				Palette []struct {
					Name string `json:"name"`
					Hex  string `json:"hex"`
				} `json:"palette"`
			} `json:"visual"`
			Audio struct {
				Asset     string `json:"asset"`
				Mode      string `json:"mode"`
				Gain      uint64 `json:"gain"`
				FadeInMs  uint64 `json:"fade_in_ms"`
				FadeOutMs uint64 `json:"fade_out_ms"`
			} `json:"audio"`
			Reactive struct {
				Audio   bool `json:"audio"`
				Pointer bool `json:"pointer"`
			} `json:"reactive"`
			Fallback struct {
				Text   string `json:"text"`
				Poster string `json:"poster"`
			} `json:"fallback"`
			DerivedFrom struct {
				RecipeHash    string `json:"recipe_hash"`
				PublicationID string `json:"publication_id"`
				RevisionHash  string `json:"revision_hash"`
			} `json:"derived_from"`
		} `json:"atmosphere"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	at := got.Atmosphere
	if at.Visual.Scene != "drift@1" || at.Visual.Seed != 0xfeedface {
		t.Fatalf("visual identity changed in transit: %+v", at.Visual)
	}
	// Permille stays permille. A projection that divided here would leave the
	// client dividing a second time, or not at all.
	if len(at.Visual.Params) != 2 || at.Visual.Params[0].Name != "density" ||
		at.Visual.Params[0].Value != 300 || at.Visual.Params[1].Value != 850 {
		t.Fatalf("params were reinterpreted: %+v", at.Visual.Params)
	}
	if len(at.Visual.Palette) != 2 || at.Visual.Palette[0].Hex != "#0b1020" {
		t.Fatalf("palette changed: %+v", at.Visual.Palette)
	}
	if at.Audio.Asset != strings.Repeat("ab", 16) || at.Audio.Mode != "loop" ||
		at.Audio.Gain != 700 || at.Audio.FadeInMs != 400 || at.Audio.FadeOutMs != 900 {
		t.Fatalf("audio changed: %+v", at.Audio)
	}
	if !at.Reactive.Audio || at.Reactive.Pointer {
		t.Fatalf("reactive flags changed: %+v", at.Reactive)
	}
	if at.Fallback.Text != "Slow motes over a dark field." {
		t.Fatalf("fallback text changed: %q", at.Fallback.Text)
	}
	// Lineage travels now, though only AM-5 reads it: the alternative is
	// widening this wire shape a second time for one feature.
	if at.DerivedFrom.RecipeHash != hex.EncodeToString([]byte(strings.Repeat("\x11", 32))) {
		t.Fatalf("recipe hash changed: %q", at.DerivedFrom.RecipeHash)
	}
	if at.DerivedFrom.PublicationID != strings.Repeat("cd", 16) ||
		at.DerivedFrom.RevisionHash != strings.Repeat("ef", 32) {
		t.Fatalf("lineage locators changed: %+v", at.DerivedFrom)
	}
}

// The composer round-trips a document through this layer on every edit, so a
// field the round trip drops is a field the next save deletes. This is the
// test that would have caught an atmosphere silently disappearing the first
// time somebody fixed a typo in a post's title.
func TestEditingAPostDoesNotStripItsAtmosphere(t *testing.T) {
	doc := &publication.Document{
		Kind: "article", Title: "With weather", Visibility: "space",
		Atmosphere: sampleAtmosphere(),
	}
	b, err := json.Marshal(documentToJSON(doc))
	if err != nil {
		t.Fatal(err)
	}
	var dj documentJSON
	if err := json.Unmarshal(b, &dj); err != nil {
		t.Fatal(err)
	}
	dj.Title = "With weather, retitled"
	back, err := documentFromJSON(dj)
	if err != nil {
		t.Fatal(err)
	}
	if back.Atmosphere == nil {
		t.Fatal("editing the title deleted the atmosphere")
	}
	// The strongest statement available: the recipe that comes back encodes to
	// the same canonical bytes it went in as. Anything lost, reordered or
	// re-derived anywhere in the round trip changes this digest.
	if got, want := back.Atmosphere.RecipeHash(), doc.Atmosphere.RecipeHash(); string(got) != string(want) {
		t.Fatalf("the recipe changed across an edit:\n got %x\nwant %x", got, want)
	}
	if back.Atmosphere.Derived == nil ||
		string(back.Atmosphere.Derived.RecipeHash) != strings.Repeat("\x11", 32) {
		t.Fatal("lineage did not survive the round trip")
	}
	if back.Atmosphere.Audio == nil || back.Atmosphere.Audio.FadeOutMs != 900 {
		t.Fatal("audio did not survive the round trip")
	}
}

// Bounds belong to publication.Validate and nowhere else. The projection layer
// deliberately does not re-check them, so this pins that an illegal recipe is
// still refused — by the validator, on the way to being signed, rather than
// by a second opinion here that could drift from it.
func TestTheProjectionDoesNotBecomeASecondValidator(t *testing.T) {
	a := sampleAtmosphere()
	a.Visual.Params[0].Value = 5000 // permille, way past the cap
	dj := documentToJSON(&publication.Document{
		Kind: "article", Title: "Bad", Visibility: "space", Atmosphere: a,
	})
	back, err := documentFromJSON(dj)
	if err != nil {
		t.Fatalf("the projection refused a document it should merely carry: %v", err)
	}
	if err := publication.Validate(back, func(string) bool { return true }); err == nil {
		t.Fatal("an out-of-range parameter passed validation")
	}
}
