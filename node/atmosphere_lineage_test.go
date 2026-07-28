package node

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/publication"
)

func publishWithAtmosphere(t *testing.T, title string, a *publication.Atmosphere) *publication.Document {
	t.Helper()
	// No sound in these: the validator refuses an atmosphere whose audio asset
	// is not published in the same space, which is the custody gate doing its
	// job and has its own tests. Carrying an asset through here would make
	// every lineage assertion depend on an upload succeeding first.
	if a != nil {
		a.Audio = nil
	}
	doc := &publication.Document{
		Kind: "article", Title: title, Visibility: "space", Atmosphere: a,
		Blocks: []publication.Block{{
			ID: "b1", Type: "text",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "body"}),
		}},
	}
	// A stable id per title, so a test can name the document it published
	// without threading the value back out of the helper.
	h := sha256.Sum256([]byte(title))
	copy(doc.DocumentID[:], h[:16])
	return doc
}

// The lineage digest must come off the source this node actually holds, never
// off the request. Otherwise "derived from that post" is a sentence any client
// can write about any post, which is precisely the free-text claim the digest
// exists to replace.
func TestLineageIsComputedFromTheSourceNotTheClaim(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	sid, err := rt.CreateSpace("Lineage")
	if err != nil {
		t.Fatal(err)
	}

	source := sampleAtmosphere()
	source.Derived = nil // the parent has no parent
	src := publishWithAtmosphere(t, "The source", source)
	if _, err := rt.PublishDocument(sid, src, nil); err != nil {
		t.Fatal(err)
	}

	// A derivative that names the source and LIES about the digest.
	child := sampleAtmosphere()
	child.Visual.Seed = 999 // a different picture, same ancestry
	child.Derived = &publication.Derived{
		PublicationID: hex.EncodeToString(src.DocumentID[:]),
		RecipeHash:    []byte(strings.Repeat("\xaa", 32)), // a fabrication
		RevisionHash:  strings.Repeat("00", 32),           // also invented
	}
	doc := publishWithAtmosphere(t, "The derivative", child)
	if _, err := rt.PublishDocument(sid, doc, nil); err != nil {
		t.Fatal(err)
	}

	// What was signed carries the SOURCE's digest, not the claimed one.
	var got *publication.Document
	if err := rt.withSpace(sid, func(st *spaceState) error {
		p, ok := st.space.State.PublicationByID(doc.DocumentID)
		if !ok {
			t.Fatal("the derivative was not published")
		}
		got = p.Document
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := source.RecipeHash()
	if d := got.Atmosphere.Derived; string(d.RecipeHash) != string(want) {
		t.Fatalf("the fabricated digest survived:\n got %x\nwant %x", d.RecipeHash, want)
	}
	if got.Atmosphere.Derived.RevisionHash == strings.Repeat("00", 32) {
		t.Fatal("the fabricated revision hash survived")
	}
	// And the digest genuinely identifies the parent's recipe rather than the
	// child's — this is what makes it worth recording at all.
	if string(want) == string(got.Atmosphere.RecipeHash()) {
		t.Fatal("the child and its source hash the same; the test proves nothing")
	}
}

// A pointer to something this space does not hold is refused, rather than
// published as a claim nobody can ever check.
func TestDerivingFromAPostThatIsNotHereIsRefused(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	sid, err := rt.CreateSpace("Lineage")
	if err != nil {
		t.Fatal(err)
	}
	a := sampleAtmosphere()
	a.Derived = &publication.Derived{PublicationID: strings.Repeat("99", 16)}
	doc := publishWithAtmosphere(t, "Orphan", a)
	_, err = rt.PublishDocument(sid, doc, nil)
	if err == nil {
		t.Fatal("a derivative of nothing was published")
	}
	if !strings.Contains(err.Error(), "not in this space") {
		t.Fatalf("the refusal should say what is missing: %v", err)
	}
}

// Anonymous inheritance — a digest with no pointer — names nobody, so there is
// nothing to verify and it passes through untouched. It still claims only
// "this recipe equals some other recipe", which is true of the bytes or not.
func TestAnonymousInheritanceIsLeftAlone(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	sid, err := rt.CreateSpace("Lineage")
	if err != nil {
		t.Fatal(err)
	}
	digest := []byte(strings.Repeat("\x11", 32))
	a := sampleAtmosphere()
	a.Derived = &publication.Derived{RecipeHash: digest}
	doc := publishWithAtmosphere(t, "Anonymous", a)
	if _, err := rt.PublishDocument(sid, doc, nil); err != nil {
		t.Fatal(err)
	}
	if string(doc.Atmosphere.Derived.RecipeHash) != string(digest) {
		t.Fatal("an anonymous digest was rewritten")
	}
	if doc.Atmosphere.Derived.PublicationID != "" {
		t.Fatal("a pointer was invented for an anonymous derivation")
	}
}
