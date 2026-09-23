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
// were reachable there; the arrival itself must never mint a peer route.
// What a receive MAY add is what the push carried on purpose: the sender's
// stated return address (bundle key 8) — his statement, not our inference.
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

	// Arrival taught alice NOTHING about where bob listens. What she may
	// hold: her OWN recorded assumption — the legacy bootstrap her delivery
	// decision wrote down, labeled for what it is — and bob's own stated
	// return address (bundle key 8, RouteAdvertised), which is not an
	// inference from ingress but a statement RIDING the push, cert-gated at
	// the receiver. Whether that statement lands here is a timing accident
	// (the cert gate closes on the bundle that carries the cert itself; a
	// re-push under load records), so it is allowed, not required — but ONLY
	// at an endpoint bob actually advertises. Anything else — an invitation
	// provenance nobody's exchange minted, or an advertised route at an
	// endpoint bob never stated — is the inversion this test bans.
	bobIngress := map[string]bool{}
	for _, ep := range bob.SelfIngressRoutes() {
		bobIngress[ep] = true
	}
	alice.mu.Lock()
	routes := append([]storage.Route(nil), alice.ks.PeerRoutes[bob.Device.ID]...)
	alice.mu.Unlock()
	for _, rt := range routes {
		if rt.Provenance == storage.RouteLegacy {
			continue
		}
		if rt.Provenance == storage.RouteAdvertised && rt.Transport == "relay" && bobIngress[rt.Endpoint] {
			continue // bob's own cert-gated self-statement, at an address he owns
		}
		t.Fatalf("an arrival minted a %d-provenance route: %+v", rt.Provenance, rt)
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

	// And the RouteBook holds nothing DERIVED. Since content pushes began
	// announcing their sender's ingress, bob may hold alice's own stated
	// routes — that is her claim, attributed and cert-gated, and it is
	// exactly what the announce exists to spread. What the explicit cycle
	// must still never do is mint a route out of the wire itself: every
	// entry bob holds for alice must be her statement (advertised), never
	// an invitation he did not receive, an observation he did not make,
	// or a guess written down as knowledge.
	bob.mu.Lock()
	routes := append([]storage.Route(nil), bob.ks.PeerRoutes[alice.Device.ID]...)
	bob.mu.Unlock()
	for _, rt := range routes {
		if rt.Provenance != storage.RouteAdvertised {
			t.Fatalf("an explicit cycle endpoint minted a non-stated peer route: %+v", rt)
		}
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

func TestAPeersLoopbackRouteIsNotDialledFromAnotherMachine(t *testing.T) {
	cases := []struct {
		ep, own string
		want    bool
	}{
		{"127.0.0.1:7411", "178.20.45.239:7411", false}, // a stand's stale loopback, seen from a phone
		{"localhost:7411", "178.20.45.239:7411", false},
		{"0.0.0.0:7411", "178.20.45.239:7411", false},
		{"[::1]:7411", "178.20.45.239:7411", false},
		{"127.0.0.1:7411", "127.0.0.1:7412", true}, // the test bench: everybody on one machine
		{"127.0.0.1:7411", "", true},               // no own relay yet: dial what was said
		{"178.20.45.239:7411", "127.0.0.1:7412", true},
		{"", "178.20.45.239:7411", false},
	}
	for _, c := range cases {
		if got := routableFrom(c.ep, c.own); got != c.want {
			t.Errorf("routableFrom(%q, own=%q) = %v, want %v", c.ep, c.own, got, c.want)
		}
	}
}

// LT-4 S2. A receipt goes where a message would: the author's best DIALABLE
// stated route, not whatever sits first in the raw book. Bob's first stated
// route (A) is a relay alice's pool has marked untrusted; the receipt must
// land at his second (B). Alice herself lives on C by then.
func TestReceiptsUseTheDialableRoute(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	defer srvB.Close()
	srvC, addrC := startRelay(t)
	defer srvC.Close()
	alice, bob, tid := pairOnRelay(t, addrA)
	defer alice.Close()
	defer bob.Close()
	setPersonalRelay(t, alice, addrC)

	// A word from bob that alice folds: alice owes him a receipt.
	if _, err := bob.Say(tid, "квитанцию сюда", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addrC, "bob": addrA}
	waitUntil(t, 30*time.Second, "alice never received bob's word", func() bool {
		convergeTick(nodes, addrs)
		return countMsg(t, alice, tid, "квитанцию сюда") >= 1
	})

	// Bob states B as well; A is dead to alice's pool from here on.
	alice.mu.Lock()
	alice.recordPeerRouteLocked(bob.Device.ID, addrB, "relay", storage.RouteAdvertised)
	alice.mu.Unlock()
	pe := alice.pool().peer(addrA)
	pe.mu.Lock()
	pe.untrusted = true
	pe.mu.Unlock()
	defer func() { pe.mu.Lock(); pe.untrusted = false; pe.mu.Unlock() }()

	ep, guessed := alice.courtesyRoute(bob.Device.ID)
	if guessed || ep != addrB {
		t.Fatalf("courtesyRoute = %q guessed=%v, want bob's dialable second route %q", ep, guessed, addrB)
	}
	before := mailboxCount(t, addrB, tid, bob.Device.ID)
	alice.mu.Lock()
	alice.receipts = nil // forget what was receipted; the next pass owes one
	alice.mu.Unlock()
	alice.sendReceipts()
	if got := mailboxCount(t, addrB, tid, bob.Device.ID); got <= before {
		t.Fatalf("the receipt did not land at bob's dialable route B (mailbox %d → %d)", before, got)
	}
}

// LT-4 S2. A stated return route that nobody could dial from here is not
// written into the book — and a statement made only of such routes does
// not erase the routes already known. (A real, certified peer: a claim
// from a device nobody's root has named is not knowledge at all.)
func TestAStatedLoopbackRouteIsNotRecordedOffTheBench(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, _ := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	// Off the bench: alice's own relay is a public address (never dialled here).
	if err := alice.SetSettings(Settings{Relay: "203.0.113.9:7411"}); err != nil {
		t.Fatal(err)
	}
	raw := func() []string {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		var out []string
		for _, rt := range alice.ks.PeerRoutes[bob.Device.ID] {
			out = append(out, rt.Endpoint)
		}
		return out
	}
	before := raw()
	alice.recordStatedReturnRoutes(bob.Device.ID[:], []string{"127.0.0.1:7411", "0.0.0.0:7411", "198.51.100.5:0"})
	if after := raw(); len(after) != len(before) {
		t.Fatalf("a statement of unroutable addresses changed the book: %v → %v", before, after)
	}
	alice.recordStatedReturnRoutes(bob.Device.ID[:], []string{"127.0.0.1:7411", "198.51.100.6:7411"})
	after := raw()
	sawReal := false
	for _, ep := range after {
		if ep == "127.0.0.1:7411" {
			t.Fatalf("a loopback address entered the book beside a real one: %v", after)
		}
		if ep == "198.51.100.6:7411" {
			sawReal = true
		}
	}
	if !sawReal {
		t.Fatalf("the real address of the statement was not recorded: %v", after)
	}
}

// LT-4 S2. This node's own loopback past is not advertised to peers who
// cannot dial it — unless this node itself lives on a loopback relay (the
// bench), where everybody shares one machine.
func TestALoopbackIngressIsNotAdvertisedOffTheBench(t *testing.T) {
	in := []string{"203.0.113.9:7411", "127.0.0.1:7411", "[::1]:7411", "203.0.113.10:7411"}
	got := advertisable(in, "203.0.113.9:7411")
	if len(got) != 2 || got[0] != "203.0.113.9:7411" || got[1] != "203.0.113.10:7411" {
		t.Fatalf("advertisable off the bench = %v", got)
	}
	bench := advertisable(in, "127.0.0.1:7412")
	if len(bench) != 4 {
		t.Fatalf("on the bench every ingress is advertisable, got %v", bench)
	}
	for _, c := range []struct {
		ep   string
		want bool
	}{
		{"198.51.100.4:7411", true}, {"198.51.100.4:0", false}, {"169.254.1.2:7411", false},
		{"224.0.0.1:7411", false}, {"127.0.0.1:7411", false}, {"nohost", false},
	} {
		if got := statableEndpoint(c.ep, "203.0.113.9:7411"); got != c.want {
			t.Errorf("statableEndpoint(%q) = %v, want %v", c.ep, got, c.want)
		}
	}
}
