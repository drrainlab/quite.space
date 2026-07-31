// RR-6: failover and the precise convergence invariant. A relay ACK
// means "accepted by transient relay memory", never durability — so the
// test is NOT "the relay kept it": it is "an event the relay accepted
// but nobody received survives the relay's death, because the CLIENT
// re-publishes it to the survivor and the receiver gets exactly one
// copy".
package node

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
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

func TestRelayOutageDoesNotLoseAcceptedEvents(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	r1, port1, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	addr1 := fmt.Sprintf("127.0.0.1:%d", port1)
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

	// Bob stops listening; alice says something R1 ACKS but nobody pulls.
	setRelay(bob, "")
	if _, err := alice.Say(dyad, "второе — R1 его заACKал", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// Let alice's loop push it to R1 (the ACK advances her push progress).
	time.Sleep(4 * time.Second)

	// R1 dies with the frame in its transient memory. A SECOND relay
	// comes up; both sides move to it.
	r1.Close()
	r2, port2, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	addr2 := fmt.Sprintf("127.0.0.1:%d", port2)
	setRelay(alice, addr2) // applyRelaySync resets push progress (RR-6)
	setRelay(bob, addr2)

	// The invariant: the accepted-but-undelivered event reaches bob via
	// R2 — the client republished, the log deduplicated.
	waitForText(t, bob, dyad, "второе")
	if n := countMsg(t, bob, dyad, "второе"); n != 1 {
		t.Fatalf("the republished event arrived %d times", n)
	}
	if n := countMsg(t, bob, dyad, "первое"); n != 1 {
		t.Fatalf("history duplicated on failover: %d copies", n)
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
