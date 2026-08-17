package node

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/signal"
)

// THE RELAY-PATH MEASUREMENT (MD-0).
//
// kernel/eventlog already answered half the question: at the log, a refusal
// is a delay. The frame creates no chain state, is never marked seen, does
// not fork or advance anything, and the SAME frame lands the moment the gate
// opens. So a holding area is not needed there.
//
// This asks the other half, which the log cannot answer: does the frame ever
// come BACK over a relay? The suspicion is concrete rather than vague — the
// relay's Collect is destructive ("fetches and removes"), so a frame the
// receiver pulled and then refused may be gone from the relay while the
// sender considers it handed over. If that is what happens, the refusal is a
// LOSS on this path however forgiving the log is, and the gate cannot go on
// until something replays it.
//
// One refusal, then the gate opens, then we wait. Nothing else.
func TestARefusedFrameComesBackOverTheRelay(t *testing.T) {
	// MEASURED 2026-08-13, and the answer was NO: 45 seconds, no return.
	// A frame the receiver pulled from the relay and then refused is gone —
	// Collect removed it, and the sender has no reason to send it twice. So
	// on this path a refusal is a LOSS, while at the log (see
	// kernel/eventlog/refusal_test.go) it is only a delay.
	//
	// That is why the certification gate is still off in node.Open: it would
	// silently drop a peer's first message whenever the certificate is one
	// round behind. This test is the ACCEPTANCE TEST for the fix — hold the
	// refused frame and re-judge it when the certificate arrives — and the
	// skip comes off in the commit that lands it.
	t.Skip("MD-0: refused frames are not replayed over the relay yet; this " +
		"is the acceptance test for hold-and-replay")

	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	tid, err := alice.CreateSpace("the measurement")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addr)
	if err != nil {
		t.Fatal(err)
	}

	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	// Bob refuses exactly the first ordinary frame that reaches him from
	// alice's device, then never again. This is the shape of an uncertified
	// peer whose certificate is one round behind.
	sp, ok := bob.spaceForTest(tid)
	if !ok {
		t.Fatal("bob lost the space")
	}
	var refused atomic.Bool
	refuse := errors.New("device not certified (measurement)")
	sp.Log.SetIdentityAdmit(func(env *signal.Envelope) error {
		if env.Device == alice.Device.ID && refused.CompareAndSwap(false, true) {
			return refuse
		}
		return nil
	})

	if _, err := alice.Say(tid, "does a refusal come back?", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// Give the sync loop long enough that "it never came back" is a finding
	// rather than impatience.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if countMsg(t, bob, tid, "does a refusal come back?") >= 1 {
			if !refused.Load() {
				t.Fatal("the gate never fired — the measurement proved nothing")
			}
			return // ANSWER: a refusal is a delay on the relay path too.
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !refused.Load() {
		t.Fatal("the gate never fired — the measurement proved nothing")
	}
	t.Fatal("ANSWER: a frame refused once never came back over the relay — " +
		"the refusal is a LOSS on this path, so the gate needs a replay " +
		"before it can go on")
}
