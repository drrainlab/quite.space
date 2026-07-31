// The PS wave, end to end (PS-5).
//
// The sentences this wave exists for, on real nodes over a real relay:
//
//	a public post travels as a card, and the card decides nothing on the
//	recipient's behalf — reading is temporary, following is a choice
//
//	reading a post never grants the right to write, however open the
//	community is
//
//	the reference carries the relay the post actually lives at, even
//	when the sender's own relay is somewhere else
package node

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// TestOpeningSharedPostDoesNotFollowOrJoinSpace is the wave's headline:
// the full arc, card to restart to Follow, with every boundary checked.
func TestOpeningSharedPostDoesNotFollowOrJoinSpace(t *testing.T) {
	aliceDir, bobDir := t.TempDir(), t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	defer alice.Close()
	bob := openRuntime(t, bobDir, "bob")
	srv, addr := setUpRelay(t, alice, bob)
	defer srv.Close()

	// Alice: a public space with a post, projected to the relay; a dyad
	// with bob for the card to travel through.
	src := newPublicSpace(t, alice, "slow technology")
	doc := publishPost(t, alice, src, "Почему локальные сети снова важны", "Короткое эссе")
	if err := alice.publishPublicProjection(addr, src); err != nil {
		t.Fatal(err)
	}
	dyad := shareTogether(t, alice, bob, addr, "alice and bob")

	// 1. Alice forwards the post; the card arrives at bob's over the relay.
	res, err := alice.SharePost(src, doc, []id.TerminalID{dyad},
		ShareOptions{NameAuthor: true, Comment: "почитай"})
	if err != nil || !res[0].OK {
		t.Fatalf("share failed: %v %+v", err, res)
	}
	waitForText(t, bob, dyad, "Почему локальные сети")
	card := cardOf(t, bob, dyad)
	if card == nil || card.Reference == "" {
		t.Fatalf("the card did not arrive intact: %+v", card)
	}

	// 2. Bob presses Read post. The post opens.
	pv, err := bob.PreviewPublicPublication(card.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if pv.State != PreviewResolved || pv.Pub == nil {
		t.Fatalf("read failed: %s (%s)", pv.State, pv.Reason)
	}
	if !strings.Contains(pv.Pub.Title, "локальные сети") {
		t.Fatalf("the wrong post: %q", pv.Pub.Title)
	}

	// 3-5. Bob closes it and restarts. The card is still in the message;
	// the source space is NOWHERE.
	relayBefore := bob.GetSettings().Relay
	bob.Close()
	bob2 := openRuntime(t, bobDir, "bob")
	if got := cardOf(t, bob2, dyad); got == nil || got.Reference != card.Reference {
		t.Fatal("the forwarded card did not survive the restart")
	}
	bob2.mu.Lock()
	_, holds := bob2.spaces[src]
	_, hasMeta := bob2.ks.Spaces[src]
	pins := len(bob2.ks.Navigator.Pins)
	bob2.mu.Unlock()
	if holds || hasMeta {
		t.Fatal("reading followed the space")
	}
	if pins != 0 {
		t.Fatal("reading touched the Navigator")
	}
	if bob2.GetSettings().Relay != relayBefore {
		t.Fatal("reading changed the relay settings")
	}

	// 6-7. Bob opens the card again and presses Follow. Only NOW does a
	// reader replica exist — and survive a restart.
	if _, err := bob2.FollowPublicSpace(card.Reference); err != nil {
		t.Fatal(err)
	}
	bob2.Close()
	bob3 := openRuntime(t, bobDir, "bob")
	defer bob3.Close()
	bob3.mu.Lock()
	_, holds = bob3.spaces[src]
	bob3.mu.Unlock()
	if !holds {
		t.Fatal("Follow did not survive the restart")
	}
	// And the followed replica reads the post through the ordinary path.
	// A projection re-installs from the relay on the sync loop after a
	// restart; run one fetch deterministically instead of sleeping on it.
	if err := bob3.fetchPublicProjection(addr, src); err != nil {
		t.Fatal(err)
	}
	if err := bob3.withSpace(src, func(st *spaceState) error {
		if _, ok := st.space.State.PublicationByID(doc); !ok {
			t.Fatal("the followed space does not hold the post")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPreviewingOpenCommunityDoesNotGrantWriteAccess: reading, and even
// following read-only, never grants the right to write. Participation is
// its own explicit flow.
func TestPreviewingOpenCommunityDoesNotGrantWriteAccess(t *testing.T) {
	aliceDir, bobDir := t.TempDir(), t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	defer alice.Close()
	bob := openRuntime(t, bobDir, "bob")
	defer bob.Close()
	srv, addr := setUpRelay(t, alice)
	defer srv.Close()
	_ = addr

	src, err := alice.CreateSpaceWithOptions("open commons", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := publishPost(t, alice, src, "a community piece", "written in the open")
	if err := alice.publishPublicProjection(addr, src); err != nil {
		t.Fatal(err)
	}
	ref, err := alice.ComposePublicLink(src, &doc)
	if err != nil {
		t.Fatal(err)
	}

	// Reading grants nothing — there is no replica to even ask.
	pv, err := bob.PreviewPublicPublication(ref)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read failed: %v %+v", err, pv)
	}
	if _, err := bob.Say(src, "hello from a reader", SayOptions{}); err == nil {
		t.Fatal("a preview granted a write path")
	}

	// Follow read-only: a replica exists and still refuses to write.
	if _, err := bob.FollowPublicSpace(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(src, "hello from a follower", SayOptions{}); err == nil {
		t.Fatal("following read-only granted a write path")
	}

	// Join and participate is the separate explicit flow, and only IT
	// grants writing.
	if err := bob.JoinPublicSpace(src); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(src, "hello as a participant", SayOptions{}); err != nil {
		t.Fatalf("joining did not grant writing: %v", err)
	}
}

// A post WITH a cover: the recipient must get the honest state for bytes
// that cannot be had, never a silent failure of the whole preview.
func TestAForwardedPostWithACoverStaysHonestAboutTheBytes(t *testing.T) {
	aliceDir := t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, addr := setUpRelay(t, alice)
	defer srv.Close()

	src := newPublicSpace(t, alice, "with pictures")
	// A real ingested asset, carried by the block frame that indexes it.
	img := bytes.Repeat([]byte{0x89, 0x50, 0x4E, 0x47}, 512)
	ref, err := alice.IngestAsset(bytes.NewReader(img), int64(len(img)),
		assets.Metadata{MediaType: "image/png", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	carrier, err := (&schemas.AttachedBlock{Original: ref}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.EmitBlock(src, schemas.BlockAttached, carrier); err != nil {
		t.Fatal(err)
	}
	doc := &publication.Document{
		Kind: "article", Title: "иллюстрированное", Summary: "с обложкой",
		Visibility: "space", Cover: ref.PublicIDHex(),
		Blocks: []publication.Block{{ID: "b1", Type: "text",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "текст"})}},
	}
	doc.DocumentID = [16]byte{7, 7, 7}
	if _, err := alice.PublishDocument(src, doc, nil); err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(addr, src); err != nil {
		t.Fatal(err)
	}
	link, err := alice.ComposePublicLink(src, &doc.DocumentID)
	if err != nil {
		t.Fatal(err)
	}

	pv, err := bob.PreviewPublicPublication(link)
	if err != nil {
		t.Fatal(err)
	}
	if pv.State != PreviewResolved {
		t.Fatalf("a post with a cover did not resolve: %s (%s)", pv.State, pv.Reason)
	}
	if pv.Pub.Document.Cover == "" {
		t.Fatal("the cover reference was lost in the preview")
	}
	// The session knows the ref (the carrier frame rode the projection) —
	// whether the BYTES can be had is a separate, honest question the
	// asset route answers per request. Here bob never fetched the blob, so
	// the store misses and the route would answer no_source rather than
	// serving garbage. Assert the ref is at least addressable.
	sess := bob.previews.bySpace(src)
	if sess == nil {
		t.Fatal("no session")
	}
	if _, ok := sess.assets[pv.Pub.Document.Cover]; !ok {
		t.Fatal("the cover's ref did not reach the session")
	}
	if _, err := assets.RetrieveBytes(bob.root, sess.assets[pv.Pub.Document.Cover]); err == nil {
		t.Fatal("bytes bob never fetched somehow retrieved")
	}
}

// The wave's actual use case: a READER forwards somebody else's post, and
// the reference carries the relay the post actually lives at — not the
// reader's own.
func TestAReadersCardCarriesThePublishersRelay(t *testing.T) {
	aliceDir, bobDir, carolDir := t.TempDir(), t.TempDir(), t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	defer alice.Close()
	bob := openRuntime(t, bobDir, "bob")
	defer bob.Close()
	carol := openRuntime(t, carolDir, "carol")
	defer carol.Close()
	srv, addr := setUpRelay(t, alice)
	defer srv.Close()

	src := newPublicSpace(t, alice, "the publisher's space")
	doc := publishPost(t, alice, src, "worth passing on", "a piece")
	if err := alice.publishPublicProjection(addr, src); err != nil {
		t.Fatal(err)
	}

	// Bob follows alice's space from its link — his own global relay is
	// configured SOMEWHERE ELSE, which is exactly the hazard.
	s := bob.GetSettings()
	s.Relay = "bobs-own-relay.example:7411"
	if err := bob.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	link, err := alice.ComposePublicLink(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.FollowPublicSpace(link); err != nil {
		t.Fatal(err)
	}
	// The projection fetch recorded where it actually came from.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		bob.mu.Lock()
		got := bob.ks.Spaces[src].SourceRelay
		bob.mu.Unlock()
		if got == addr {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Bob forwards the post to a private space with carol.
	dest := shareTogether(t, bob, carol, addr, "bob and carol")
	res, err := bob.SharePost(src, doc, []id.TerminalID{dest}, ShareOptions{NameAuthor: true})
	if err != nil || !res[0].OK {
		t.Fatalf("the reader's share failed: %v %+v", err, res)
	}
	card := cardOf(t, bob, dest)
	if card == nil || card.Reference == "" {
		t.Fatal("no card from the reader")
	}
	relayInRef, tid, d, err := ParsePublicLink(card.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if relayInRef != addr {
		t.Fatalf("the reference carries the reader's relay %q, not the publisher's %q",
			relayInRef, addr)
	}
	if tid != src || d == nil || *d != doc {
		t.Fatal("the reference does not point at the post")
	}
	_ = signal.AuthorshipHuman
}
