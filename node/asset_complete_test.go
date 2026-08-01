package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/assets"
)

// An asset this node ALREADY HOLDS must report itself complete.
//
// Reported from a live pair: a post's sound sat at "fetching… 57/57" and never
// played. Every chunk was on disk — the count says so — and the endpoint the
// client polls said "fetching" anyway, forever.
//
// The cause is an ordering, not a race. handleFetchAsset calls RequestAsset
// and THEN reads the status; RequestAsset marks the asset in-flight before
// returning, and assetStatusLocked lets that mark overwrite the state it just
// computed. So every poll re-arms the flag a moment before the answer is
// read, and the answer can never be "complete" — for any asset, however
// completely it has arrived.
//
// Nobody noticed because the two existing consumers do not depend on the
// verdict: an <img> loads its bytes from the GET route whatever the poll
// says. A bed is the first thing that WAITS for it.
func TestAnAssetWeAlreadyHoldReportsComplete(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Studio")
	if err != nil {
		t.Fatal(err)
	}

	content := randBytes(t, 60_000)
	ref := emitVisual(t, rt, tid, content, 4096)
	aid := ref.PublicIDHex()

	// The author holds every byte: this is the state a reader reaches when
	// the last chunk lands, and the answer must be the same for both.
	if st, res := assets.StateOf(rt.root, ref); st != assets.StateComplete {
		t.Fatalf("the test's own premise is wrong: %v (%d chunks missing)",
			st, len(res.MissingChunks))
	}

	// Exactly what the client does on every poll, in the same order.
	if err := rt.RequestAsset(tid, aid); err != nil {
		t.Fatal(err)
	}
	st, err2 := rt.AssetStatus(tid, aid)
	if err2 != nil {
		t.Fatal(err2)
	}
	if st.State != assets.StateComplete {
		t.Fatalf("an asset with every chunk on disk reported %q (missing %d of %d) — "+
			"a client waiting for the bytes waits forever", st.State, st.Missing, st.Total)
	}
	if st.Missing != 0 {
		t.Fatalf("complete but %d chunks missing — the two disagree", st.Missing)
	}
}

// And asking for it does not start a fetch at all: a poll every 1.2s would
// otherwise spawn a goroutine per poll, each one re-registering relay wants
// for an asset that is already here.
func TestRequestingAHeldAssetStartsNoFetch(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Studio")
	if err != nil {
		t.Fatal(err)
	}

	ref := emitVisual(t, rt, tid, randBytes(t, 60_000), 4096)
	if err := rt.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	fetching := rt.assetIdx.fetching[AssetKey{Space: tid, Asset: ref.PublicIDHex()}]
	rt.mu.Unlock()
	if fetching {
		t.Fatal("a fetch was started for an asset this node already holds")
	}
}
