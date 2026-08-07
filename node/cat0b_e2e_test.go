// The wave, end to end: a stranger walks a directory and their node is
// untouched until they say otherwise (CAT-0b).
//
// CAT-0a could already express a hierarchy of catalogs. What it could not
// do was let somebody LOOK at one: every glance went through
// OpenPublicLink, which wrote a reader replica, a SpaceMeta, a keystore
// record and an adopted relay, and — because membership in r.spaces IS the
// background-sync registration — signed the person up to keep a folder they
// glanced at in step forever.
//
// So the assertion block this file is built around is not "the walk
// worked". It is: after two levels, a leaf, a post read from that leaf and
// a full restart, bob's node holds NOTHING. Looking never subscribes.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// nothingPersisted is the block PS-3 established for reading one post,
// asked here about browsing a whole hierarchy. Every clause is a separate
// way the old path leaked, and all of them have to hold at once.
func nothingPersisted(t *testing.T, rt *Runtime, when string) {
	t.Helper()
	rt.mu.Lock()
	held := len(rt.spaces)
	metas := len(rt.ks.Spaces)
	nav := len(rt.ks.Navigator.Pins) + len(rt.ks.Navigator.Catalogs) +
		len(rt.ks.Navigator.Recent)
	for _, g := range rt.ks.Navigator.Groups {
		nav += len(g.Children)
	}
	rt.mu.Unlock()
	if held != 0 {
		t.Fatalf("%s: browsing left %d replicas behind", when, held)
	}
	if metas != 0 {
		t.Fatalf("%s: browsing wrote %d space records", when, metas)
	}
	if nav != 0 {
		t.Fatalf("%s: browsing wrote into the Navigator", when)
	}
	if got := rt.GetSettings().Relay; got != "" {
		t.Fatalf("%s: browsing adopted a relay: %q", when, got)
	}
}

func TestBrowsingAHierarchyPersistsNothingUntilSomebodySaysSo(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bobDir := t.TempDir()
	bob := openRuntime(t, bobDir, "bob")
	srv, _ := setUpRelay(t, alice)
	defer srv.Close()

	newDir := func(title string) id.TerminalID {
		t.Helper()
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
	card := func(tid id.TerminalID) string { return "qs:" + shareLinkOf(t, alice, tid) }

	// Alice builds a directory of directories, and one ordinary place at
	// the bottom with something actually written in it.
	root := newDir("quite.space")
	music := newDir("Music")
	news := newDir("News")
	room := newPublicSpace(t, alice, "Experimental music")
	post := publishPost(t, alice, room, "a first listen", "field recordings")

	addCard(t, alice, root, "Music", card(music))
	addCard(t, alice, root, "News", card(news))
	addCard(t, alice, music, "Experimental music", card(room))

	for _, tid := range []id.TerminalID{root, music, news, room} {
		if err := alice.publishPublicProjection(alice.GetSettings().Relay, tid); err != nil {
			t.Fatal(err)
		}
	}

	// ---- bob holds one link and nothing else ----
	rootLink := shareLinkOf(t, alice, root)

	level1, err := bob.InspectPublicSpace(rootLink, false)
	if err != nil {
		t.Fatal(err)
	}
	if level1.Kind != terminals.SpaceKindDirectory {
		t.Fatalf("the root did not declare itself: %q", level1.Kind)
	}
	if len(level1.Cards) != 2 {
		t.Fatalf("the root listed %d entries, wanted 2", len(level1.Cards))
	}
	// The hierarchy is walked by FOLLOWING a card's own target — there is
	// no tree protocol here, only "look at the next space".
	var musicLink string
	for _, c := range level1.Cards {
		if c.Title == "Music" {
			musicLink = c.Link
		}
	}
	if musicLink == "" {
		t.Fatal("the Music entry carried no target")
	}
	bob.previews.drop(level1.PreviewID)

	level2, err := bob.InspectPublicSpace(musicLink, false)
	if err != nil {
		t.Fatal(err)
	}
	if level2.Kind != terminals.SpaceKindDirectory || len(level2.Cards) != 1 {
		t.Fatalf("the second level is wrong: kind %q, %d entries",
			level2.Kind, len(level2.Cards))
	}
	roomLink := level2.Cards[0].Link
	bob.previews.drop(level2.PreviewID)

	// ---- the leaf: an ordinary space, and that is an ANSWER ----
	leaf, err := bob.InspectPublicSpace(roomLink, false)
	if err != nil {
		t.Fatalf("an ordinary space was an error rather than an answer: %v", err)
	}
	if leaf.Kind != terminals.SpaceKindOrdinary {
		t.Fatalf("an ordinary space claimed a purpose: %q", leaf.Kind)
	}
	if len(leaf.Cards) != 1 || leaf.Cards[0].Title != "a first listen" {
		t.Fatalf("the leaf did not carry its own posts: %+v", leaf.Cards)
	}

	// Reading a post from the leaf is served by the SAME session — one
	// dial for "what is this place" and "what does it say".
	postLink, err := alice.ComposePublicLink(room, &post)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := bob.PreviewPublicPublication(postLink)
	if err != nil {
		t.Fatal(err)
	}
	if pv.PreviewID != leaf.PreviewID {
		t.Fatalf("reading a post from a leaf opened a second session: %s vs %s",
			pv.PreviewID, leaf.PreviewID)
	}

	// ---- THE POINT OF THE WAVE ----
	nothingPersisted(t, bob, "after the walk")
	bob.Close()
	bob = openRuntime(t, bobDir, "bob")
	nothingPersisted(t, bob, "after a restart")

	// ---- and only now, on an explicit act ----
	kept, err := bob.FollowPublicSpace(roomLink)
	if err != nil {
		t.Fatal(err)
	}
	if kept != leaf.Space {
		t.Fatal("Add to my spaces kept something other than what was read")
	}
	bob.mu.Lock()
	held := len(bob.spaces)
	bob.mu.Unlock()
	if held != 1 {
		t.Fatalf("keeping one space kept %d", held)
	}
	// The directories bob merely walked through are NOT among them.
	for _, tid := range []id.TerminalID{root, music, news} {
		bob.mu.Lock()
		_, there := bob.spaces[tid]
		bob.mu.Unlock()
		if there {
			t.Fatal("a directory bob only looked at was kept as well")
		}
	}
	bob.Close()
}

// An older build sees a directory as what it also is: an ordinary public
// space whose posts happen to be space-cards. The declaration is additive,
// and a build that cannot read it loses nothing but the word.
func TestAnOlderBuildStillReadsADirectoryAsAnOrdinaryPublicSpace(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	srv, _ := setUpRelay(t, alice)
	defer srv.Close()

	root, err := alice.CreateSpaceWithOptions("quite.space", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
			Kind:       terminals.SpaceKindDirectory,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	room := newPublicSpace(t, alice, "Experimental music")
	addCard(t, alice, root, "Experimental music", "qs:"+shareLinkOf(t, alice, room))
	for _, tid := range []id.TerminalID{root, room} {
		if err := alice.publishPublicProjection(alice.GetSettings().Relay, tid); err != nil {
			t.Fatal(err)
		}
	}

	// Carol stands in for a build that never learned qp.kind: she reads the
	// same signed policy through ParsePolicy with the declaration stripped
	// of meaning, which is what an older ParsePolicy does with a key it has
	// no case for. What must survive is everything else.
	in, err := carol.InspectPublicSpace(shareLinkOf(t, alice, root), false)
	if err != nil {
		t.Fatal(err)
	}
	if in.State != InspectResolved {
		t.Fatalf("the space did not resolve: %s", in.State)
	}
	if in.Title != "quite.space" || in.Publish != terminals.PublishCurated {
		t.Fatalf("the ordinary reading of the space changed: %+v", in)
	}
	if len(in.Cards) != 1 || in.Cards[0].Kind != "space" || in.Cards[0].Link == "" {
		t.Fatalf("the space-card convention broke: %+v", in.Cards)
	}
	// And the entry still opens the way it always did.
	if _, err := carol.InspectPublicSpace(in.Cards[0].Link, false); err != nil {
		t.Fatalf("following an entry the old way broke: %v", err)
	}
}
