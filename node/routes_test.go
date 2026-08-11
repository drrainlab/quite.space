// The directed RouteBook (RT-0 / T1): learned from the sealed invitation
// exchange, both directions, and never from watching where frames arrive.
package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

// The exchange, end to end over a real relay: after a pass join, each side
// holds a route TO the other with invitation provenance — Alice's book says
// "Bob is at B", Bob's says "Alice is at A" — and neither table was inferred
// from its own ingress.
func TestJoinExchangeTeachesBothSides(t *testing.T) {
	alice, bob, addrA, addrB := twoRelays(t)

	tid, err := alice.CreateSpace("routes both ways")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	// The host learned the guest's ingress from the request's hint.
	waitUntil(t, 10*time.Second, "alice never learned a route to bob", func() bool {
		for _, ep := range alice.PeerRoutesFor(bob.Device.ID) {
			if ep == addrB {
				return true
			}
		}
		return false
	})
	// The guest learned the host's ingress from the acceptance's hint.
	waitUntil(t, 10*time.Second, "bob never learned a route to alice", func() bool {
		for _, ep := range bob.PeerRoutesFor(alice.Device.ID) {
			if ep == addrA {
				return true
			}
		}
		return false
	})

	// Provenance is invitation, and it survives a restart — the whole point
	// of putting the book in the keystore rather than beside it.
	alice.mu.Lock()
	routes := alice.ks.PeerRoutes[bob.Device.ID]
	alice.mu.Unlock()
	if len(routes) == 0 || routes[0].Provenance != storage.RouteInvitation {
		t.Fatalf("host's learned route has the wrong provenance: %+v", routes)
	}
}

// THE INVERSION BAN. A frame arriving through an endpoint proves that WE
// were reachable there; it must never mint a peer route. The book after a
// pure receive is exactly the book before it.
func TestArrivalNeverMintsAPeerRoute(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	// Same relay on both sides — the single-relay world, where delivery
	// works without any route book at all.
	setPersonalRelay(t, alice, addr)
	setPersonalRelay(t, bob, addr)

	tid, err := alice.CreateSpace("watch the book")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(inv); err != nil {
		t.Fatal(err)
	}

	// Bob authors; his frames reach alice through the shared relay.
	if _, err := bob.Say(tid, "a frame from bob", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "bob's frame never arrived", func() bool {
		return countMsg(t, alice, tid, "a frame from bob") >= 1
	})

	// Arrival taught alice NOTHING about where bob listens. The one entry
	// she may hold is her OWN recorded assumption — the legacy bootstrap her
	// delivery decision wrote down, labeled for what it is — never a route
	// with a provenance that claims bob stated it.
	alice.mu.Lock()
	routes := append([]storage.Route(nil), alice.ks.PeerRoutes[bob.Device.ID]...)
	alice.mu.Unlock()
	for _, rt := range routes {
		if rt.Provenance != storage.RouteLegacy {
			t.Fatalf("an arrival minted a %d-provenance route: %+v", rt.Provenance, rt)
		}
	}
}

// Advertising an endpoint creates the obligation to keep listening there:
// it lands in SelfIngress in the same breath as the hint.
func TestAdvertisingRecordsTheObligationToListen(t *testing.T) {
	alice, bob, addrA, addrB := twoRelays(t)

	tid, err := alice.CreateSpace("obligations")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	has := func(rt *Runtime, ep string) bool {
		for _, e := range rt.SelfIngressRoutes() {
			if e == ep {
				return true
			}
		}
		return false
	}
	if !has(bob, addrB) {
		t.Error("bob advertised B and does not list it as ingress")
	}
	waitUntil(t, 10*time.Second, "alice advertised A and does not list it as ingress", func() bool {
		return has(alice, addrA)
	})
}

// THE COMPATIBILITY CONTEXT STAYS A CONTEXT. A sync cycle armed at an
// explicit address (relaySyncOnce, manual sync, tests — the legacy
// single-address API) may use that address as its execution context: pull
// there, carry the legacy batch there. It must NOT become knowledge: no
// PeerRoutes entry, no SelfIngress entry, nothing that survives the cycle
// or comes back out of the general resolver.
func TestExplicitSyncEndpointDoesNotBecomePeerReachability(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	// Bob has NO settings relay and the registry is blank: every address he
	// touches below arrives as an explicit cycle parameter, never as his own.
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, alice, addr)

	tid, err := alice.CreateSpace("контекст, не знание")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(inv); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(tid, "через контекст цикла", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// Bob learns alice's device from her authored frames, then runs ONE
	// explicitly-addressed cycle. The cycle works — that is the adapter's
	// job — and alice receives.
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	// Arm the sync state, then stop the loop, so the explicit cycle below is
	// the only one that runs (the held-report test's pattern).
	bob.applyRelaySync(addr, 0)
	bob.applyRelaySync("", 0)
	bob.relaySyncOnce(addr)
	if _, err := alice.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if countMsg(t, alice, tid, "через контекст цикла") < 1 {
		t.Fatal("the explicitly-addressed cycle did not carry the frame")
	}

	// And it taught the RouteBook NOTHING.
	bob.mu.Lock()
	routes := append([]storage.Route(nil), bob.ks.PeerRoutes[alice.Device.ID]...)
	bob.mu.Unlock()
	if len(routes) != 0 {
		t.Fatalf("an explicit cycle endpoint minted peer routes: %+v", routes)
	}
	for _, ep := range bob.SelfIngressRoutes() {
		if ep == addr {
			t.Fatalf("an explicit cycle endpoint became a standing ingress: %q", ep)
		}
	}
}

// THE PRE-T4 MIGRATION FENCE. Changing my personal relay must never retire
// an ingress route already shared with an established peer: Bob still only
// knows "Alice is at A", and until signed reachability (T3) and
// make-before-break (T4) exist there is no way to tell him otherwise. So A
// stays in Alice's listen loop, and the conversation survives her move.
func TestPreT4NeverRetiresEstablishedIngress(t *testing.T) {
	alice, bob, addrA, _ := twoRelays(t)
	srvC, addrC := startRelay(t)
	defer srvC.Close()

	tid, err := alice.CreateSpace("before the move")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	// Alice's personal relay moves. Selection quality, not reachability.
	setPersonalRelay(t, alice, addrC)

	// The advertised ingress survives the move…
	stillListens := false
	for _, ep := range alice.SelfIngressRoutes() {
		if ep == addrA {
			stillListens = true
		}
	}
	if !stillListens {
		t.Fatal("moving the personal relay retired an ingress an established peer still uses")
	}

	// …and so does the conversation: bob delivers to A, alice still reads A.
	if _, err := bob.Say(tid, "still here?", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "the move stranded the conversation", func() bool {
		return countMsg(t, alice, tid, "still here?") >= 1
	})
}
