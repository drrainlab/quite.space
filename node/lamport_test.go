// The Lamport clock has to count what this replica has SEEN, not only what it
// has said.
//
// FOUND ALONGSIDE THE PRESENCE BUG, on the same two devices. The phone had
// absorbed events with clocks 36, 37, 38 and was still stamping its own
// messages 11, 12 — every one of them "before" everything the other side had
// already said. Nothing is lost by that, but ordering is decided by
// (created_at, logical_clock, id), and a clock that never advances turns the
// tie-break into noise: two devices editing the same card, or racing a
// reaction, resolve by a number that has stopped meaning anything.
//
// The cause was that Participant.ObserveClock is called on ResumeChain — a
// restart — and nowhere on the live sync path, so a running node never
// learned anything from what it received.
package node

import (
	"fmt"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestOurClockCountsWhatWeHaveSeen(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	bob := openRuntime(t, t.TempDir(), "bob")

	// Bob has a life of his own first, so his clock is genuinely ahead —
	// this is the ordinary case, not a contrivance: any peer who has been
	// running longer arrives with a higher clock.
	elsewhere, err := bob.CreateSpace("bob's own room")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := bob.Say(elsewhere, fmt.Sprintf("note %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	tid, err := alice.CreateSpace("the line")
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
	// Alice's manifest and the epoch reach bob first, then bob speaks.
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(tid, "from a node that has been up a while", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}

	clockOf := func(rt *Runtime, want string) uint64 {
		t.Helper()
		s, ok := rt.spaceForTest(tid)
		if !ok {
			t.Fatal("no replica")
		}
		for _, m := range s.State.Messages() {
			if m.Text == want {
				return m.Clock
			}
		}
		t.Fatalf("message %q not found", want)
		return 0
	}

	seen := clockOf(alice, "from a node that has been up a while")
	if seen == 0 {
		t.Fatal("alice never received bob's message")
	}

	// The whole property: what she says next happened AFTER what she read.
	if _, err := alice.Say(tid, "and this is my answer", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	mine := clockOf(alice, "and this is my answer")
	if mine <= seen {
		t.Fatalf("alice answered a message she had already absorbed with a "+
			"clock at or behind it: hers %d, his %d — a reply that sorts "+
			"before the thing it replies to", mine, seen)
	}

	// And bob agrees once he has it: the same event, the same number.
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if got := clockOf(bob, "and this is my answer"); got != mine {
		t.Fatalf("the clock is not the event's: alice %d, bob %d", mine, got)
	}
	_ = id.TerminalID{}
}
