package node

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// A follower's media after a RESTART.
//
// Reported from a real pair of nodes: the text of a followed public space
// caught up, but the posts' pictures did not come back after the reader was
// restarted. Everything a reader needs to ask for media is in memory — the
// want set, the reply box, the space's ingress address — and all three are
// deliberately not persisted. So the question this asks is whether a reader
// that has forgotten all of it can still recover by simply asking again,
// which is what opening the post does.
func TestAFollowerStillGetsMediaAfterARestart(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "alice")
	defer owner.Close()
	tid := openPublicSpaceForMirror(t, owner, "Meditation")
	if _, err := owner.Say(tid, "before the picture", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 200_000) // big enough for the manifest path
	ref := emitVisual(t, owner, tid, content, 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	// Bob follows, and gets the picture the ordinary way first — so the
	// restart is the only thing under test.
	dir := t.TempDir()
	bob := openRuntime(t, dir, "bob")
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := bob.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	pump := func(rt *Runtime) bool {
		t.Helper()
		_ = rt.fetchPublicProjection(addr, tid)
		_ = rt.RequestAsset(tid, ref.PublicIDHex())
		_ = rt.pushPublicIngress(addr, tid)
		_, _ = owner.collectPublicIngress(addr, tid)
		_, _ = rt.PullFromRelay(addr)
		st, err := rt.AssetStatus(tid, ref.PublicIDHex())
		return err == nil && st.State == assets.StateComplete
	}
	got := false
	for i := 0; i < 40 && !got; i++ {
		got = pump(bob)
	}
	if !got {
		t.Fatal("the follower never got the picture even before a restart")
	}
	bob.Close()

	// The restart. Everything the asking machinery keeps is now gone.
	bob2 := openRuntime(t, dir, "bob")
	defer bob2.Close()
	if have, err := assets.RetrieveBytes(bob2.root, ref); err != nil || !bytes.Equal(have, content) {
		t.Fatalf("a picture already fetched did not survive the restart: %v", err)
	}

	// And a SECOND picture, published while the follower was away: this is
	// the case that was reported, and the one that needs the whole asking
	// path to rebuild itself from nothing.
	content2 := randBytes(t, 200_000)
	ref2 := emitVisual(t, owner, tid, content2, 4096)
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	got = false
	for i := 0; i < 40 && !got; i++ {
		_ = bob2.fetchPublicProjection(addr, tid)
		_ = bob2.RequestAsset(tid, ref2.PublicIDHex())
		_ = bob2.pushPublicIngress(addr, tid)
		_, _ = owner.collectPublicIngress(addr, tid)
		_, _ = bob2.PullFromRelay(addr)
		st, err := bob2.AssetStatus(tid, ref2.PublicIDHex())
		got = err == nil && st.State == assets.StateComplete
	}
	if !got {
		st, err := bob2.AssetStatus(tid, ref2.PublicIDHex())
		t.Fatalf("a restarted follower never got a picture published while it "+
			"was away: status %+v (%v)", st, err)
	}
	have, err := assets.RetrieveBytes(bob2.root, ref2)
	if err != nil || !bytes.Equal(have, content2) {
		t.Fatalf("the bytes are not the ones that were published: %v", err)
	}
}
