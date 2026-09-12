package node

// A holder that answers AFTER the fetch gave up is still a holder: the
// verdict clears and the fetch resumes. Seen on a stand — the sender's
// phone answered a want two minutes late, and the reader's projection kept
// saying "did not answer" while the chunks were landing.

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func TestALateAnswerReopensAFetchThatGaveUp(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid := openPublicSpaceForMirror(t, owner, "Late Media")
	ref := emitVisual(t, owner, tid, randBytes(t, 200_000), 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := reader.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	key := AssetKey{Space: tid, Asset: ref.PublicIDHex()}
	waitUntil(t, 20*time.Second, "reader never indexed the asset", func() bool {
		_ = reader.fetchPublicProjection(addr, tid)
		_, err := reader.AssetStatus(tid, ref.PublicIDHex())
		return err == nil
	})

	// The state a give-up leaves behind: the loop is gone and the verdict
	// stands. (Reaching it for real costs the two-minute idle deadline,
	// which is deliberately not a knob — cadence.go.)
	reader.mu.Lock()
	reader.assetIdx.failed[key] = ReasonNoSource
	reader.mu.Unlock()
	st, err := reader.AssetStatus(tid, ref.PublicIDHex())
	if err != nil || st.State != assets.StateFailed {
		t.Fatalf("precondition: want failed, got %+v %v", st, err)
	}

	// The late answer: the holder's manifest lands the way the pump lands
	// it — stored, then announced to the index.
	man, err := owner.root.GetBlob(*ref.ManifestWireID)
	if err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	if _, err := reader.root.PutBlob(man); err != nil {
		reader.mu.Unlock()
		t.Fatal(err)
	}
	reader.onBlobStored(*ref.ManifestWireID)
	reader.mu.Unlock()

	st, err = reader.AssetStatus(tid, ref.PublicIDHex())
	if err != nil {
		t.Fatal(err)
	}
	if st.State == assets.StateFailed {
		t.Fatalf("a chunk landed and the projection still says failed: %+v", st)
	}
	// And the fetch is alive again, asking the holder for the rest.
	waitUntil(t, 60*time.Second, "the reopened fetch never completed", func() bool {
		st, err := reader.AssetStatus(tid, ref.PublicIDHex())
		return err == nil && st.State == assets.StateComplete
	})
}
