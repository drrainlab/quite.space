package publication_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/publication"
)

const (
	hexAsset32 = "0123456789abcdef0123456789abcdef" // 16 bytes, legacy handle
	hexAsset64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func sampleAtmosphere() *publication.Atmosphere {
	return &publication.Atmosphere{
		Visual: publication.Visual{
			Scene: "organic-field@1",
			Seed:  82374,
			Params: []publication.Param{
				{Name: "density", Value: 400},
				{Name: "motion", Value: 200},
			},
			Palette: []publication.PaletteToken{
				{Name: "moss", Hex: "#4a5d3f"},
				{Name: "violet", Hex: "#6b5b95"},
			},
		},
		Audio: &publication.Audio{
			Asset: hexAsset64, Mode: publication.AudioLoop,
			Gain: 300, FadeInMs: 1800, FadeOutMs: 1200,
		},
		React: &publication.Reactive{Audio: true},
		Fall: publication.Fallback{
			Text: "A slow green field, breathing.", Poster: hexAsset32,
		},
	}
}

func docWith(a *publication.Atmosphere) *publication.Document {
	return &publication.Document{
		DocumentID: [16]byte{1}, Kind: "article", Title: "Night Moss",
		Visibility: "space", Atmosphere: a,
	}
}

func TestAtmosphereRoundTrip(t *testing.T) {
	doc := docWith(sampleAtmosphere())
	back, err := publication.Decode(doc.Encode())
	if err != nil {
		t.Fatal(err)
	}
	got := back.Atmosphere
	if got == nil {
		t.Fatal("atmosphere did not survive the round trip")
	}
	want := doc.Atmosphere
	if got.Visual.Scene != want.Visual.Scene || got.Visual.Seed != want.Visual.Seed {
		t.Fatalf("visual changed: %+v", got.Visual)
	}
	if len(got.Visual.Params) != 2 || got.Visual.Params[0].Value != 400 {
		t.Fatalf("params changed: %+v", got.Visual.Params)
	}
	if len(got.Visual.Palette) != 2 || got.Visual.Palette[1].Hex != "#6b5b95" {
		t.Fatalf("palette changed: %+v", got.Visual.Palette)
	}
	if got.Audio == nil || got.Audio.Gain != 300 || got.Audio.FadeInMs != 1800 {
		t.Fatalf("audio changed: %+v", got.Audio)
	}
	if got.React == nil || !got.React.Audio || got.React.Pointer {
		t.Fatalf("reactive changed: %+v", got.React)
	}
	if got.Fall.Text != want.Fall.Text || got.Fall.Poster != want.Fall.Poster {
		t.Fatalf("fallback changed: %+v", got.Fall)
	}
	// Canonical: encoding what we decoded reproduces the bytes exactly.
	if !bytes.Equal(doc.Encode(), back.Encode()) {
		t.Fatal("re-encoding a decoded document produced different bytes")
	}
}

// A post without atmosphere must encode exactly as it did before this field
// existed, or every previously signed document changes shape.
func TestNoAtmosphereChangesNothing(t *testing.T) {
	doc := docWith(nil)
	enc := doc.Encode()
	back, err := publication.Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if back.Atmosphere != nil {
		t.Fatal("an absent atmosphere decoded as present")
	}
	if !bytes.Equal(enc, back.Encode()) {
		t.Fatal("round trip changed the bytes of an ordinary post")
	}
}

func TestAtmosphereBounds(t *testing.T) {
	ok := func(string) bool { return true }
	cases := []struct {
		name string
		mut  func(*publication.Atmosphere)
		want string
	}{
		{"scene without a version", func(a *publication.Atmosphere) {
			a.Visual.Scene = "organic-field"
		}, "name@version"},
		{"scene version zero", func(a *publication.Atmosphere) {
			a.Visual.Scene = "organic-field@0"
		}, "name@version"},
		{"param over permille", func(a *publication.Atmosphere) {
			a.Visual.Params[0].Value = 1001
		}, "at most 1000"},
		{"param name with a capital", func(a *publication.Atmosphere) {
			a.Visual.Params[0].Name = "Density"
		}, "not allowed"},
		{"duplicate param", func(a *publication.Atmosphere) {
			a.Visual.Params[1].Name = a.Visual.Params[0].Name
		}, "appears twice"},
		{"too many params", func(a *publication.Atmosphere) {
			a.Visual.Params = nil
			for i := range 17 {
				a.Visual.Params = append(a.Visual.Params,
					publication.Param{Name: string(rune('a'+i)) + "x", Value: 1})
			}
		}, "at most 16"},
		{"too many palette tokens", func(a *publication.Atmosphere) {
			a.Visual.Palette = make([]publication.PaletteToken, 5)
			for i := range a.Visual.Palette {
				a.Visual.Palette[i] = publication.PaletteToken{Name: "c", Hex: "#000000"}
			}
		}, "at most 4"},
		{"uppercase hex", func(a *publication.Atmosphere) {
			a.Visual.Palette[0].Hex = "#4A5D3F"
		}, "#rrggbb"},
		{"three-digit hex", func(a *publication.Atmosphere) {
			a.Visual.Palette[0].Hex = "#4a5"
		}, "#rrggbb"},
		{"audio mode invented", func(a *publication.Atmosphere) {
			a.Audio.Mode = "shuffle"
		}, "loop or once"},
		{"gain over permille", func(a *publication.Atmosphere) {
			a.Audio.Gain = 1001
		}, "at most 1000"},
		{"fade too long", func(a *publication.Atmosphere) {
			a.Audio.FadeInMs = 10001
		}, "fade longer"},
		{"no fallback text", func(a *publication.Atmosphere) {
			a.Fall.Text = ""
		}, "needs fallback text"},
		{"fallback text too long", func(a *publication.Atmosphere) {
			a.Fall.Text = strings.Repeat("x", 201)
		}, "longer than 200"},
		{"lineage hash wrong size", func(a *publication.Atmosphere) {
			a.Derived = &publication.Derived{RecipeHash: []byte{1, 2, 3}}
		}, "want 32"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := sampleAtmosphere()
			c.mut(a)
			err := a.Validate(ok)
			if err == nil {
				t.Fatalf("accepted %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error should mention %q: %v", c.want, err)
			}
		})
	}
	// And the untouched sample passes, or the table above proves nothing.
	if err := sampleAtmosphere().Validate(ok); err != nil {
		t.Fatalf("the valid sample was rejected: %v", err)
	}
}

// An atmosphere asset that was never uploaded into this space would render for
// its author and be blank for everybody else — worse than a refusal.
func TestAtmosphereAssetsMustResolve(t *testing.T) {
	none := func(string) bool { return false }
	a := sampleAtmosphere()
	if err := a.Validate(none); err == nil {
		t.Fatal("an unresolvable audio asset was accepted")
	}
	a2 := sampleAtmosphere()
	a2.Audio = nil
	if err := a2.Validate(none); err == nil {
		t.Fatal("an unresolvable poster was accepted")
	}
	// Both must be ENUMERATED too, or the custody gate cannot see them. The
	// one walk is the only place that answers this — there is deliberately
	// no second list of atmosphere assets to fall out of step with it.
	doc := &publication.Document{Kind: "post", Title: "t", Visibility: "space"}
	doc.Atmosphere = sampleAtmosphere()
	live := doc.LiveAssetIDs()
	if live[hexAsset64] != "atmosphere_audio" || live[hexAsset32] != "atmosphere_poster" {
		t.Fatalf("expected the audio AND the poster in the asset graph, got %v", live)
	}
}

// Lineage points at the visual recipe alone: two people who started from the
// same scene and seed inherited the same thing whether or not they also picked
// the same background track.
func TestRecipeHashCoversTheFormNotTheSoundtrack(t *testing.T) {
	a := sampleAtmosphere()
	b := sampleAtmosphere()
	b.Audio.Asset = hexAsset32
	b.Fall.Text = "different words entirely"
	if !bytes.Equal(a.RecipeHash(), b.RecipeHash()) {
		t.Fatal("changing the audio or the fallback changed the lineage hash")
	}
	c := sampleAtmosphere()
	c.Visual.Seed++
	if bytes.Equal(a.RecipeHash(), c.RecipeHash()) {
		t.Fatal("a different seed produced the same lineage hash")
	}
	d := sampleAtmosphere()
	d.Visual.Params[0].Value = 401
	if bytes.Equal(a.RecipeHash(), d.RecipeHash()) {
		t.Fatal("a different parameter produced the same lineage hash")
	}
	if n := len(a.RecipeHash()); n != 32 {
		t.Fatalf("recipe hash is %d bytes", n)
	}
}

// ---- RawExtra: the forward-compatibility hole this gate closes ----

// withFutureKey appends a document-level key this build cannot know.
func withFutureKey(t *testing.T, doc *publication.Document, key uint64, text string) []byte {
	t.Helper()
	enc := doc.Encode()
	d := codec.NewDecoder(enc)
	mr, err := d.ReadMapHeader()
	if err != nil {
		t.Fatal(err)
	}
	n := mr.Len()
	// Re-emit the same map with one more entry: everything after the header
	// is already canonical, so only the header count changes.
	out := codec.AppendMap(nil, n+1)
	out = append(out, enc[len(codec.AppendMap(nil, n)):]...)
	out = codec.AppendUint(out, key)
	out = codec.AppendText(out, text)
	return out
}

// Before this, an editor built on an older client silently deleted whatever a
// newer one had written — including, once it exists, somebody's atmosphere.
func TestUnknownDocumentKeysSurviveAnEditRoundTrip(t *testing.T) {
	doc := docWith(sampleAtmosphere())
	future := withFutureKey(t, doc, 99, "written by a newer client")

	back, err := publication.Decode(future)
	if err != nil {
		t.Fatalf("an unknown key made the document unreadable: %v", err)
	}
	if len(back.RawExtra) != 1 || back.RawExtra[0].Key != 99 {
		t.Fatalf("the unknown key was not retained: %+v", back.RawExtra)
	}
	// An edit: change the title, as an older client would.
	back.Title = "Edited by an older client"
	again, err := publication.Decode(back.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.RawExtra) != 1 || again.RawExtra[0].Key != 99 {
		t.Fatal("editing the document deleted the newer client's field")
	}
	if !bytes.Equal(again.RawExtra[0].Raw, back.RawExtra[0].Raw) {
		t.Fatal("the retained bytes changed")
	}
	if again.Title != "Edited by an older client" {
		t.Fatal("the edit itself was lost")
	}
}

// The three encoding invariants, each of which would produce a document that
// fails to decode somewhere else.
func TestRawExtraInvariants(t *testing.T) {
	base := docWith(nil)

	t.Run("a key we know is never written twice", func(t *testing.T) {
		doc := docWith(sampleAtmosphere())
		// A build that did not know key 12 would have retained it here.
		// Now that we do know it, it must come from the typed field only.
		doc.RawExtra = []publication.Extra{{Key: 12, Raw: []byte{0x01}}}
		back, err := publication.Decode(doc.Encode())
		if err != nil {
			t.Fatalf("document became unreadable: %v", err)
		}
		if len(back.RawExtra) != 0 {
			t.Fatalf("a now-known key was written from RawExtra too: %+v", back.RawExtra)
		}
		if back.Atmosphere == nil || back.Atmosphere.Visual.Scene != "organic-field@1" {
			t.Fatal("the typed atmosphere did not survive")
		}
	})

	t.Run("keys stay strictly ascending", func(t *testing.T) {
		doc := *base
		doc.RawExtra = []publication.Extra{
			{Key: 50, Raw: codec.AppendUint(nil, 1)},
			{Key: 20, Raw: codec.AppendUint(nil, 2)}, // out of order
			{Key: 70, Raw: codec.AppendUint(nil, 3)},
		}
		// The codec rejects a non-ascending map on read, so a successful
		// decode IS the assertion.
		back, err := publication.Decode(doc.Encode())
		if err != nil {
			t.Fatalf("encoding produced a non-canonical map: %v", err)
		}
		var keys []uint64
		for _, e := range back.RawExtra {
			keys = append(keys, e.Key)
		}
		// 20 came after 50 in the input and is dropped rather than reordered:
		// silently reordering somebody's bytes is a worse answer than not
		// carrying a field that was already malformed.
		for i := 1; i < len(keys); i++ {
			if keys[i] <= keys[i-1] {
				t.Fatalf("keys not ascending: %v", keys)
			}
		}
	})

	t.Run("the passenger is bounded", func(t *testing.T) {
		doc := *base
		big := codec.AppendBytes(nil, make([]byte, publication.MaxRawExtraBytes+1))
		doc.RawExtra = []publication.Extra{{Key: 40, Raw: big}}
		back, err := publication.Decode(doc.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if len(back.RawExtra) != 0 {
			t.Fatal("an over-budget unknown field was carried anyway")
		}
	})
}
