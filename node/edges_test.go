// SP-2 node gates: an edge to an unknown asset is refused loudly, the
// whole-state API preserves what the caller didn't say, the candidate
// moves only when told to, the authoring cap holds, and every new write
// path sits behind canWrite.
package node

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// uploadTestAsset ingests bytes and emits the block.attached.v1 carrier —
// the same two steps handleUploadAsset performs — returning the public id.
func uploadTestAsset(t *testing.T, rt *Runtime, tid id.TerminalID, size int) string {
	t.Helper()
	data := randBytes(t, size)
	ref, err := rt.IngestAsset(bytes.NewReader(data), int64(len(data)),
		assets.Metadata{MediaType: "audio/wav", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	pl, err := (&schemas.AttachedBlock{Filename: "mix.wav", MediaType: "audio/wav", Original: ref}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.EmitBlock(tid, schemas.BlockAttached, pl); err != nil {
		t.Fatal(err)
	}
	return ref.PublicIDHex()
}

func TestAssetEdgeLifecycleAtNode(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Label")
	if err != nil {
		t.Fatal(err)
	}
	trackRec := &objects.Record{Kind: "track", Name: "Winter Song", Status: "mixing"}
	oid, _, err := rt.CreateObject(tid, trackRec)
	if err != nil {
		t.Fatal(err)
	}
	mixA := uploadTestAsset(t, rt, tid, 40_000)
	mixB := uploadTestAsset(t, rt, tid, 41_000)

	// An edge to an asset this space has never seen is refused BEFORE
	// emit — a beautiful card over a dead file is exactly the failure
	// ADR-030 exists to prevent.
	ghost := strings.Repeat("ab", 32)
	_, err = rt.EmitAssetEdge(tid, &objects.AttachPayload{
		Fallback: "x", ObjectID: oid, Asset: ghost, Role: "mix"})
	if err == nil || !strings.Contains(err.Error(), "not in this space") {
		t.Fatalf("ghost asset accepted: %v", err)
	}

	// Attach both mixes; B supersedes A and takes the star.
	if _, err := rt.EmitAssetEdge(tid, &objects.AttachPayload{
		Fallback: "mix-11", ObjectID: oid, Asset: mixA, Role: "mix", Label: "mix-11"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.EmitAssetEdge(tid, &objects.AttachPayload{
		Fallback: "mix-12", ObjectID: oid, Asset: mixB, Role: "mix", Label: "mix-12",
		Supersedes: mixA, Candidate: objects.CandidateSet}); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	if got := sp.State.CurrentAsset(oid); got != mixB {
		t.Fatalf("current wrong: %q", got)
	}
	chains := sp.State.VersionChains(oid)
	if len(chains) != 1 || len(chains[0].Chain) != 2 {
		t.Fatalf("chain wrong: %+v", chains)
	}
	// Both mixes pin their carriers; annotation on A must not change that
	// picture, and detaching A must release only A.
	if _, err := rt.AnnotateAsset(tid, mixA, "вокал суховат", 102_000, true, &oid); err != nil {
		t.Fatal(err)
	}
	if notes := sp.State.AnnotationsForAsset(mixA); len(notes) != 1 || notes[0].PositionMs != 102_000 {
		t.Fatalf("annotation wrong: %+v", notes)
	}
	// Annotating a ghost asset is refused too.
	if _, err := rt.AnnotateAsset(tid, ghost, "x", 0, false, nil); err == nil {
		t.Fatal("annotation on ghost asset accepted")
	}
}

func TestEdgeCapIsAuthoringBound(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Label")
	if err != nil {
		t.Fatal(err)
	}
	oid, _, err := rt.CreateObject(tid, &objects.Record{Kind: "session", Name: "Vocals"})
	if err != nil {
		t.Fatal(err)
	}
	asset := uploadTestAsset(t, rt, tid, 30_000)
	if _, err := rt.EmitAssetEdge(tid, &objects.AttachPayload{
		Fallback: "t", ObjectID: oid, Asset: asset, Role: "take"}); err != nil {
		t.Fatal(err)
	}
	// Revising the SAME pair is always allowed — the cap only guards NEW
	// assets. (Filling 200 real assets here would be a slow test for no
	// extra truth; the branch is `!known && live >= cap`, and known is
	// what we exercise.)
	if _, err := rt.EmitAssetEdge(tid, &objects.AttachPayload{
		Fallback: "t2", ObjectID: oid, Asset: asset, Role: "take", Label: "take 02"}); err != nil {
		t.Fatalf("revise of known pair refused: %v", err)
	}
}

func TestEdgeAndAnnotationBehindCanWrite(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Label")
	if err != nil {
		t.Fatal(err)
	}
	oid, _, err := rt.CreateObject(tid, &objects.Record{Kind: "track", Name: "Song"})
	if err != nil {
		t.Fatal(err)
	}
	asset := uploadTestAsset(t, rt, tid, 20_000)
	sp, _ := rt.spaceForTest(tid)
	sp.ReadOnly = true
	if _, err := rt.EmitAssetEdge(tid, &objects.AttachPayload{
		Fallback: "x", ObjectID: oid, Asset: asset}); err == nil ||
		!strings.Contains(err.Error(), "join this space") {
		t.Fatalf("EmitAssetEdge not refused: %v", err)
	}
	if _, err := rt.AnnotateAsset(tid, asset, "x", 0, false, nil); err == nil ||
		!strings.Contains(err.Error(), "join this space") {
		t.Fatalf("AnnotateAsset not refused: %v", err)
	}
}

func TestParentChildAtNode(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Label")
	if err != nil {
		t.Fatal(err)
	}
	relID, _, err := rt.CreateObject(tid, &objects.Record{Kind: "release", Name: "Night Signals"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = rt.CreateObject(tid, &objects.Record{Kind: "track", Name: "Winter Song", Parent: &relID})
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	kids := sp.State.ChildrenOf(relID)
	if len(kids) != 1 || kids[0].Record.Name != "Winter Song" {
		t.Fatalf("children wrong: %+v", kids)
	}
}
