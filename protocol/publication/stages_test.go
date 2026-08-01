// AM-7: an atmosphere that is an image sequence rather than a scene.
package publication_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/publication"
)

const (
	stageImage1 = "1111111111111111111111111111111111111111111111111111111111111111"
	stageImage2 = "2222222222222222222222222222222222222222222222222222222222222222"
	stageImage3 = "3333333333333333333333333333333333333333333333333333333333333333"
	stageSound  = "4444444444444444444444444444444444444444444444444444444444444444"
)

// sampleStages is a three-step story over the blocks of storyDoc.
func sampleStages() *publication.Atmosphere {
	return &publication.Atmosphere{
		Visual: publication.Visual{
			Stages: []publication.Stage{
				{Anchor: "b1", Image: stageImage1},
				{Anchor: "b2", Image: stageImage2,
					Transition: publication.TransitionFade, DurationMs: 900},
				{Anchor: "b4", Image: stageImage3, Transition: publication.TransitionCut},
			},
			Palette: []publication.PaletteToken{{Name: "moss", Hex: "#4a5d3f"}},
		},
		Fall: publication.Fallback{Text: "Three photographs, in order."},
	}
}

// storyDoc has a nested block (b4 inside b3) so document order is proved to be
// the reading order, not merely the top level.
func storyDoc(a *publication.Atmosphere) *publication.Document {
	doc := &publication.Document{
		DocumentID: [16]byte{7}, Kind: "article", Title: "Three photographs",
		Visibility: "space", Atmosphere: a,
	}
	doc.Blocks = []publication.Block{
		{ID: "b1", Type: "heading",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "One"})},
		{ID: "b2", Type: "text",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "Two"})},
		{ID: "b3", Type: "section",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "Three"}),
			Children: []publication.Block{
				{ID: "b4", Type: "text",
					RawProps: publication.EncodeTextProps(publication.TextProps{Text: "Four"})},
			}},
	}
	return doc
}

func allAssets(string) bool { return true }

func TestStagesRoundTrip(t *testing.T) {
	doc := storyDoc(sampleStages())
	back, err := publication.Decode(doc.Encode())
	if err != nil {
		t.Fatal(err)
	}
	got := back.Atmosphere.Visual.Stages
	if len(got) != 3 {
		t.Fatalf("expected three stages, got %d", len(got))
	}
	if got[1].Anchor != "b2" || got[1].Image != stageImage2 ||
		got[1].Transition != publication.TransitionFade || got[1].DurationMs != 900 {
		t.Fatalf("stage 2 did not survive: %+v", got[1])
	}
	if back.Atmosphere.Visual.Scene != "" || back.Atmosphere.Visual.Seed != 0 {
		t.Fatal("a sequence grew a scene on the way through the wire")
	}
	if err := publication.Validate(back, allAssets); err != nil {
		t.Fatalf("the round-tripped sequence was rejected: %v", err)
	}
}

// A post has one background. Two sources for it would leave every renderer
// free to decide differently which one wins.
func TestSceneAndStagesAreAlternatives(t *testing.T) {
	both := sampleStages()
	both.Visual.Scene = "organic-field@1"
	if err := publication.Validate(storyDoc(both), allAssets); err == nil {
		t.Fatal("a scene AND a sequence were accepted together")
	}

	neither := sampleStages()
	neither.Visual.Stages = nil
	if err := publication.Validate(storyDoc(neither), allAssets); err == nil {
		t.Fatal("an atmosphere with no scene and no stages was accepted")
	}

	// A seed steers a scene. With no scene it is a number a later renderer
	// would feel free to invent a use for.
	seeded := sampleStages()
	seeded.Visual.Seed = 42
	if err := publication.Validate(storyDoc(seeded), allAssets); err == nil {
		t.Fatal("a sequence with a seed was accepted")
	}
	parammed := sampleStages()
	parammed.Visual.Params = []publication.Param{{Name: "density", Value: 400}}
	if err := publication.Validate(storyDoc(parammed), allAssets); err == nil {
		t.Fatal("a sequence with scene params was accepted")
	}
}

// An anchor is a block id in THIS document. Anything else is a stage that
// never fires, in a post whose author believed it would.
func TestStageAnchorsMustExistAndRunInOrder(t *testing.T) {
	dangling := sampleStages()
	dangling.Visual.Stages[1].Anchor = "b99"
	err := publication.Validate(storyDoc(dangling), allAssets)
	if err == nil || !strings.Contains(err.Error(), "not in this document") {
		t.Fatalf("a dangling anchor was accepted: %v", err)
	}

	// b4 is nested inside b3 and comes after b2 in reading order, so the
	// sample is in order; reversing two of them is not.
	backwards := sampleStages()
	backwards.Visual.Stages[1], backwards.Visual.Stages[2] =
		backwards.Visual.Stages[2], backwards.Visual.Stages[1]
	err = publication.Validate(storyDoc(backwards), allAssets)
	if err == nil || !strings.Contains(err.Error(), "out of document order") {
		t.Fatalf("stages out of document order were accepted: %v", err)
	}

	twice := sampleStages()
	twice.Visual.Stages[2].Anchor = "b2"
	if err := publication.Validate(storyDoc(twice), allAssets); err == nil {
		t.Fatal("two stages on one block were accepted")
	}
}

// The decoder never refuses what the authoring gate refuses (ADR-014): a
// dangling anchor must travel and degrade, exactly like an unknown block type.
func TestTheDecoderStillAcceptsADanglingAnchor(t *testing.T) {
	dangling := sampleStages()
	dangling.Visual.Stages[0].Anchor = "gone"
	back, err := publication.Decode(storyDoc(dangling).Encode())
	if err != nil {
		t.Fatalf("transport refused what only authoring may refuse: %v", err)
	}
	if back.Atmosphere.Visual.Stages[0].Anchor != "gone" {
		t.Fatal("the anchor was rewritten in transit")
	}
}

// A stage image not in the space renders for its author and is blank for
// everybody else — which is the whole reason there is one asset walk.
func TestStageAssetsAreEnumeratedAndChecked(t *testing.T) {
	doc := storyDoc(sampleStages())
	doc.Atmosphere.Visual.Stages[0].Audio = stageSound

	live := doc.LiveAssetIDs()
	for _, id := range []string{stageImage1, stageImage2, stageImage3} {
		if live[id] != "atmosphere_stage" {
			t.Fatalf("stage image %s… is not in the asset graph: %v", id[:8], live)
		}
	}
	// Carried even though nothing plays it yet — otherwise it would be
	// unfetchable on the day the renderer lands.
	if live[stageSound] != "atmosphere_stage_audio" {
		t.Fatalf("stage audio is not in the asset graph: %v", live)
	}

	missing := func(id string) bool { return id != stageImage2 }
	if err := publication.Validate(doc, missing); err == nil {
		t.Fatal("a stage image absent from the space was accepted")
	}
}

func TestStageBounds(t *testing.T) {
	t.Run("too many stages", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		var many []publication.Stage
		for range publication.MaxAtmosphereStages + 1 {
			many = append(many, publication.Stage{Anchor: "b1", Image: stageImage1})
		}
		doc.Atmosphere.Visual.Stages = many
		if err := publication.Validate(doc, allAssets); err == nil {
			t.Fatal("an unbounded sequence was accepted")
		}
		// And the DECODER refuses the count before allocating anything.
		if _, err := publication.Decode(doc.Encode()); err == nil {
			t.Fatal("the decoder allocated an over-long stage array")
		}
	})

	t.Run("unknown transition", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		doc.Atmosphere.Visual.Stages[0].Transition = "dissolve-with-shader"
		if err := publication.Validate(doc, allAssets); err == nil {
			t.Fatal("an unlisted transition was accepted")
		}
	})

	t.Run("transition longer than a fade may be", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		doc.Atmosphere.Visual.Stages[0].DurationMs = publication.MaxFadeMs + 1
		if err := publication.Validate(doc, allAssets); err == nil {
			t.Fatal("an unbounded transition was accepted")
		}
	})

	t.Run("a stage needs an image", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		doc.Atmosphere.Visual.Stages[0].Image = ""
		if err := publication.Validate(doc, allAssets); err == nil {
			t.Fatal("a stage with nothing to show was accepted")
		}
	})

	t.Run("an oversized anchor could not be a block id", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		doc.Atmosphere.Visual.Stages[0].Anchor = strings.Repeat("x", 65)
		if err := publication.Validate(doc, allAssets); err == nil {
			t.Fatal("an anchor no block could carry was accepted")
		}
	})
}

// Inheriting somebody's story means inheriting the images and their order.
func TestRecipeHashCoversTheSequence(t *testing.T) {
	a := sampleStages()
	b := sampleStages()
	b.Fall.Text = "different words entirely"
	if !bytes.Equal(a.RecipeHash(), b.RecipeHash()) {
		t.Fatal("the fallback text changed the lineage hash")
	}

	swapped := sampleStages()
	swapped.Visual.Stages[0].Image = stageImage3
	if bytes.Equal(a.RecipeHash(), swapped.RecipeHash()) {
		t.Fatal("a different image produced the same lineage hash")
	}

	reordered := sampleStages()
	reordered.Visual.Stages[0].Image, reordered.Visual.Stages[1].Image =
		reordered.Visual.Stages[1].Image, reordered.Visual.Stages[0].Image
	if bytes.Equal(a.RecipeHash(), reordered.RecipeHash()) {
		t.Fatal("the same images in a different order hashed the same")
	}
}

// A recipe written before stages existed carries no key 5, so its bytes and
// its hash are exactly what they always were.
//
// The literal below is the sample scene recipe's hash, pinned here because
// encodeVisual now has a conditional arity and a new key: if a later edit
// changes what a scene-only recipe encodes to, every derived_from claim ever
// written stops resolving, silently. That is what this number guards, and it
// is why it is a constant rather than a recomputation.
func TestSceneRecipeHashesAreUnchangedByThisGate(t *testing.T) {
	const pinned = "9cd56a3b1597600d02f4aa2762ff14f29c72f26e5ad306f7a1d074ffe2eb1ac6"
	a := sampleAtmosphere()
	if a.Visual.Stages != nil {
		t.Fatal("the scene sample grew stages")
	}
	if got := hex.EncodeToString(a.RecipeHash()); got != pinned {
		t.Fatalf("a scene recipe's lineage hash moved:\n  was %s\n  now %s", pinned, got)
	}
}

// ---- forward compatibility, one level down ----

// withFutureVisualKey produces bytes whose VISUAL map carries a key this
// build cannot know — the shape every future atmosphere extension has.
//
// It goes through the encoder rather than splicing the buffer by hand because
// the encoder emits exactly what a newer client would; what is under test is
// the DECODER's willingness to keep those bytes rather than skip them.
func withFutureVisualKey(t *testing.T, doc *publication.Document, key uint64, text string) []byte {
	t.Helper()
	saved := doc.Atmosphere.Visual
	doc.Atmosphere.Visual.RawExtra = []publication.Extra{
		{Key: key, Raw: codec.AppendText(nil, text)},
	}
	out := doc.Encode()
	doc.Atmosphere.Visual = saved
	return out
}

// The document already retained unknown keys; the maps INSIDE it did not, and
// stages are the first extension nested deeply enough for anyone to notice.
func TestUnknownAtmosphereKeysSurviveAnEditRoundTrip(t *testing.T) {
	t.Run("visual", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		future := withFutureVisualKey(t, doc, 40, "a stage property from the future")

		back, err := publication.Decode(future)
		if err != nil {
			t.Fatalf("an unknown visual key made the document unreadable: %v", err)
		}
		extra := back.Atmosphere.Visual.RawExtra
		if len(extra) != 1 || extra[0].Key != 40 {
			t.Fatalf("the unknown visual key was not retained: %+v", extra)
		}
		back.Title = "Edited by an older client"
		again, err := publication.Decode(back.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if len(again.Atmosphere.Visual.RawExtra) != 1 {
			t.Fatal("editing the post deleted the newer client's visual field")
		}
		if len(again.Atmosphere.Visual.Stages) != 3 {
			t.Fatal("the stages themselves were lost")
		}
	})

	t.Run("atmosphere", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		doc.Atmosphere.RawExtra = []publication.Extra{
			{Key: 20, Raw: codec.AppendUint(nil, 7)},
		}
		back, err := publication.Decode(doc.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if len(back.Atmosphere.RawExtra) != 1 || back.Atmosphere.RawExtra[0].Key != 20 {
			t.Fatalf("the unknown atmosphere key was not retained: %+v",
				back.Atmosphere.RawExtra)
		}
	})

	t.Run("a key we know is never written twice", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		// A build that did not know key 5 would have retained it here. Now
		// that we do, it comes from the typed field only.
		doc.Atmosphere.Visual.RawExtra = []publication.Extra{
			{Key: 5, Raw: codec.AppendUint(nil, 1)},
		}
		back, err := publication.Decode(doc.Encode())
		if err != nil {
			t.Fatalf("the visual map became unreadable: %v", err)
		}
		if len(back.Atmosphere.Visual.RawExtra) != 0 {
			t.Fatalf("a now-known key was written from RawExtra too: %+v",
				back.Atmosphere.Visual.RawExtra)
		}
		if len(back.Atmosphere.Visual.Stages) != 3 {
			t.Fatal("the typed stages did not survive")
		}
	})

	t.Run("the passenger is bounded", func(t *testing.T) {
		doc := storyDoc(sampleStages())
		big := codec.AppendBytes(nil, make([]byte, publication.MaxAtmosphereRawExtraBytes+1))
		doc.Atmosphere.Visual.RawExtra = []publication.Extra{{Key: 40, Raw: big}}
		back, err := publication.Decode(doc.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if len(back.Atmosphere.Visual.RawExtra) != 0 {
			t.Fatal("an over-budget unknown field was carried anyway")
		}
	})
}
