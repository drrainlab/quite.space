// Nested catalogs cost nothing (CAT-0a).
//
// A catalog is already an ordinary public broadcast space whose posts are
// publications with Kind "space", each carrying a "qs:" share link in a
// plain link block. Nothing says the target of that link cannot itself be
// a catalog — so a hierarchy needs no tree protocol, no index and no
// recursive sync. It needs somebody to publish one.
//
// These tests build the shape the client will walk, on catalogs they make
// themselves, so nothing here waits on an official catalog existing. What
// they prove is the DATA: that descent, a cycle, an unresolvable target and
// a mislabelled entry are all expressible and all distinguishable. The
// breadcrumb, the cycle refusal and "add to Navigator" are client
// behaviour and are verified in the browser, not here.
package node

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/terminals"
)

// newCatalog opens a public broadcast space — which is all a catalog is.
func newCatalog(t *testing.T, rt *Runtime, title string) id.TerminalID {
	t.Helper()
	tid, err := rt.CreateSpaceWithOptions(title, CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tid
}

// addCard publishes one space-card. tags carries the author's CLAIMS about
// the target — including, optionally, that it is itself a catalog.
func addCard(t *testing.T, rt *Runtime, catalog id.TerminalID, title, link string, tags ...string) {
	t.Helper()
	var docID [16]byte
	if _, err := rand.Read(docID[:]); err != nil {
		t.Fatal(err)
	}
	doc := &publication.Document{
		DocumentID: docID,
		Kind:       "space", Title: title, Tags: tags,
		Visibility: "public-intent",
		Blocks: []publication.Block{{
			ID: "l1", Type: "link",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: link}),
		}},
	}
	if _, err := rt.PublishDocument(catalog, doc, nil); err != nil {
		t.Fatalf("publishing %q: %v", title, err)
	}
}

// cardsOf reads a catalog the way the client does: every publication of
// kind "space", with its first link block's text as the target.
func cardsOf(t *testing.T, rt *Runtime, catalog id.TerminalID) []struct {
	Title, Link string
	Tags        []string
} {
	t.Helper()
	var out []struct {
		Title, Link string
		Tags        []string
	}
	err := rt.withSpace(catalog, func(st *spaceState) error {
		for _, p := range st.space.State.Publications() {
			if p.Document == nil || p.Document.Kind != "space" {
				continue
			}
			link := ""
			for _, b := range p.Document.Blocks {
				if b.Type != "link" {
					continue
				}
				if tp, err := publication.ParseTextProps(b.RawProps); err == nil {
					link = tp.Text
					break
				}
			}
			out = append(out, struct {
				Title, Link string
				Tags        []string
			}{p.Document.Title, link, p.Document.Tags})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// targetOf reads which space a card points at. The share link is base64 —
// which is exactly what the client's own status check got wrong until this
// test looked at a real one.
func targetOf(t *testing.T, link string) id.TerminalID {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "qs:"))
	if err != nil {
		return id.TerminalID{}
	}
	i := strings.Index(string(raw), "space:")
	if i < 0 {
		return id.TerminalID{}
	}
	tid, err := id.ParseTerminalID(strings.TrimSpace(string(raw)[i+len("space:"):]))
	if err != nil {
		return id.TerminalID{}
	}
	return tid
}

func linkOf(t *testing.T, rt *Runtime, tid id.TerminalID) string {
	t.Helper()
	l, err := rt.ComposePublicLink(tid, nil)
	if err != nil {
		t.Fatal(err)
	}
	return "qs:" + l
}

// The headline: a reader who knows only the root catalog's link can walk
// down into a catalog it names, and find a space there.
func TestACatalogCanPointAtAnotherCatalog(t *testing.T) {
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	srv, addr := setUpRelay(t, owner, reader)
	defer srv.Close()

	A := newCatalog(t, owner, "Catalog A")
	B := newCatalog(t, owner, "Catalog B")
	X := newCatalog(t, owner, "Space X")
	Y := newCatalog(t, owner, "Space Y")

	addCard(t, owner, A, "Space X", linkOf(t, owner, X))
	addCard(t, owner, A, "Catalog B", linkOf(t, owner, B), "catalog")
	addCard(t, owner, B, "Space Y", linkOf(t, owner, Y))
	// And back up, which is the cycle the client must refuse to descend.
	addCard(t, owner, B, "Catalog A", linkOf(t, owner, A), "catalog")

	for _, tid := range []id.TerminalID{A, B} {
		if err := owner.publishPublicProjection(addr, tid); err != nil {
			t.Fatal(err)
		}
	}

	// The reader arrives with one link and nothing else.
	if err := reader.OpenPublicSpace(A, addr); err != nil {
		t.Fatal(err)
	}
	top := cardsOf(t, reader, A)
	if len(top) != 2 {
		t.Fatalf("the root catalog reads as %d cards", len(top))
	}
	var toB string
	for _, c := range top {
		if c.Title == "Catalog B" {
			toB = c.Link
		}
	}
	if toB == "" || !strings.HasPrefix(toB, "qs:") {
		t.Fatalf("the nested catalog has no share link: %+v", top)
	}

	// Descend. This is exactly OpenPublicSpace on the target — no new
	// protocol, no index, no recursive sync.
	if _, err := reader.OpenPublicLink(strings.TrimPrefix(toB, "qs:")); err != nil {
		t.Fatalf("descending into a nested catalog failed: %v", err)
	}
	next := cardsOf(t, reader, B)
	if len(next) != 2 {
		t.Fatalf("the nested catalog reads as %d cards", len(next))
	}
	var sawY, sawBackToA bool
	for _, c := range next {
		if c.Title == "Space Y" {
			sawY = true
		}
		if targetOf(t, c.Link) == A {
			sawBackToA = true
		}
	}
	if !sawY {
		t.Fatal("the nested catalog's own entry is missing")
	}
	if !sawBackToA {
		t.Fatal("the cycle is not expressible, so the client guard would be untestable")
	}
}

// The tag is the AUTHOR'S CLAIM and nothing more. A card may say "catalog"
// about an ordinary space, and a real catalog may carry no tag at all —
// so the client must show the tag as a claim and confirm only by opening.
func TestTheCatalogTagIsAClaimAndNotAFact(t *testing.T) {
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	srv, addr := setUpRelay(t, owner)
	defer srv.Close()

	root := newCatalog(t, owner, "root")
	plain := newCatalog(t, owner, "an ordinary space with no cards in it")
	real := newCatalog(t, owner, "a real catalog that says nothing about itself")
	leaf := newCatalog(t, owner, "leaf")
	addCard(t, owner, real, "leaf", linkOf(t, owner, leaf))

	// A lie: an ordinary space tagged as a catalog.
	addCard(t, owner, root, "not really a catalog", linkOf(t, owner, plain), "catalog")
	// And the reverse: a real catalog carrying no hint.
	addCard(t, owner, root, "unmarked", linkOf(t, owner, real))
	_ = addr

	cards := cardsOf(t, owner, root)
	var lying, unmarked struct {
		Title, Link string
		Tags        []string
	}
	for _, c := range cards {
		if c.Title == "not really a catalog" {
			lying = c
		}
		if c.Title == "unmarked" {
			unmarked = c
		}
	}
	if len(lying.Tags) == 0 || lying.Tags[0] != "catalog" {
		t.Fatal("the fixture does not carry the false claim")
	}
	if len(unmarked.Tags) != 0 {
		t.Fatal("the fixture's real catalog is not unmarked")
	}
	// Opening the liar's target yields a space with no cards: the claim is
	// simply wrong, and nothing but opening it could have told us.
	if n := len(cardsOf(t, owner, plain)); n != 0 {
		t.Fatalf("the tagged-but-ordinary space holds %d cards", n)
	}
	// And the unmarked one really is a catalog.
	if n := len(cardsOf(t, owner, real)); n != 1 {
		t.Fatalf("the unmarked catalog holds %d cards", n)
	}
}

// A card whose target this node cannot resolve is still a card. It must
// not break the level it sits in — the person can see it and decide.
func TestACardWithAnUnreachableTargetStillLists(t *testing.T) {
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	srv, addr := setUpRelay(t, owner)
	defer srv.Close()
	_ = addr

	root := newCatalog(t, owner, "root")
	real := newCatalog(t, owner, "somewhere real")
	addCard(t, owner, root, "somewhere real", linkOf(t, owner, real))
	// A link nobody can open: well-formed, points at nothing this node has.
	addCard(t, owner, root, "a space that is not there",
		"qs:relay.example:9000/space:"+strings.Repeat("ab", 32))

	cards := cardsOf(t, owner, root)
	if len(cards) != 2 {
		t.Fatalf("an unreachable card cost the level its other rows: %d", len(cards))
	}
	for _, c := range cards {
		if c.Link == "" {
			t.Fatalf("a card lost its target: %+v", c)
		}
	}
}
