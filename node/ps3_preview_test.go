// The transient preview (PS-3): reading a shared post persists nothing,
// and the preview is never a laxer reader than a real replica.
package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// previewFixture: alice publishes a post in a public space and pushes its
// projection to a live relay; bob is a stranger holding only the reference.
func previewFixture(t *testing.T) (alice, bob *Runtime, src id.TerminalID, doc [16]byte, reference string, closeAll func()) {
	t.Helper()
	aliceDir, bobDir := t.TempDir(), t.TempDir()
	alice = openRuntime(t, aliceDir, "alice")
	bob = openRuntime(t, bobDir, "bob")
	// Only alice gets the relay in settings: bob is a stranger whose node
	// has never been configured, which is exactly the case the preview
	// serves — it dials the LINK's relay and adopts nothing.
	srv, addr := setUpRelay(t, alice)

	src = newPublicSpace(t, alice, "slow technology")
	doc = publishPost(t, alice, src, "Почему локальные сети снова важны", "Короткое описание")
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, src); err != nil {
		t.Fatal(err)
	}
	link, err := alice.ComposePublicLink(src, &doc)
	if err != nil {
		t.Fatal(err)
	}
	_ = addr
	return alice, bob, src, doc, link, func() { alice.Close(); bob.Close(); srv.Close() }
}

func TestAPreviewReadsThePostAndPersistsNothing(t *testing.T) {
	_, bob, src, doc, ref, done := previewFixture(t)
	defer done()

	pv, err := bob.PreviewPublicPublication(ref)
	if err != nil {
		t.Fatal(err)
	}
	if pv.State != PreviewResolved {
		t.Fatalf("the post did not resolve: %s (%s)", pv.State, pv.Reason)
	}
	if pv.Pub == nil || pv.Pub.Title != "Почему локальные сети снова важны" {
		t.Fatalf("the wrong post arrived: %+v", pv.Pub)
	}
	if pv.SpaceTitle != "slow technology" {
		t.Fatalf("the space's name was lost: %q", pv.SpaceTitle)
	}
	if pv.Document != doc {
		t.Fatal("the landing hint drifted")
	}

	// NOTHING persisted — not a replica, not a SpaceMeta, not a Navigator
	// entry, not a relay setting.
	bob.mu.Lock()
	_, holds := bob.spaces[src]
	_, hasMeta := bob.ks.Spaces[src]
	nav := len(bob.ks.Navigator.Pins) + len(bob.ks.Navigator.Catalogs) + len(bob.ks.Navigator.Recent)
	bob.mu.Unlock()
	if holds || hasMeta {
		t.Fatal("reading a post created a replica")
	}
	if nav != 0 {
		t.Fatal("reading a post touched the Navigator")
	}
	if bob.GetSettings().Relay != "" {
		t.Fatalf("reading a post adopted a relay: %q", bob.GetSettings().Relay)
	}
}

func TestReadingSurvivesNothingAcrossARestart(t *testing.T) {
	// The plan's headline: bob reads, closes, restarts — and the space is
	// NOWHERE. Then he follows, and only that survives.
	aliceDir, bobDir := t.TempDir(), t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	defer alice.Close()
	bob := openRuntime(t, bobDir, "bob")
	srv, addr := setUpRelay(t, alice)
	defer srv.Close()

	src := newPublicSpace(t, alice, "slow technology")
	doc := publishPost(t, alice, src, "a piece", "about things")
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, src); err != nil {
		t.Fatal(err)
	}
	ref, err := alice.ComposePublicLink(src, &doc)
	if err != nil {
		t.Fatal(err)
	}

	pv, err := bob.PreviewPublicPublication(ref)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read failed: %v %+v", err, pv)
	}
	relayBefore := bob.GetSettings().Relay
	bob.Close()

	bob2 := openRuntime(t, bobDir, "bob")
	bob2.mu.Lock()
	_, holds := bob2.spaces[src]
	_, hasMeta := bob2.ks.Spaces[src]
	bob2.mu.Unlock()
	if holds || hasMeta {
		t.Fatal("a preview survived a restart as a replica")
	}
	if bob2.GetSettings().Relay != relayBefore {
		t.Fatal("a preview changed the relay across a restart")
	}
	// And the session itself is gone — memory only.
	if got := bob2.previews.bySpace(src); got != nil {
		t.Fatal("a preview session survived a restart")
	}

	// Now the explicit act: Follow. Only THIS persists.
	s := bob2.GetSettings()
	s.Relay = addr
	if err := bob2.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := bob2.OpenPublicSpace(src, addr); err != nil {
		t.Fatal(err)
	}
	bob2.Close()
	bob3 := openRuntime(t, bobDir, "bob")
	defer bob3.Close()
	bob3.mu.Lock()
	_, holds = bob3.spaces[src]
	bob3.mu.Unlock()
	if !holds {
		t.Fatal("Follow did not survive a restart")
	}
}

func TestAPreviewOfAMissingPostSaysWhichKindOfMissing(t *testing.T) {
	alice, bob, src, _, _, done := previewFixture(t)
	defer done()

	// A post that does not exist in an ARRIVED projection: the space is
	// there, this post is not — a different sentence from "nothing arrived".
	other, err := alice.ComposePublicLink(src, &[16]byte{0xAB})
	if err != nil {
		t.Fatal(err)
	}
	pv, err := bob.PreviewPublicPublication(other)
	if err != nil {
		t.Fatal(err)
	}
	if pv.State != PreviewMissingDoc {
		t.Fatalf("missing post reported %s", pv.State)
	}
	if !strings.Contains(pv.Reason, "not in it") {
		t.Fatalf("the wrong sentence: %q", pv.Reason)
	}
}

func TestAWithdrawnPostSaysSoAtReadTimeToo(t *testing.T) {
	alice, bob, src, doc, ref, done := previewFixture(t)
	defer done()

	if _, err := alice.ArchiveDocument(src, doc); err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, src); err != nil {
		t.Fatal(err)
	}
	// A fresh session sees the archive (the fixture's session may be
	// cached; a second bob would not have one — drop it by reading with a
	// new runtime).
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	pv, err := carol.PreviewPublicPublication(ref)
	if err != nil {
		t.Fatal(err)
	}
	if pv.State != PreviewArchived {
		t.Fatalf("a withdrawn post read as %s", pv.State)
	}
	if !strings.Contains(pv.Reason, "withdrawn") {
		t.Fatalf("the wrong sentence: %q", pv.Reason)
	}
	_ = bob
}

// The preview must never show what a real replica would refuse: the same
// Authorized defense in depth, from the same computation.
func TestAPreviewIsNoLaxerThanAReplica(t *testing.T) {
	alice, bob, src, _, ref, done := previewFixture(t)
	defer done()

	// Craft a frame from an unauthorized principal by having a second
	// runtime emit into its OWN copy of the space id... which it cannot
	// sign as the space. Instead, verify the mechanism directly: the
	// materialized state carries the Authorized set of the verified
	// policy, non-nil for this curated revision-1 space.
	pv, err := bob.PreviewPublicPublication(ref)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read failed: %v %+v", err, pv)
	}
	sess := bob.previews.bySpace(src)
	if sess == nil {
		t.Fatal("no session after a resolved read")
	}
	if sess.state.Authorized == nil {
		t.Fatal("the preview dropped the curated defense in depth")
	}
	// The replica path computes the same set from the same manifest.
	var replicaAuth map[id.PrincipalID]bool
	if err := alice.withSpace(src, func(st *spaceState) error {
		replicaAuth = st.space.State.Authorized
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(replicaAuth) != len(sess.state.Authorized) {
		t.Fatalf("preview and replica disagree on who is authorized: %d vs %d",
			len(sess.state.Authorized), len(replicaAuth))
	}
	for p := range replicaAuth {
		if !sess.state.Authorized[p] {
			t.Fatal("preview and replica disagree on who is authorized")
		}
	}
}

func TestASecondReadWithinTheTTLDoesNotRefetch(t *testing.T) {
	_, bob, src, _, ref, done := previewFixture(t)
	defer done()

	if _, err := bob.PreviewPublicPublication(ref); err != nil {
		t.Fatal(err)
	}
	first := bob.previews.bySpace(src)
	if _, err := bob.PreviewPublicPublication(ref); err != nil {
		t.Fatal(err)
	}
	if got := bob.previews.bySpace(src); got != first {
		t.Fatal("a re-open within the TTL refetched")
	}
}

// A local-only or simply held space never previews: plain navigation.
func TestAHeldSpaceIsPlainNavigationNotAPreview(t *testing.T) {
	alice, _, src, doc, ref, done := previewFixture(t)
	defer done()

	pv, err := alice.PreviewPublicPublication(ref)
	if err != nil {
		t.Fatal(err)
	}
	if pv.State != PreviewExistingLocal {
		t.Fatalf("the owner previewed their own space: %s", pv.State)
	}
	if pv.Space != src || pv.Document != doc {
		t.Fatal("the navigation target drifted")
	}
	if pv.PreviewID != "" {
		t.Fatal("a session was created for a held space")
	}
	_ = terminals.SpacePolicy{}
}
