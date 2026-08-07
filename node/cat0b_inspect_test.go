// Looking never subscribes (CAT-0b).
//
// CAT-0a proved a hierarchy of catalogs is expressible; it walked it by
// OPENING each level, which left a durable reader replica behind for every
// place a person merely glanced at. These tests prove the same walk with
// nothing persisted, and that the answer says what the space declares
// itself to be.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// shareLinkOf is a plain share link for a space this runtime owns.
func shareLinkOf(t *testing.T, rt *Runtime, tid id.TerminalID) string {
	t.Helper()
	l, err := rt.ComposePublicLink(tid, nil)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// directoryFixture: alice publishes a directory listing one child directory
// and one ordinary space, and pushes every projection to a live relay. bob
// is a stranger who holds only the root's link.
func directoryFixture(t *testing.T) (alice, bob *Runtime, root id.TerminalID, rootLink string, done func()) {
	t.Helper()
	alice = openRuntime(t, t.TempDir(), "alice")
	bob = openRuntime(t, t.TempDir(), "bob")
	srv, _ := setUpRelay(t, alice)

	newDir := func(title string) id.TerminalID {
		tid, err := alice.CreateSpaceWithOptions(title, CreateOptions{
			Policy: terminals.SpacePolicy{
				Visibility: terminals.VisibilityPublic,
				Publish:    terminals.PublishCurated,
				Kind:       terminals.SpaceKindDirectory,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return tid
	}
	// A card's link block carries the BEARER form: the validator refuses a
	// bare base64 there, and inspect strips the scheme back off.
	card := func(tid id.TerminalID) string { return "qs:" + shareLinkOf(t, alice, tid) }

	root = newDir("quite.space")
	music := newDir("Music")
	ordinary := newPublicSpace(t, alice, "Experimental music")
	publishPost(t, alice, ordinary, "a first listen", "field recordings")

	addCard(t, alice, root, "Music", card(music))
	addCard(t, alice, root, "Experimental music", card(ordinary))

	for _, tid := range []id.TerminalID{root, music, ordinary} {
		if err := alice.publishPublicProjection(alice.GetSettings().Relay, tid); err != nil {
			t.Fatal(err)
		}
	}
	return alice, bob, root, shareLinkOf(t, alice, root), func() { alice.Close(); bob.Close(); srv.Close() }
}

// THE HEADLINE. Bob walks a directory and his node is untouched — the same
// assertion block PS-3 makes about reading one post, now about browsing.
func TestInspectingADirectoryListsItsCardsAndPersistsNothing(t *testing.T) {
	_, bob, root, link, done := directoryFixture(t)
	defer done()

	in, err := bob.InspectPublicSpace(link, false)
	if err != nil {
		t.Fatal(err)
	}
	if in.State != InspectResolved {
		t.Fatalf("the directory did not resolve: %s (%s)", in.State, in.Reason)
	}
	if in.Space != root {
		t.Fatal("the inspection did not carry the space id it verified")
	}
	if in.Kind != terminals.SpaceKindDirectory {
		t.Fatalf("the signed declaration did not reach a stranger: %q", in.Kind)
	}
	if in.Title != "quite.space" {
		t.Fatalf("the space's own name was lost: %q", in.Title)
	}
	if len(in.Cards) != 2 || in.CardsTotal != 2 || in.Truncated {
		t.Fatalf("the listing is wrong: %d cards, total %d, truncated %v",
			len(in.Cards), in.CardsTotal, in.Truncated)
	}
	for _, c := range in.Cards {
		if c.Kind != "space" || c.Link == "" {
			t.Fatalf("a space-card lost its target: %+v", c)
		}
	}

	// LOOKING NEVER SUBSCRIBES. Not a replica, not a SpaceMeta, not a
	// Navigator entry, not an adopted relay.
	bob.mu.Lock()
	held := len(bob.spaces)
	meta := len(bob.ks.Spaces)
	nav := len(bob.ks.Navigator.Pins) + len(bob.ks.Navigator.Catalogs) +
		len(bob.ks.Navigator.Recent)
	bob.mu.Unlock()
	if held != 0 || meta != 0 {
		t.Fatalf("browsing created durable state: %d replicas, %d metas", held, meta)
	}
	if nav != 0 {
		t.Fatal("browsing wrote into the Navigator")
	}
	if got := bob.GetSettings().Relay; got != "" {
		t.Fatalf("browsing adopted a relay: %q", got)
	}
}

// A space-only reference — no document id — is what a directory link is.
// The post preview refuses exactly this, which is what kept the machinery
// from serving a space at all.
func TestInspectingAcceptsASpaceOnlyReference(t *testing.T) {
	_, bob, _, link, done := directoryFixture(t)
	defer done()

	if _, err := bob.PreviewPublicPublication(link); err == nil {
		t.Fatal("the post preview accepted a reference that names no post")
	}
	if _, err := bob.InspectPublicSpace(link, false); err != nil {
		t.Fatalf("inspect refused a space link: %v", err)
	}
}

// "This is not a directory" is an ANSWER, not a failure — settling that
// question is what inspect is for.
func TestInspectingSaysWhenTheTargetIsNotADirectory(t *testing.T) {
	alice, bob, _, _, done := directoryFixture(t)
	defer done()

	ordinary := newPublicSpace(t, alice, "just a room")
	publishPost(t, alice, ordinary, "hello", "")
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, ordinary); err != nil {
		t.Fatal(err)
	}
	in, err := bob.InspectPublicSpace(shareLinkOf(t, alice, ordinary), false)
	if err != nil {
		t.Fatalf("an ordinary space was an error rather than an answer: %v", err)
	}
	if in.State != InspectResolved {
		t.Fatalf("an ordinary space did not resolve: %s", in.State)
	}
	if in.Kind != terminals.SpaceKindOrdinary {
		t.Fatalf("an ordinary space claimed a purpose: %q", in.Kind)
	}
	if len(in.Cards) != 1 || in.Cards[0].Kind != "article" {
		t.Fatalf("its own posts did not come back: %+v", in.Cards)
	}
}

// One session, two questions. Inspecting a directory and then reading a
// post inside it must not dial twice.
func TestAnInspectAndAPostPreviewShareOneSession(t *testing.T) {
	alice, bob, _, _, done := directoryFixture(t)
	defer done()

	space := newPublicSpace(t, alice, "a space with a post")
	doc := publishPost(t, alice, space, "the post", "")
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, space); err != nil {
		t.Fatal(err)
	}
	spaceLink := shareLinkOf(t, alice, space)
	postLink, err := alice.ComposePublicLink(space, &doc)
	if err != nil {
		t.Fatal(err)
	}

	in, err := bob.InspectPublicSpace(spaceLink, false)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := bob.PreviewPublicPublication(postLink)
	if err != nil {
		t.Fatal(err)
	}
	if pv.PreviewID != in.PreviewID {
		t.Fatalf("reading a post opened a second session: %s vs %s",
			pv.PreviewID, in.PreviewID)
	}
}

func TestClosingAnInspectEndsItsSession(t *testing.T) {
	_, bob, _, link, done := directoryFixture(t)
	defer done()

	in, err := bob.InspectPublicSpace(link, false)
	if err != nil {
		t.Fatal(err)
	}
	sess := bob.previews.get(in.PreviewID)
	if sess == nil {
		t.Fatal("the session was not stored")
	}
	bob.previews.drop(in.PreviewID)
	if bob.previews.get(in.PreviewID) != nil {
		t.Fatal("the session survived its close")
	}
	if sess.fetcher != nil && !sess.fetcher.stopped {
		t.Fatal("closing left the fetcher running")
	}
	// Idempotent: closing twice is not an error.
	bob.previews.drop(in.PreviewID)
}

// TWO CEILINGS LIE CLOSE TOGETHER, and which one binds is not fixed.
//
// maxInspectCards is 200. The projection is bounded in BYTES
// (MaxProjectionBytes) and drops its oldest frames to fit, so publishing
// 203 posts delivered 194 on one run and 201 on the next — frame sizes
// vary, and so does where the byte budget lands. Asserting either ceiling
// specifically would be asserting a coincidence.
//
// So this asserts what is true on both sides of that line, which is also
// the only thing a client may rely on: the list never exceeds the bound,
// and `truncated` agrees with the total it is describing. And the total
// itself is what THIS READING carried, never a claim about how many the
// space holds — the projection is the only truth a stranger has.
func TestACardListIsBoundedAndItsCountAgreesWithIt(t *testing.T) {
	alice, bob, _, _, done := directoryFixture(t)
	defer done()

	big := newPublicSpace(t, alice, "a long directory")
	const published = maxInspectCards + 3
	for range published {
		publishPost(t, alice, big, "post", "")
	}
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, big); err != nil {
		t.Fatal(err)
	}
	in, err := bob.InspectPublicSpace(shareLinkOf(t, alice, big), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Cards) > maxInspectCards {
		t.Fatalf("the listing was not bounded: %d cards", len(in.Cards))
	}
	if in.CardsTotal < len(in.Cards) {
		t.Fatalf("the total is smaller than the list it describes: %d < %d",
			in.CardsTotal, len(in.Cards))
	}
	if want := in.CardsTotal > maxInspectCards; in.Truncated != want {
		t.Fatalf("truncated says %v with %d of %d cards",
			in.Truncated, len(in.Cards), in.CardsTotal)
	}
}

// Refresh is what makes a stale list movable: without it a person waits out
// a ten-minute TTL with no way to act.
func TestRefreshingAnInspectActuallyRefetches(t *testing.T) {
	alice, bob, root, link, done := directoryFixture(t)
	defer done()

	first, err := bob.InspectPublicSpace(link, false)
	if err != nil {
		t.Fatal(err)
	}
	addCard(t, alice, root, "Later", "qs:whatever")
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, root); err != nil {
		t.Fatal(err)
	}

	cached, err := bob.InspectPublicSpace(link, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cached.Cards) != len(first.Cards) {
		t.Fatal("a cached read went to the network anyway")
	}
	fresh, err := bob.InspectPublicSpace(link, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Cards) != len(first.Cards)+1 {
		t.Fatalf("refresh did not refetch: %d cards, was %d",
			len(fresh.Cards), len(first.Cards))
	}
}
