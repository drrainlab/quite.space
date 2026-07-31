// PM-6: promotion on Follow, the mirror path, and the invariants the
// whole wave turns on — on real nodes over a real relay.
package node

import (
	"bytes"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/projection"
)

// materialize drives one session asset to ready while the answering side
// runs its ordinary drain.
func materialize(t *testing.T, reader *Runtime, src id.TerminalID, aid string,
	answer func()) string {
	t.Helper()
	sess := reader.previews.bySpace(src)
	if sess == nil || sess.fetcher == nil {
		t.Fatal("no session fetcher")
	}
	if state, _, _, reason := sess.fetcher.request(aid); state == "" || state == FetchDescriptorGone {
		t.Fatalf("not requestable: %s %s", state, reason)
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		answer()
		st, _, _, _ := sess.fetcher.status(aid)
		if st == FetchReady {
			return st
		}
		time.Sleep(300 * time.Millisecond)
	}
	st, _, _, reason := sess.fetcher.status(aid)
	t.Fatalf("never materialized: %s (%s)", st, reason)
	return st
}

// TestFollowPromotesVerifiedPreviewAssetsWithoutRedownload — and asserts
// CHUNK INDEXING after promotion, not just presence: an asset that
// reports complete but is skipped by LAN serving and exports is the bug
// promotion exists to prevent.
func TestFollowPromotesVerifiedPreviewAssetsWithoutRedownload(t *testing.T) {
	alice, bob, src, coverID, img, link, done := coverFixture(t)
	defer done()

	pv, err := bob.PreviewPublicPublication(link)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read failed: %v %+v", err, pv)
	}
	addr := alice.GetSettings().Relay
	materialize(t, bob, src, coverID, func() { _, _ = alice.collectPublicIngress(addr, src) })

	// Follow. Promotion copies the session's verified bytes into the CAS.
	if _, err := bob.FollowPublicSpace(link); err != nil {
		t.Fatal(err)
	}
	// The bytes are durable, WITHOUT re-downloading: retrievable straight
	// from bob's root through the ordinary space path.
	data, _, err := bob.RetrieveAsset(src, coverID)
	if err != nil || !bytes.Equal(data, img) {
		t.Fatalf("promotion did not land the bytes: %v", err)
	}
	// And the CHUNKS are indexed: the space's allow-set covers the wire
	// ids, so LAN serving and exports see the asset — the read-through/
	// onBlobStored half of promotion.
	bob.mu.Lock()
	ref := bob.assetIdx.refs[AssetKey{Space: src, Asset: coverID}]
	indexed := false
	if ref != nil {
		st, _ := assets.StateOf(bob.root, ref)
		indexed = st == assets.StateComplete
		if ref.ManifestWireID != nil {
			if man, err := assets.LoadManifest(bob.root, ref); err == nil {
				indexed = true
				for _, c := range man.Chunks {
					if !bob.assetIdx.allowed(c, src) {
						indexed = false
					}
				}
			}
		}
	}
	bob.mu.Unlock()
	if ref == nil {
		t.Fatal("the followed replica never indexed the ref")
	}
	if !indexed {
		t.Fatal("promotion left chunks unindexed — complete for the UI, invisible to serving")
	}
}

// TestPostMaterializesFromMirrorWhileOwnerIsOffline: the interchangeable-
// sources promise. The owner leaves; a mirror keeps the projection AND
// answers the wants; a stranger still reads the whole post.
func TestPostMaterializesFromMirrorWhileOwnerIsOffline(t *testing.T) {
	alice, bob, src, coverID, img, link, done := coverFixture(t)
	defer done()
	addr := alice.GetSettings().Relay

	// A mirror node takes custody while the owner is still around.
	mirror := openRuntime(t, t.TempDir(), "mirror")
	defer mirror.Close()
	s := mirror.GetSettings()
	s.Relay = addr
	if err := mirror.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.OpenPublicLink(link); err != nil {
		t.Fatal(err)
	}
	if err := mirror.SetMirror(src, true); err != nil {
		t.Fatal(err)
	}
	// Greedy custody through the PRODUCTION machinery: install kicks the
	// custody fetch, the ingress push carries the wants, the owner
	// answers, and PullFromRelay drains the reply box into the root.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_ = mirror.fetchPublicProjection(addr, src)
		var frames [][]byte
		_ = mirror.withSpace(src, func(st *spaceState) error {
			if len(st.projWire) > 0 {
				if env, err := projection.Decode(st.projWire); err == nil {
					frames = env.Frames
				}
			}
			return nil
		})
		// OUTSIDE the lock: requestIncompleteAssets takes r.mu itself.
		mirror.requestIncompleteAssets(src, frames)
		_ = mirror.pushPublicIngress(addr, src)
		_, _ = alice.collectPublicIngress(addr, src)
		_, _ = mirror.PullFromRelay(addr)
		if _, _, err := mirror.RetrieveAsset(src, coverID); err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if _, _, err := mirror.RetrieveAsset(src, coverID); err != nil {
		t.Fatalf("the mirror never took custody: %v", err)
	}

	// The owner goes dark.
	alice.Close()

	// A stranger reads the post and materializes the cover FROM THE MIRROR.
	pv, err := bob.PreviewPublicPublication(link)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read from the mirror's world failed: %v %+v", err, pv)
	}
	materialize(t, bob, src, coverID, func() { _ = mirror.seedForSpace(addr, src) })
	sess := bob.previews.bySpace(src)
	data, _, ok := sess.fetcher.bytesFor(coverID)
	if !ok || !bytes.Equal(data, img) {
		t.Fatal("the mirror's answer is not the image")
	}
}

// TestReceivingSharedCardPerformsZeroAssetRequests pins the lazy-privacy
// invariant at the node boundary: a card ARRIVING creates no preview
// session and no fetch state — the network is touched only by Read post.
func TestReceivingSharedCardPerformsZeroAssetRequests(t *testing.T) {
	alice, bob, src, _, _, _, done := coverFixture(t)
	defer done()

	// The card travels to bob through a shared dyad. Bob's node needs the
	// relay in settings for HIS sync loop to pull the dyad's bundle — the
	// coverFixture leaves him unconfigured on purpose (the preview dials
	// the link's relay), but a member of a private space syncs normally.
	addr := alice.GetSettings().Relay
	bs := bob.GetSettings()
	bs.Relay = addr
	if err := bob.SetSettings(bs); err != nil {
		t.Fatal(err)
	}
	dyad := shareTogether(t, alice, bob, addr, "alice and bob")
	pubs := func() [16]byte {
		var doc [16]byte
		_ = alice.withSpace(src, func(st *spaceState) error {
			for _, p := range st.space.State.Publications() {
				doc = p.DocumentID
			}
			return nil
		})
		return doc
	}()
	res, err := alice.SharePost(src, pubs, []id.TerminalID{dyad}, ShareOptions{NameAuthor: true})
	if err != nil || !res[0].OK {
		t.Fatalf("share failed: %v %+v", err, res)
	}
	waitForText(t, bob, dyad, "с обложкой")

	// The card is HERE — and nothing else is.
	if got := bob.previews.bySpace(src); got != nil {
		t.Fatal("a card's arrival created a preview session")
	}
	bob.previews.mu.Lock()
	n := len(bob.previews.sessions)
	bob.previews.mu.Unlock()
	if n != 0 {
		t.Fatalf("a card's arrival created %d sessions", n)
	}
}

// GET is passive: reading a session asset's route never starts a fetch.
func TestAssetGETNeverTriggersTheSwarm(t *testing.T) {
	_, bob, src, coverID, _, link, done := coverFixture(t)
	defer done()

	pv, err := bob.PreviewPublicPublication(link)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read failed: %v %+v", err, pv)
	}
	sess := bob.previews.bySpace(src)
	// The status read IS the GET path's question; it must not create a job.
	if st, _, _, _ := sess.fetcher.status(coverID); st != FetchNotRequested {
		t.Fatalf("a passive read found state %s", st)
	}
	sess.fetcher.mu.Lock()
	jobs := len(sess.fetcher.jobs)
	sess.fetcher.mu.Unlock()
	if jobs != 0 {
		t.Fatalf("a passive read created %d jobs", jobs)
	}
}

// An archived post's withdrawn media is not offered for fetch: its graph
// never extends the allowlist.
func TestArchivedPostMediaIsNotOfferedForFetch(t *testing.T) {
	alice, bob, src, coverID, _, link, done := coverFixture(t)
	defer done()

	var doc [16]byte
	_ = alice.withSpace(src, func(st *spaceState) error {
		for _, p := range st.space.State.Publications() {
			doc = p.DocumentID
		}
		return nil
	})
	if _, err := alice.ArchiveDocument(src, doc); err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, src); err != nil {
		t.Fatal(err)
	}

	pv, err := bob.PreviewPublicPublication(link)
	if err != nil {
		t.Fatal(err)
	}
	if pv.State != PreviewArchived {
		t.Fatalf("expected archived, got %s", pv.State)
	}
	sess := bob.previews.bySpace(src)
	if state, _, _, _ := sess.fetcher.request(coverID); state != "" {
		t.Fatalf("withdrawn media was requestable: %s", state)
	}
}
