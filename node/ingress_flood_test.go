package node

import (
	"fmt"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// PA-1.3: a single contributor flooding an open community is THROTTLED per
// drain cycle but never LOSES content — over-cap frames stay in its durable
// pending set and arrive on later cycles. A second, quiet contributor is
// unaffected by the flood.
func TestIngressFloodThrottledNotLost(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Commons", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	loud := openRuntime(t, t.TempDir(), "loud")
	defer loud.Close()
	quiet := openRuntime(t, t.TempDir(), "quiet")
	defer quiet.Close()
	for _, c := range []*Runtime{loud, quiet} {
		if err := c.OpenPublicSpace(tid, addr); err != nil {
			t.Fatal(err)
		}
		if err := c.JoinPublicSpace(tid); err != nil {
			t.Fatal(err)
		}
	}

	const flood = ingressMaxFramesPerAuthorCycle + 20 // exceeds one cycle's cap
	for i := 0; i < flood; i++ {
		if _, err := loud.Say(tid, fmt.Sprintf("flood %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := quiet.Say(tid, "one calm word", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// First cycle: push everything, owner drains ONCE.
	if err := loud.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if err := quiet.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	osp, _ := owner.spaceForTest(tid)
	// Throttle: loud's flood is capped this cycle — NOT all of it landed.
	if got := countFlood(osp.State.Messages()); got > ingressMaxFramesPerAuthorCycle {
		t.Fatalf("first drain took %d flood frames — per-author cap not enforced", got)
	} else if got == flood {
		t.Fatal("entire flood landed in one cycle — no throttle")
	}
	// The quiet contributor's single word must be among the first drain —
	// the flood did not crowd it out.
	if !hasMessage(osp.State.Messages(), "one calm word") {
		t.Fatal("quiet contributor starved by the flood")
	}

	// Converge: publish → contributors ack → re-push leftovers → drain,
	// until the owner holds every flooded message. Bounded loop.
	for cycle := 0; cycle < 12; cycle++ {
		if err := owner.publishPublicProjection(addr, tid); err != nil {
			t.Fatal(err)
		}
		if err := loud.fetchPublicProjection(addr, tid); err != nil {
			t.Fatal(err)
		}
		if err := loud.pushPublicIngress(addr, tid); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.collectPublicIngress(addr, tid); err != nil {
			t.Fatal(err)
		}
		osp, _ = owner.spaceForTest(tid)
		if countFlood(osp.State.Messages()) == flood {
			return // no loss — every flooded message eventually landed
		}
	}
	osp, _ = owner.spaceForTest(tid)
	t.Fatalf("flood not fully delivered: %d/%d after convergence loop",
		countFlood(osp.State.Messages()), flood)
}

func hasMessage(msgs []reducers.Message, text string) bool {
	for _, m := range msgs {
		if m.Text == text {
			return true
		}
	}
	return false
}

func countFlood(msgs []reducers.Message) int {
	n := 0
	for _, m := range msgs {
		if len(m.Text) >= 6 && m.Text[:6] == "flood " {
			n++
		}
	}
	return n
}
