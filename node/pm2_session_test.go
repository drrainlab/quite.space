// The transient fetch session (PM-1 + PM-2): the allowlist is the post's
// graph, concurrent requests coalesce, budgets refuse at admission, and a
// stranger's Read post materializes a real cover from the owner — with
// nothing persisted anywhere.
package node

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// coverFixture: alice's public space with a post whose cover is a REAL
// ingested asset; the projection at the relay carries the carrier ref.
func coverFixture(t *testing.T) (alice, bob *Runtime, src id.TerminalID,
	ref string, img []byte, link string, done func()) {
	t.Helper()
	alice = openRuntime(t, t.TempDir(), "alice")
	bob = openRuntime(t, t.TempDir(), "bob")
	srv, addr := setUpRelay(t, alice)

	src = newPublicSpace(t, alice, "with pictures")
	img = bytes.Repeat([]byte{0x89, 0x50, 0x4E, 0x47}, 2048)
	aref, err := alice.IngestAsset(bytes.NewReader(img), int64(len(img)),
		assets.Metadata{MediaType: "image/png", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	carrier, err := (&schemas.AttachedBlock{Original: aref}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.EmitBlock(src, schemas.BlockAttached, carrier); err != nil {
		t.Fatal(err)
	}
	doc := &publication.Document{
		Kind: "article", Title: "с обложкой", Visibility: "space",
		Cover: aref.PublicIDHex(),
		Blocks: []publication.Block{{ID: "b1", Type: "text",
			RawProps: publication.EncodeTextProps(publication.TextProps{Text: "текст"})}},
	}
	if _, err := rand.Read(doc.DocumentID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.PublishDocument(src, doc, nil); err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(addr, src); err != nil {
		t.Fatal(err)
	}
	l, err := alice.ComposePublicLink(src, &doc.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	return alice, bob, src, aref.PublicIDHex(), img, l,
		func() { alice.Close(); bob.Close(); srv.Close() }
}

// The wave's thesis on real machinery: a stranger materializes the cover
// from the owner through the swarm, into memory, persisting nothing.
func TestStrangerMaterializesTheCoverWithoutFollowing(t *testing.T) {
	alice, bob, src, coverID, img, link, done := coverFixture(t)
	defer done()

	pv, err := bob.PreviewPublicPublication(link)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read failed: %v %+v", err, pv)
	}
	sess := bob.previews.bySpace(src)
	if sess == nil || sess.fetcher == nil {
		t.Fatal("no fetch pipeline on the session")
	}
	// The consent: an explicit fetch request for the cover.
	state, _, _, reason := sess.fetcher.request(coverID)
	if state == "" || state == FetchDescriptorGone {
		t.Fatalf("the cover is not requestable: %s %s", state, reason)
	}
	// The owner's ordinary drain answers; poll both sides briefly.
	addr := alice.GetSettings().Relay
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = alice.collectPublicIngress(addr, src)
		st, _, _, _ := sess.fetcher.status(coverID)
		if st == FetchReady {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	st, _, _, rsn := sess.fetcher.status(coverID)
	if st != FetchReady {
		t.Fatalf("the cover never materialized: %s (%s)", st, rsn)
	}
	data, _, ok := sess.fetcher.bytesFor(coverID)
	if !ok || !bytes.Equal(data, img) {
		t.Fatal("the bytes are not the image")
	}
	// Persisted NOTHING: not a replica, not a durable blob in bob's CAS.
	bob.mu.Lock()
	_, holds := bob.spaces[src]
	bob.mu.Unlock()
	if holds {
		t.Fatal("materializing followed the space")
	}
}

// The allowlist is the POST's graph: an asset of the space that no opened
// publication references is not requestable through the session.
func TestPreviewCannotRequestAssetOutsidePublicationGraph(t *testing.T) {
	alice, bob, src, _, _, link, done := coverFixture(t)
	defer done()

	// A second asset in the SPACE — carried by the projection, referenced
	// by no publication bob opened.
	stray := bytes.Repeat([]byte{0x33}, 4096)
	sref, err := alice.IngestAsset(bytes.NewReader(stray), int64(len(stray)),
		assets.Metadata{MediaType: "image/png", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	carrier, _ := (&schemas.AttachedBlock{Original: sref}).Encode()
	if _, err := alice.EmitBlock(src, schemas.BlockAttached, carrier); err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(alice.GetSettings().Relay, src); err != nil {
		t.Fatal(err)
	}

	pv, err := bob.PreviewPublicPublication(link)
	if err != nil || pv.State != PreviewResolved {
		t.Fatalf("read failed: %v %+v", err, pv)
	}
	sess := bob.previews.bySpace(src)
	if state, _, _, _ := sess.fetcher.request(sref.PublicIDHex()); state != "" {
		t.Fatalf("a stray space asset was requestable: %s", state)
	}
	// It IS in the projection's ref map — the boundary is the graph, not
	// the projection.
	if _, inProjection := sess.assets[sref.PublicIDHex()]; !inProjection {
		t.Fatal("fixture broke: the stray carrier did not reach the projection")
	}
}

// A publication that references media whose CARRIER the projection lacks:
// descriptor_unavailable, a fact about the projection — never a network
// silence, never a spinner.
func TestMissingCarrierReportsDescriptorUnavailable(t *testing.T) {
	f, err := newSessionFetcher(id.TerminalID{1}, "nowhere.example:1", nil, newMemSink(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	f.extendGraph(map[string]string{"ab12cd34ab12cd34ab12cd34ab12cd34": "cover"},
		map[string]*schemas.AssetRef{}) // the ref map has no carrier for it
	state, _, _, reason := f.request("ab12cd34ab12cd34ab12cd34ab12cd34")
	if state != FetchDescriptorGone {
		t.Fatalf("got %s, want descriptor_unavailable", state)
	}
	if reason == "" {
		t.Fatal("no sentence for the person")
	}
}

// Identical concurrent fetch requests coalesce into ONE job.
func TestConcurrentFetchRequestsCoalesce(t *testing.T) {
	f, err := newSessionFetcher(id.TerminalID{1}, "nowhere.example:1", nil, newMemSink(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	ref := &schemas.AssetRef{MediaType: "image/png", Size: 1024, ChunkSize: 4096,
		InlineChunks: []id.Hash{{1}}}
	f.extendGraph(map[string]string{"aa": "cover"}, map[string]*schemas.AssetRef{"aa": ref})
	f.request("aa")
	f.request("aa")
	f.request("aa")
	f.mu.Lock()
	n := len(f.jobs)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d jobs for one asset", n)
	}
}

// Peak-memory admission: an asset whose expected working set exceeds the
// global cap is refused AT ADMISSION, even when Size alone would fit.
func TestPeakWorkingSetIsRefusedAtAdmission(t *testing.T) {
	f, err := newSessionFetcher(id.TerminalID{1}, "nowhere.example:1", nil, newMemSink(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	// Size alone is under the global budget; 2×Size + slack is not.
	big := &schemas.AssetRef{MediaType: "video/mp4",
		Size: previewGlobalBudget/2 + 4096, ChunkSize: 65536,
		InlineChunks: []id.Hash{{1}}}
	f.extendGraph(map[string]string{"bb": "inline_video"},
		map[string]*schemas.AssetRef{"bb": big})
	state, _, _, reason := f.request("bb")
	if state != FetchBudget {
		t.Fatalf("got %s, want budget_exceeded", state)
	}
	if reason == "" {
		t.Fatal("no sentence for the person")
	}
}
