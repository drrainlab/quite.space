package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// Relay auto-sync: two LAN-less nodes converge purely through the
// background relay loop (Settings.Relay) — no manual push/pull, no direct
// link. Media stays on-demand (manifests-only push).
func TestRelayAutoSync(t *testing.T) {
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

	tid, err := alice.CreateSpace("Remote")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "reaches bob through the blind relay only", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// No LAN, no ConnectPeer — only the background relay loop on both.
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// Bob receives Alice's message (owner -> joiner).
	deadline := time.Now().Add(20 * time.Second)
	for {
		if msgCount(bob, tid) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner->joiner did not converge (status: %+v / %+v)",
				alice.RelaySync(), bob.RelaySync())
		}
		time.Sleep(200 * time.Millisecond)
	}

	// ...and Alice receives Bob's reply (joiner -> owner). The joiner's member
	// map is empty (controller-only), so this only works if the pusher also
	// addresses peers learned from received frames — regression guard for the
	// asymmetric-delivery bug where only one direction flowed.
	if _, err := bob.Say(tid, "and bob answers back the same way", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		if msgCount(alice, tid) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("joiner->owner did not converge (status: %+v / %+v)",
				alice.RelaySync(), bob.RelaySync())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st := alice.RelaySync(); !st.Active || st.Addr != addr {
		t.Fatalf("alice relay sync inactive: %+v", st)
	}

	// Turning the relay off in settings stops the loop.
	if err := alice.SetSettings(Settings{Relay: ""}); err != nil {
		t.Fatal(err)
	}
	if st := alice.RelaySync(); st.Active {
		t.Fatal("relay sync should stop when the address is cleared")
	}
}
