package node

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// emitVisual posts a visual block with an ingested asset (Gate C's
// PostBlock does this via API; Gate B exercises the same path directly).
func emitVisual(t *testing.T, rt *Runtime, tid id.TerminalID, data []byte, chunkSize int) *schemas.AssetRef {
	t.Helper()
	ref, err := rt.IngestAsset(bytes.NewReader(data), int64(len(data)),
		assets.Metadata{MediaType: "image/webp", Role: "original", ChunkSize: chunkSize})
	if err != nil {
		t.Fatal(err)
	}
	block := &schemas.VisualBlock{Alt: "test image", Original: ref}
	payload, err := block.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.Space(tid)
	rt.mu.Lock()
	_, err = rt.Self.Emit(sp, schemas.BlockVisual, payload, signal.AuthorshipHuman, uint64(time.Now().Unix()))
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// The Gate B proof: two headless nodes exchange a multi-chunk encrypted
// asset lazily over a real link — event first (with the ref), then manifest,
// then chunks, all scoped to the shared space.
func TestTwoNodesAssetExchange(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()

	tid, err := rtA.CreateSpace("Media Lab")
	if err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 200_000) // 49 chunks at 4KiB → external manifest
	ref := emitVisual(t, rtA, tid, content, 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}

	invite, err := rtA.MintInvite(tid, rtB.Device.ID, rtB.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtB.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if err := rtA.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := rtB.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := rtB.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", rtA.LAN().Port)); err != nil {
		t.Fatal(err)
	}

	// Phase 0: the block event arrives; B indexes the ref from the
	// decrypted payload and honestly reports manifest_missing.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if st, err := rtB.AssetStatus(tid, ref.AssetID); err == nil {
			if st.State != assets.StateManifestMissing && st.State != assets.StateFetching {
				t.Fatalf("unexpected initial state %v", st.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("block event never reached B")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Phases 1+2: fetch manifest, then chunks.
	if err := rtB.RequestAsset(tid, ref.AssetID); err != nil {
		t.Fatal(err)
	}
	for {
		st, err := rtB.AssetStatus(tid, ref.AssetID)
		if err != nil {
			t.Fatal(err)
		}
		if st.State == assets.StateComplete {
			break
		}
		if st.State == assets.StateFailed {
			t.Fatalf("fetch failed: %s", st.Reason)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no completion: %+v", st)
		}
		time.Sleep(100 * time.Millisecond)
	}
	got, _, err := rtB.RetrieveAsset(tid, ref.AssetID)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("retrieved content wrong: %v", err)
	}
}

// Plan verification #2, directly: after a restart the node re-derives
// manifest_wire_id→space from the log and — because the manifest is local —
// decrypts it and re-authorizes chunk wire ids for the space.
func TestRestartRebuildsAssetIndexes(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Archive")
	if err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 100_000)
	ref := emitVisual(t, rt, tid, content, 4096)
	man, err := assets.LoadManifest(rt.root, ref)
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	rt2.mu.Lock()
	manifestAllowed := rt2.assetIdx.allowed(*ref.ManifestWireID, tid)
	chunkAllowed := rt2.assetIdx.allowed(man.Chunks[0], tid)
	lastChunkAllowed := rt2.assetIdx.allowed(man.Chunks[len(man.Chunks)-1], tid)
	rt2.mu.Unlock()
	if !manifestAllowed {
		t.Fatal("manifest wire id not reindexed for space after restart")
	}
	if !chunkAllowed || !lastChunkAllowed {
		t.Fatal("chunk wire ids not reindexed from local manifest after restart")
	}
	// And the asset is still fully retrievable.
	got, _, err := rt2.RetrieveAsset(tid, ref.AssetID)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("asset lost across restart: %v", err)
	}
}

// Dead drop with media: the relay bundle carries encrypted blobs; the
// offline peer pulls event + manifest + chunks in one collect.
func TestRelayDeadDropCarriesAssets(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()

	tid, err := rtA.CreateSpace("Dead Drop Media")
	if err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 60_000)
	ref := emitVisual(t, rtA, tid, content, 16384) // 4 chunks → inline refs
	invite, err := rtA.MintInvite(tid, rtB.Device.ID, rtB.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtB.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}

	if _, _, err := rtA.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := rtB.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	got, _, err := rtB.RetrieveAsset(tid, ref.AssetID)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("asset did not survive the dead drop: %v", err)
	}
}

// Bundle format: blobs round-trip, and decoders skip unknown future keys
// (ADR-009 compatibility, tested directly).
func TestBundleBlobsAndUnknownKeySkip(t *testing.T) {
	tid := id.TerminalID{0xBB}
	frames := [][]byte{{1, 2, 3}}
	blobs := [][]byte{{9, 8, 7}, {6, 5}}
	data := bundle.EncodeWithBlobs(tid, frames, blobs)
	gotTid, gotFrames, gotBlobs, err := bundle.DecodeFull(data)
	if err != nil || gotTid != tid || len(gotFrames) != 1 || len(gotBlobs) != 2 {
		t.Fatalf("blob round trip: %v", err)
	}
	// The 3-return Decode (an "old" reader) sees frames and ignores blobs.
	gotTid2, gotFrames2, err := bundle.Decode(data)
	if err != nil || gotTid2 != tid || len(gotFrames2) != 1 {
		t.Fatalf("old-style decode: %v", err)
	}
}
