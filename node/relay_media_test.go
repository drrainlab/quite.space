package node

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// Relay media fetch: with NO direct link, bob pulls an image's bytes purely
// through the blind relay. Auto-sync carries the block event + manifest
// (manifests-only), then "fetch original" rides a want-request to alice, who
// answers the chunks into bob's inbox — media on-demand over the relay.
func TestRelayMediaFetch(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Media Relay")
	if err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 200_000) // 49 chunks @ 4KiB → external manifest
	ref := emitVisual(t, alice, tid, content, 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}

	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}

	// Relay-only: no LAN, no ConnectPeer.
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// The block event (with the ref) reaches bob via auto-sync; once he can
	// project a status, the ref is indexed and a fetch is possible.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := bob.AssetStatus(tid, ref.PublicIDHex()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("block event never reached bob via relay")
		}
		time.Sleep(150 * time.Millisecond)
	}

	// Fetch original over the relay — there is no direct peer to serve bytes.
	if err := bob.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	for {
		st, err := bob.AssetStatus(tid, ref.PublicIDHex())
		if err != nil {
			t.Fatal(err)
		}
		if st.State == assets.StateComplete {
			break
		}
		if st.State == assets.StateFailed {
			t.Fatalf("relay media fetch failed: %s", st.Reason)
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay media fetch never completed: %+v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}

	got, _, err := bob.RetrieveAsset(tid, ref.PublicIDHex())
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("fetched content wrong over relay: %v", err)
	}
}

// A track larger than the 8 MiB relay bundle budget must still converge: the
// holder answers up to a budget per round and the requester re-asks for only
// the tail, not the chunks it already has. Regression for the multi-round
// stall where a big asset re-requested satisfied chunks forever.
func TestRelayMediaFetchMultiRound(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Big Track")
	if err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 9<<20) // 9 MiB > 8 MiB budget → at least two rounds
	ref := emitVisual(t, alice, tid, content, 64<<10)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := bob.AssetStatus(tid, ref.PublicIDHex()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("block event never reached bob via relay")
		}
		time.Sleep(150 * time.Millisecond)
	}
	if err := bob.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	for {
		st, err := bob.AssetStatus(tid, ref.PublicIDHex())
		if err != nil {
			t.Fatal(err)
		}
		if st.State == assets.StateComplete {
			break
		}
		if st.State == assets.StateFailed {
			t.Fatalf("multi-round relay fetch failed: %s", st.Reason)
		}
		if time.Now().After(deadline) {
			t.Fatalf("multi-round relay fetch never completed: %+v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	got, _, err := bob.RetrieveAsset(tid, ref.PublicIDHex())
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("multi-round fetched content wrong: %v", err)
	}
}
