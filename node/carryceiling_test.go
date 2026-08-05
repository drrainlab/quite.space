// One ceiling, asked of the carrier — never two constants that disagree.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/routing"
)

// The contradiction this exists to prevent, stated as a test.
//
// The ledger used to answer from BetaOutboundCap (1536) while the radio
// derives its own from its own airtime (2586 on the profile the boards run).
// A 2 KiB preview sits BETWEEN those two numbers, so the delivery view would
// have told somebody their message was "waiting for a faster link" while sync
// carried it perfectly well. A status that contradicts what happened is worse
// than no status: it teaches people to stop reading it.
func TestOneCeilingRatherThanTwoThatDisagree(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	// With no radio attached there is nothing to ask, and the constant is the
	// only honest answer left.
	if got := rt.radioCarryCeiling(); got != routing.BetaOutboundCap {
		t.Fatalf("with no radio the ceiling is %d, want the floor %d",
			got, routing.BetaOutboundCap)
	}

	// And the eligibility answer is derived from THAT number rather than from
	// a second copy of it — checked by moving the number and watching the
	// answer move with it, which a duplicated constant could not do.
	in := DeliveryIntent{Size: routing.BetaOutboundCap + 1}
	if in.blockedOn(TransportRadio, rt.radioCarryCeiling()) != BlockTooLarge {
		t.Fatal("an event over the ceiling was not blocked on the radio")
	}
	if in.blockedOn(TransportRadio, routing.BetaOutboundCap*4) != BlockNone {
		t.Fatal("raising the ceiling did not change the answer — the size " +
			"test is reading a constant of its own somewhere")
	}
	// A carrier that declares no ceiling forbids nothing, which is every
	// transport but the radio and must stay that way.
	if in.blockedOn(TransportRelay, rt.radioCarryCeiling()) != BlockNone {
		t.Fatal("the relay started refusing things by size")
	}
}
