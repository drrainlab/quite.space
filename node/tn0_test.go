package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// TN-0: presence is NoCustody + header-expiring — the relay push custody
// filter excludes it, while ordinary messages ride the bundle.
func TestRelayPushCustodyFilter(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Field")
	if err != nil {
		t.Fatal(err)
	}
	baseline, _, err := rt.PushToRelay(addr, tid)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := rt.Say(tid, "custody-worthy note", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetPresence(tid, "mixing_a_track", 300); err != nil {
		t.Fatal(err)
	}
	pushed, _, err := rt.PushToRelay(addr, tid)
	if err != nil {
		t.Fatal(err)
	}
	// One new custody-worthy frame (the message); presence must be filtered.
	if pushed != baseline+1 {
		t.Fatalf("custody filter wrong: baseline %d, now %d (presence leaked?)",
			baseline, pushed)
	}
}

// TN-0: the delivery ladder climbs honestly — created_local for own events
// always; queued once a transport is live; handed_to_transport after a
// sync send; never destination proof without a signed receipt.
func TestDeliveryLadderHonestClimb(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Ladder")
	if err != nil {
		t.Fatal(err)
	}
	// No transport yet: created_local only.
	eid0, err := alice.Say(tid, "before transports", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d := deliveryStatus(alice, tid, eid0); d.Level != claims.DeliveryCreatedLocal {
		t.Fatalf("expected created_local, got %v", d.Level)
	}

	// Bring up LAN + peer; new event reaches handed_to_transport and the
	// carried-by projection names the link.
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if err := alice.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := bob.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := bob.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", alice.LAN().Port)); err != nil {
		t.Fatal(err)
	}

	eid, err := alice.Say(tid, "over the wire", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		d := deliveryStatus(alice, tid, eid)
		if d.Level >= claims.DeliveryHandedToTransport {
			// Transport custody only — never destination proof (ADR-007).
			if d.Level.RequiresDestinationProof() {
				t.Fatalf("overclaim: %v without signed receipt", d.Level)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ladder stuck at %v", d.Level)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if links := alice.CarriedBy(eid); len(links) == 0 || links[0] != "lan" {
		t.Fatalf("carried-by projection wrong: %v", links)
	}
}
