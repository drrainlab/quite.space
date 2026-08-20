package node

// The deterministic causal chain for stream 1A (owner's t0…t9). This
// file carries the HONESTY half — t1..t3: a bootstrap guess is never
// recorded and never final; the exact pending item re-offers to a real
// route the moment one is stated; displacement deletes recorded guesses.
// The CONVERGENCE half (t4..t9: freight routes, bundle key 8, the same
// want retried and answered) joins this file with commit 2.
//
// Everything is driven by hand — one relaySyncOnce per step, no timers
// to race — and every relay poke stays far under the shipped
// 240-collects-a-minute budget.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// TestABootstrapGuessIsNeverRecordedAndNeverFinal — t1 and t2.
func TestABootstrapGuessIsNeverRecordedAndNeverFinal(t *testing.T) {
	srvA, addrA := startRelay(t) // the holder's own relay (the guess target)
	defer srvA.Close()
	srvB, addrB := startRelay(t) // where the recipient actually listens
	defer srvB.Close()

	holder := openRuntime(t, t.TempDir(), "holder")
	defer holder.Close()
	setPersonalRelay(t, holder, addrA)
	tid, err := holder.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}
	// A recipient the holder KNOWS (a member device) but has no route to:
	// the direct-invite shape.
	guest := openRuntime(t, t.TempDir(), "guest")
	defer guest.Close()
	setPersonalRelay(t, guest, addrB)
	// The guest pulls BY HAND in this test. Its background loop polls
	// addrB every test-cadence tick, and Collect is destructive — left
	// running, it drains the very item t3 asserts is pending at the
	// stated relay, a coin-flip lost more often the busier the machine.
	// The flake read as a product bug for an afternoon; the product had
	// in fact delivered end-to-end, which is why every instrument on the
	// holder came back clean.
	guest.applyRelaySync("", 0)
	invite, err := holder.MintInvite(tid, guest.Device.ID, guest.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Say(tid, "письмо", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// t1 — the zero-knowledge push. The put IS made (at the holder's own
	// relay: the tentative courtesy), the route book is NOT written, the
	// cursor does NOT move, and the hold says "guess" out loud.
	holder.applyRelaySync(addrA, 0)
	holder.applyRelaySync("", 0) // arm, then stop the background loop
	holder.relaySyncOnce(addrA)

	if n := srvA.Pending(); n == 0 {
		t.Fatal("t1: the tentative put was not made at the holder's own relay")
	}
	if eps := holder.PeerRoutesFor(guest.Device.ID); len(eps) != 0 {
		t.Fatalf("t1: the guess was recorded as knowledge: %v", eps)
	}
	st := holder.RelaySync()
	if len(st.Held) != 1 || st.Held[0].Reason != heldTentative {
		t.Fatalf("t1: delivery on a guess is not held as a guess: %+v", st.Held)
	}

	// t2 — the next cycle re-offers (the cursor never moved), and the
	// relay's byte-identical dedup absorbs the repeat: pending must not
	// grow cycle over cycle.
	before := srvA.Pending()
	time.Sleep(700 * time.Millisecond)
	holder.relaySyncOnce(addrA)
	if after := srvA.Pending(); after != before {
		t.Fatalf("t2: the re-offer was not deduped: %d -> %d items", before, after)
	}

	// t3 — a stated route arrives; the SAME pending frames go to the
	// right relay this cycle, the hold clears, the cursor advances.
	holder.mu.Lock()
	holder.recordPeerRouteLocked(guest.Device.ID, addrB, "relay", storage.RouteInvitation)
	holder.mu.Unlock()
	time.Sleep(700 * time.Millisecond)
	holder.relaySyncOnce(addrA)

	if n := srvB.Pending(); n == 0 {
		t.Fatal("t3: the pending item never reached the stated relay")
	}
	if held := holder.RelaySync().Held; len(held) != 0 {
		t.Fatalf("t3: still held after a routed delivery: %+v", held)
	}
	// The guest can now actually read it — the point of all of this.
	if _, err := guest.PullFromRelay(addrB); err != nil {
		t.Fatal(err)
	}
	if n := countMsg(t, guest, tid, "письмо"); n != 1 {
		t.Fatalf("t3: the guest holds %d copies, want exactly 1", n)
	}
}

// TestAStatedRouteDisplacesEveryRecordedGuess — the poison cleanup.
// Production books already hold wrong RouteLegacy entries minted by the
// old bootstrap; the moment anything stated arrives for a device, the
// guesses must be DELETED — not merely outranked, or the health filter
// falls back to a healthy-but-wrong relay whenever the stated one
// blinks.
func TestAStatedRouteDisplacesEveryRecordedGuess(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "node")
	defer rt.Close()
	dev := id.DeviceID{0x77}

	rt.mu.Lock()
	rt.recordPeerRouteLocked(dev, "10.0.0.1:7411", "relay", storage.RouteLegacy)
	rt.recordPeerRouteLocked(dev, "10.0.0.2:7411", "relay", storage.RouteLegacy)
	gen0 := rt.routeKnowledgeGen
	rt.recordPeerRouteLocked(dev, "203.0.113.9:7411", "relay", storage.RouteInvitation)
	gen1 := rt.routeKnowledgeGen
	routes := append([]storage.Route(nil), rt.ks.PeerRoutes[dev]...)
	rt.mu.Unlock()

	if len(routes) != 1 || routes[0].Provenance != storage.RouteInvitation {
		t.Fatalf("the guesses survived a statement: %+v", routes)
	}
	if gen1 == gen0 {
		t.Fatal("the knowledge generation did not tick — legacy-basis spaces would never re-offer")
	}
}

// TestStrongerKnowledgeReoffersALegacyBasisDelivery — the invalidation
// hook end to end: a delivery whose cursor advanced on a RECORDED
// legacy route (the open-time backfill shape) is re-offered from zero
// when stated knowledge arrives, and the re-offer lands at the stated
// relay.
func TestStrongerKnowledgeReoffersALegacyBasisDelivery(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	defer srvB.Close()

	holder := openRuntime(t, t.TempDir(), "holder")
	defer holder.Close()
	setPersonalRelay(t, holder, addrA)
	tid, err := holder.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}
	guest := openRuntime(t, t.TempDir(), "guest")
	defer guest.Close()
	invite, err := holder.MintInvite(tid, guest.Device.ID, guest.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Say(tid, "на старом знании", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// A RECORDED legacy route (what a pre-RT0 backfill leaves behind).
	holder.mu.Lock()
	holder.recordPeerRouteLocked(guest.Device.ID, addrA, "relay", storage.RouteLegacy)
	holder.mu.Unlock()

	holder.applyRelaySync(addrA, 0)
	holder.applyRelaySync("", 0)
	holder.relaySyncOnce(addrA)
	// Delivered on the recorded assumption: the cursor ADVANCES (a
	// pre-RT0 install must not re-push its whole log forever) and the
	// basis is remembered rather than shown as held.
	if held := holder.RelaySync().Held; len(held) != 0 {
		t.Fatalf("a recorded-legacy delivery should advance, not hold: %+v", held)
	}

	// Stated knowledge arrives → the same frames re-offer to the stated
	// relay, unprompted by any new message.
	holder.mu.Lock()
	holder.recordPeerRouteLocked(guest.Device.ID, addrB, "relay", storage.RouteInvitation)
	holder.mu.Unlock()
	time.Sleep(700 * time.Millisecond)
	holder.relaySyncOnce(addrA)
	if n := srvB.Pending(); n == 0 {
		t.Fatal("the legacy-basis delivery was never re-offered to the stated relay")
	}
}
