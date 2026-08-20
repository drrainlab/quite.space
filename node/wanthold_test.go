package node

// Phase 1 of stream 1A (plan "route honesty"): the transfer state
// machine's held state, made visible. Phase 0's table found the true
// holder of every starving asset standing at a bare `return` in
// answerWantsRouted — wanting to answer, saying nothing. These tests
// pin the replacement: no route is HELD and visible, never silent; and
// a hold clears the moment an answer is actually routed.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// TestAnUnroutableWantIsHeldOutLoud — the direct pin on the Phase-0
// cell: a want from a device with no known route produces a visible
// held record, not a silent drop, and the record says it is transient.
func TestAnUnroutableWantIsHeldOutLoud(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "holder")
	defer rt.Close()
	tid, err := rt.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}

	stranger := id.DeviceID{0xAB, 0xCD}
	rt.answerWantsRouted(tid, stranger[:], [][]byte{make([]byte, 32), make([]byte, 32)})

	holds := rt.WantHolds()
	if len(holds) != 1 {
		t.Fatalf("expected one hold, got %d", len(holds))
	}
	h := holds[0]
	if h.Wanter != stranger || h.Space != tid || h.Wants != 2 || h.Reason != "held_no_route" {
		t.Fatalf("the hold does not describe what happened: %+v", h)
	}

	// And it is VISIBLE where an operator looks, not only in a getter.
	st := rt.RelaySync()
	if len(st.WantHolds) != 1 || st.WantHolds[0].Reason != "held_no_route" {
		t.Fatalf("RelaySync does not surface the hold: %+v", st.WantHolds)
	}

	// The same starving fetch knocks every cycle; the record must not
	// scroll into sixty-four copies of one fact.
	for i := 0; i < 10; i++ {
		rt.answerWantsRouted(tid, stranger[:], [][]byte{make([]byte, 32)})
	}
	if holds = rt.WantHolds(); len(holds) != 1 {
		t.Fatalf("repeated knocks multiplied the record: %d entries", len(holds))
	}
	if holds[0].Wants != 1 {
		t.Fatalf("the refreshed hold kept stale detail: %+v", holds[0])
	}
}

// TestAHoldClearsWhenTheAnswerRoutes — once a route exists and an
// answer is actually sent toward the wanter, the "waiting for a route"
// line must go: a stale hold beside a delivered answer is its own lie.
func TestAHoldClearsWhenTheAnswerRoutes(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	rt := openRuntime(t, t.TempDir(), "holder")
	defer rt.Close()
	setPersonalRelay(t, rt, addr)
	tid, err := rt.CreateSpace("room")
	if err != nil {
		t.Fatal(err)
	}

	wanter := id.DeviceID{0x11, 0x22}
	rt.answerWantsRouted(tid, wanter[:], [][]byte{make([]byte, 32)})
	if len(rt.WantHolds()) != 1 {
		t.Fatal("no hold recorded while unroutable")
	}

	// The route arrives (any provenance stronger than nothing).
	rt.mu.Lock()
	rt.recordPeerRouteLocked(wanter, addr, "relay", storage.RouteInvitation)
	rt.mu.Unlock()

	// The next want is answerable — the hold must clear, even though the
	// wanted blobs are unknown here (routing happened; the skip-per-hash
	// inside answerWants is a different, terminal story).
	rt.answerWantsRouted(tid, wanter[:], [][]byte{make([]byte, 32)})
	deadline := time.Now().Add(5 * time.Second)
	for len(rt.WantHolds()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the hold outlived the routed answer: %+v", rt.WantHolds())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
