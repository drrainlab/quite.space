// The card builder + the unified share gates (PS-2).
//
// A public post always forwards as a card; the reference inside it is
// optional and implies the source's name; a private source yields the
// quotation alone; an archived post refuses in its own sentence; the
// author is the SIGNER, never Document.DisplayAuthors.
package node

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
)

// publishPost writes one publication and returns its document id.
func publishPost(t *testing.T, rt *Runtime, tid id.TerminalID, title, summary string) [16]byte {
	t.Helper()
	doc := &publication.Document{
		Kind: "article", Title: title, Summary: summary,
		Visibility:     "space",
		DisplayAuthors: []string{"A Made-Up Byline"},
		Blocks: []publication.Block{{
			ID: "b1", Type: "text",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "the body of the piece"}),
		}},
	}
	if _, err := rand.Read(doc.DocumentID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.PublishDocument(tid, doc, nil); err != nil {
		t.Fatal(err)
	}
	return doc.DocumentID
}

func newPublicSpace(t *testing.T, rt *Runtime, title string) id.TerminalID {
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

func TestAPublicPostForwardsAsACardWithAReference(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	s := rt.GetSettings()
	s.Relay = "relay.example:7411"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	src := newPublicSpace(t, rt, "slow technology")
	dest, err := rt.CreateSpace("notes")
	if err != nil {
		t.Fatal(err)
	}
	doc := publishPost(t, rt, src, "Почему локальные сети снова важны", "Короткое описание")

	res, err := rt.SharePost(src, doc, []id.TerminalID{dest}, ShareOptions{NameAuthor: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("the share did not land: %+v", res)
	}

	o, body := originOf(t, rt, dest)
	if o == nil {
		t.Fatal("no provenance on the copy")
	}
	// A reference implies the source's name — hiding the name while
	// carrying the address is theatre.
	if o.SourceLabel != "slow technology" {
		t.Fatalf("the reference did not bring the source's name: %q", o.SourceLabel)
	}
	// The author is the SIGNER, resolved to their chosen name — never the
	// document's free-text byline.
	if o.AuthorLabel != "alice" {
		t.Fatalf("the quotation names %q, not the signer", o.AuthorLabel)
	}
	if strings.Contains(body, "A Made-Up Byline") {
		t.Fatal("DisplayAuthors leaked into the quotation")
	}

	card := cardOf(t, rt, dest)
	if card == nil {
		t.Fatal("a public post did not travel as a card")
	}
	if card.Title != "Почему локальные сети снова важны" {
		t.Fatalf("the card's title changed: %q", card.Title)
	}
	if card.Reference == "" {
		t.Fatal("the card carries no way back")
	}
	relayAddr, tid, d, err := ParsePublicLink(card.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if relayAddr != "relay.example:7411" || tid != src || d == nil || *d != doc {
		t.Fatalf("the reference does not point home: %q %s %v", relayAddr, tid.Hex()[:8], d)
	}
}

func TestTheToggleDropsThePathBackNotTheCard(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	s := rt.GetSettings()
	s.Relay = "relay.example:7411"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	src := newPublicSpace(t, rt, "slow technology")
	dest, _ := rt.CreateSpace("notes")
	doc := publishPost(t, rt, src, "a title", "a summary")

	res, err := rt.SharePost(src, doc, []id.TerminalID{dest},
		ShareOptions{NameAuthor: true, NoReference: true})
	if err != nil || !res[0].OK {
		t.Fatalf("share failed: %v %+v", err, res)
	}
	card := cardOf(t, rt, dest)
	if card == nil {
		t.Fatal("dropping the reference dropped the card with it")
	}
	if card.Reference != "" {
		t.Fatalf("the toggle did not drop the path back: %q", card.Reference)
	}
	// No reference → no implied name; NameSource was not asked for.
	if o, _ := originOf(t, rt, dest); o.SourceLabel != "" {
		t.Fatalf("the source was named without a reference and without being asked: %q", o.SourceLabel)
	}
}

// TestPublicPostWithoutRelayStillCarriesCardWithoutReference — the plan's
// named test: no usable relay degrades the card to a snapshot, never to a
// bare quotation and never to a failure.
func TestPublicPostWithoutRelayStillCarriesCardWithoutReference(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src := newPublicSpace(t, rt, "slow technology")
	dest, _ := rt.CreateSpace("notes")
	doc := publishPost(t, rt, src, "a title", "a summary")

	res, err := rt.SharePost(src, doc, []id.TerminalID{dest}, ShareOptions{NameAuthor: true})
	if err != nil {
		t.Fatalf("a missing relay failed the share: %v", err)
	}
	if !res[0].OK {
		t.Fatalf("a missing relay lost the destination: %+v", res)
	}
	card := cardOf(t, rt, dest)
	if card == nil {
		t.Fatal("a missing relay dropped the card")
	}
	if card.Reference != "" {
		t.Fatalf("a reference appeared with no relay to point at: %q", card.Reference)
	}
}

func TestAPrivatePostYieldsTheQuotationAlone(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	s := rt.GetSettings()
	s.Relay = "relay.example:7411"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	src, err := rt.CreateSpace("just ours") // private by default
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := rt.CreateSpace("notes")
	doc := publishPost(t, rt, src, "a private piece", "not for the catalog")

	res, err := rt.SharePost(src, doc, []id.TerminalID{dest}, ShareOptions{NameAuthor: true})
	if err != nil || !res[0].OK {
		t.Fatalf("share failed: %v %+v", err, res)
	}
	if card := cardOf(t, rt, dest); card != nil {
		t.Fatalf("a private source produced a card: %+v", card)
	}
	if o, _ := originOf(t, rt, dest); o.SourceLabel != "" {
		t.Fatalf("a private source was named unasked: %q", o.SourceLabel)
	}
	// And the quotation itself still landed.
	waitLocalText(t, rt, dest, "a private piece")
}

func TestAWithdrawnPostRefusesInItsOwnSentence(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src := newPublicSpace(t, rt, "slow technology")
	dest, _ := rt.CreateSpace("notes")
	doc := publishPost(t, rt, src, "soon withdrawn", "")
	if _, err := rt.ArchiveDocument(src, doc); err != nil {
		t.Fatal(err)
	}

	_, err := rt.SharePost(src, doc, []id.TerminalID{dest}, ShareOptions{NameAuthor: true})
	if err == nil {
		t.Fatal("a withdrawn post was forwarded")
	}
	if !strings.Contains(err.Error(), "withdrawn by its author") {
		t.Fatalf("the wrong sentence: %v", err)
	}
	// A refusal writes nothing anywhere.
	if got := len(textsOf(t, rt, dest)); got != 0 {
		t.Fatalf("a refusal wrote %d messages", got)
	}
	// And an UNKNOWN post is a different sentence — the two must not sound
	// alike, or "the author took this back" reads as "nothing here".
	_, err = rt.SharePost(src, [16]byte{0xEE}, []id.TerminalID{dest}, ShareOptions{})
	if err == nil || strings.Contains(err.Error(), "withdrawn") {
		t.Fatalf("unknown and withdrawn sound alike: %v", err)
	}
}

func TestAnEditDoesNotMasqueradeAsANewPost(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src := newPublicSpace(t, rt, "slow technology")
	doc := publishPost(t, rt, src, "first title", "first summary")

	var first uint64
	if err := rt.withSpace(src, func(st *spaceState) error {
		pub, _ := st.space.State.PublicationByID(doc)
		first = pub.CreatedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if first == 0 {
		t.Fatal("no CreatedAt on a fresh post")
	}

	// Revise it, then check the clock did not move.
	var d2 publication.Document
	var rev id.EventID
	if err := rt.withSpace(src, func(st *spaceState) error {
		pub, _ := st.space.State.PublicationByID(doc)
		d2, rev = *pub.Document, pub.RevisionEventID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d2.Title = "second title"
	if _, err := rt.PublishDocument(src, &d2, &rev); err != nil {
		t.Fatal(err)
	}
	if err := rt.withSpace(src, func(st *spaceState) error {
		pub, _ := st.space.State.PublicationByID(doc)
		if pub.CreatedAt != first {
			t.Fatalf("an edit moved CreatedAt: %d -> %d", first, pub.CreatedAt)
		}
		if pub.Title != "second title" {
			t.Fatalf("the revision itself did not land: %q", pub.Title)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ---- helpers ----

// cardOf reads the card off the newest message in a space, through the
// runtime's lock like every reader here.
func cardOf(t *testing.T, rt *Runtime, tid id.TerminalID) *cardView {
	t.Helper()
	var out *cardView
	_ = rt.withSpace(tid, func(st *spaceState) error {
		entries := st.space.State.Entries()
		for i := len(entries) - 1; i >= 0; i-- {
			if c := entries[i].Content.Text; c != nil && c.Card != nil {
				out = &cardView{Title: c.Card.Title, Summary: c.Card.Summary,
					Reference: c.Card.Reference}
				return nil
			}
		}
		return nil
	})
	return out
}

type cardView struct{ Title, Summary, Reference string }

func waitLocalText(t *testing.T, rt *Runtime, tid id.TerminalID, want string) {
	t.Helper()
	for _, s := range textsOf(t, rt, tid) {
		if strings.Contains(s, want) {
			return
		}
	}
	t.Fatalf("%q not found in %q", want, textsOf(t, rt, tid))
}

// The composer and the matcher cannot drift: quotedLines drops exactly
// what composeQuote wrote around the quotation — no more, no less.
func TestQuotedLinesMatchesTheComposerNotAPosition(t *testing.T) {
	// A headerless origin: composeQuote writes NO header line, and the
	// old "drop line 0" would have eaten the first line of the body.
	o := &schemas.ShareOrigin{}
	body := composeQuote(o, "first line\nsecond line")
	if got := quotedLines(o, body); got != "first line\nsecond line" {
		t.Fatalf("a headerless quotation lost its first line: %q", got)
	}
	// A body line that LOOKS like a header survives, because matching is
	// by string, not by position or shape.
	o2 := &schemas.ShareOrigin{AuthorLabel: "bob", OriginalAt: 1785400000}
	body2 := composeQuote(o2, "alice · somewhere · 2020-01-01\nreal content")
	if got := quotedLines(o2, body2); !strings.Contains(got, "alice · somewhere") {
		t.Fatalf("a header-looking body line was eaten: %q", got)
	}
	if strings.Contains(quotedLines(o2, body2), "bob · 2026") {
		t.Fatal("the real header leaked into the body")
	}
}

// One ellipsis, not two: the composed text carries the truncation marker
// for old clients, and the structured path must NOT collect it as body —
// the client appends its own from the truncated flag.
func TestATruncatedQuoteCarriesOneEllipsisNotTwo(t *testing.T) {
	o := &schemas.ShareOrigin{AuthorLabel: "bob", Truncated: true}
	body := composeQuote(o, "a long sentence, cut")
	if !strings.Contains(body, "…") {
		t.Fatal("the composed text lost the honest marker")
	}
	if got := quotedLines(o, body); strings.Contains(got, "…") {
		t.Fatalf("the marker leaked into the structured body: %q", got)
	}
}
