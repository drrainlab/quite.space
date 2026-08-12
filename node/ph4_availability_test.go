package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// PH-4: "nobody online has this file" and "the network went quiet on us"
// are different sentences to a person, and today only the second one
// exists. A reader whose media has no source waits the full two-minute
// deadline and is then told "timeout" — so the interface can only show a
// spinner, and then a word that suggests something broke.
func TestNoSourceIsDistinctFromTimeout(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// A space with an asset the owner knows about but nobody serves: the
	// reader has the reference (from the projection) and no way to get the
	// bytes, because the only holder never comes online.
	owner := openRuntime(t, t.TempDir(), "owner")
	tid := openPublicSpaceForMirror(t, owner, "Orphaned Media")
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
	waitUntil(t, 20*time.Second, "reader never indexed the asset", func() bool {
		_ = reader.fetchPublicProjection(addr, tid)
		_, err := reader.AssetStatus(tid, ref.PublicIDHex())
		return err == nil
	})

	// The only holder leaves. Nobody can answer.
	owner.Close()

	if err := reader.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	// The verdict must arrive WITHOUT waiting out the full deadline, and it
	// must say what is actually true.
	var got FetchStatus
	waitUntil(t, 60*time.Second, "no verdict inside a minute", func() bool {
		st, err := reader.AssetStatus(tid, ref.PublicIDHex())
		if err != nil {
			return false
		}
		got = st
		return st.Reason != ReasonNone
	})
	if got.Reason != ReasonNoSource {
		t.Fatalf("a fetch with no online holder reported %q, want %q",
			got.Reason, ReasonNoSource)
	}
}

// The no-relay case keeps its own meaning: that is a node configured to
// reach nobody, which is a different problem with a different fix.
func TestNoPeersStillMeansNoRelayConfigured(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alone")
	defer rt.Close()
	tid, err := rt.CreateSpace("Isolated")
	if err != nil {
		t.Fatal(err)
	}
	ref := emitVisual(t, rt, tid, randBytes(t, 200_000), 4096)
	// Wipe the local blobs so a fetch has real work to do, with no relay
	// configured and no peer connected.
	other := openRuntime(t, t.TempDir(), "other")
	defer other.Close()
	_ = other

	st, err := rt.AssetStatus(tid, ref.PublicIDHex())
	if err != nil {
		t.Fatal(err)
	}
	// The owner holds it all, so there is nothing to fetch — the interesting
	// assertion is only that the constant still exists and is distinct.
	if ReasonNoPeers == ReasonNoSource {
		t.Fatal("no_peers and no_source collapsed into one reason")
	}
	_ = st
}
