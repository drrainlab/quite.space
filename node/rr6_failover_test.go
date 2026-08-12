// RR-6 under RT-0: the outage contract, rewritten on purpose.
//
// The test that lived here assumed the shared-relay world — "everyone
// follows the relay, so when it dies, everyone moves to the same new one
// and the client republishes there". RT-0 CANCELS that world: a peer's
// reachability is what the peer STATED, and from "we both listened on A,
// A died, I moved to C" it does not follow that the peer is on C. The old
// test was superseded, not broken: it protected an architectural
// assumption we have deliberately declared wrong.
//
// The pre-T5 contract is: a stated route that died is NO_ROUTE — a hold
// with a name — and delivery resumes when the STATED route itself
// recovers. "A dies → peer's advertised fallback B" is T5's test;
// "A dies → guess my own current relay" is nobody's.
package node

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func countMsg(t *testing.T, rt *Runtime, tid id.TerminalID, text string) int {
	t.Helper()
	n := 0
	for _, s := range textsOf(t, rt, tid) {
		if strings.Contains(s, text) {
			n++
		}
	}
	return n
}

// THE DEATH TEST (owner's name for it). Alice and Bob happened to share
// relay A; the pass join taught alice "bob stated A" with invitation
// provenance. A dies and alice moves her OWN relay to C. A peer route
// coinciding with my ingress is not provenance: the result must be
// NO_ROUTE — held, said out loud, nothing minted at C — and when the
// stated route itself comes back, delivery resumes, still without a guess.
func TestInvitationRouteNeverFallsBackToMyOwnRelay(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	r1, port1, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	addr1 := fmt.Sprintf("127.0.0.1:%d", port1)
	rC, portC, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer rC.Close()
	addrC := fmt.Sprintf("127.0.0.1:%d", portC)
	setRelay := func(rt *Runtime, addr string) {
		s := rt.GetSettings()
		s.Relay = addr
		if err := rt.SetSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	setRelay(alice, addr1)
	setRelay(bob, addr1)
	dyad := shareTogether(t, alice, bob, addr1, "alice и bob")
	if _, err := alice.Say(dyad, "первое", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForText(t, bob, dyad, "первое")

	// Bob goes quiet, relay A dies, and alice's own relay moves to C.
	setRelay(bob, "")
	r1.Close()
	setRelay(alice, addrC)

	if _, err := alice.Say(dyad, "второе — сказано в разрыв", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// The hold surfaces with a route-shaped reason once the breaker calls
	// A offline — never a delivery at C on alice's own say-so.
	waitUntil(t, 45*time.Second, "a dead stated route was not reported as held", func() bool {
		for _, h := range alice.RelaySync().Held {
			if h.SpaceID == dyad.Hex() && strings.Contains(h.Reason, "route") {
				return true
			}
		}
		return false
	})
	alice.mu.Lock()
	routes := append([]storage.Route(nil), alice.ks.PeerRoutes[bob.Device.ID]...)
	alice.mu.Unlock()
	for _, rt := range routes {
		if rt.Endpoint == addrC {
			t.Fatalf("the outage minted a route at alice's OWN relay: %+v", rt)
		}
		if rt.Provenance != storage.RouteInvitation {
			t.Fatalf("the outage rewrote provenance: %+v", rt)
		}
	}

	// The stated route recovers — same address, empty relay memory. Alice
	// still pulls A (advertised ingress, pre-T4 fence), the breaker heals on
	// the first successful touch, and the held frames go where bob SAID he
	// listens. Bob resumes at A and receives everything exactly once.
	r1b, _, err := relayserver.StartServer(addr1, relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r1b.Close()
	setRelay(bob, addr1)

	waitUntil(t, 90*time.Second, "delivery never resumed on the recovered stated route", func() bool {
		return countMsg(t, bob, dyad, "второе") >= 1
	})
	if n := countMsg(t, bob, dyad, "второе"); n != 1 {
		t.Fatalf("the held event arrived %d times", n)
	}
	if n := countMsg(t, bob, dyad, "первое"); n != 1 {
		t.Fatalf("history duplicated across the outage: %d copies", n)
	}
}

// THE FAILURE SCHEDULE IS FOR "NOBODY ANSWERED", NOT "SOMEBODY DIED". The
// pre-T4 fence keeps every once-advertised endpoint in the pull loop, so a
// permanently dead historical ingress is normal life until T4 retires it —
// its dials belong to the pool's per-endpoint breaker, and the healthy
// ingress must keep its normal cadence. One dead address slowing the whole
// loop ×16 is the property T4/T5 would inherit; this pins it out.
func TestDeadHistoricalIngressDoesNotBackoffHealthyIngress(t *testing.T) {
	srvB, addrB := startRelay(t)
	defer srvB.Close()
	// A real port that refuses connections: born live, killed at once.
	srvDead, addrDead := startRelay(t)
	srvDead.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addrB)
	// A space to sync — a pull with nothing to pull for never dials.
	if _, err := alice.CreateSpace("cadence"); err != nil {
		t.Fatal(err)
	}
	alice.mu.Lock()
	alice.recordSelfIngressLocked(addrDead, storage.RouteAdvertised)
	alice.mu.Unlock()

	// Arm the sync state, stop the loop, drive the cycles by hand.
	alice.applyRelaySync(addrB, 0)
	alice.applyRelaySync("", 0)
	for i := 0; i < 5; i++ {
		alice.relaySyncOnce(addrB)
	}

	rs := alice.relaySync
	rs.mu.Lock()
	streak, retry, lastErr := rs.failStreak, rs.nextRetry, rs.lastErr
	rs.mu.Unlock()
	if streak != 0 || !retry.IsZero() {
		t.Fatalf("a dead HISTORICAL ingress engaged the global failure schedule: streak=%d retry=%v", streak, retry)
	}
	// The dead endpoint was attempted, reported honestly, and absorbed by
	// its own breaker — independence, not silence.
	if lastErr == "" {
		t.Fatal("the dead ingress was never attempted or its failure was hidden")
	}
	if h := alice.pool().health(addrDead); h == "healthy" {
		t.Fatalf("the dead ingress never reached its own breaker: health=%q", h)
	}
}

// The symmetric half: when EVERY ingress is unreachable, the global failure
// schedule is exactly what should engage.
func TestGlobalBackoffOnlyWhenAllIngressEndpointsFail(t *testing.T) {
	srvA, addrA := startRelay(t)
	srvA.Close()
	srvB, addrB := startRelay(t)
	srvB.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addrA)
	if _, err := alice.CreateSpace("dark everywhere"); err != nil {
		t.Fatal(err)
	}
	alice.mu.Lock()
	alice.recordSelfIngressLocked(addrB, storage.RouteAdvertised)
	alice.mu.Unlock()

	alice.applyRelaySync(addrA, 0)
	alice.applyRelaySync("", 0)
	for i := 0; i < 3; i++ {
		alice.relaySyncOnce(addrA)
	}

	rs := alice.relaySync
	rs.mu.Lock()
	streak, retry := rs.failStreak, rs.nextRetry
	rs.mu.Unlock()
	if streak == 0 || retry.IsZero() {
		t.Fatalf("every ingress failed and the failure schedule never engaged: streak=%d retry=%v", streak, retry)
	}
}

func TestPersonalLadderFailsOverAndHoldsOnUntrusted(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "ladder")
	defer r.Close()

	// Custom mode: single rung; unhealthy → hold (empty), not substitute.
	s := r.GetSettings()
	s.Relay = "203.0.113.77:7411"
	if err := r.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	pe := r.pool().peer("203.0.113.77:7411")
	for i := 0; i < poolUnhealthyAfter; i++ {
		r.pool().noteFailure(pe, fmt.Errorf("dial tcp: connection refused"))
	}
	if got := r.ResolvePersonalRelay(); got != "" {
		t.Fatalf("an offline custom relay still resolved: %q", got)
	}
	// untrusted is a latch: same hold, and health names it.
	r.pool().resetTrust("203.0.113.77:7411")
	r.pool().noteFailure(pe, ErrRelayUntrusted{Endpoint: "203.0.113.77:7411"})
	if got := r.ResolvePersonalRelay(); got != "" {
		t.Fatalf("an untrusted relay resolved: %q", got)
	}
	if h := r.pool().health("203.0.113.77:7411"); h != "untrusted" {
		t.Fatalf("health %q", h)
	}
}
