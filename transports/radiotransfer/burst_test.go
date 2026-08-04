// The burst protocol: an explicit turnaround instead of a hopeful silence.
//
// A half-duplex link cannot answer a sender that never stops talking. The
// burst protocol makes the turnaround a stated thing: a burst sized in AIR,
// its last frame marked EOB, one cumulative answer, and — when the answer is
// lost — a one-frame POLL before a single byte of DATA is paid again.
package radiotransfer

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

// pollLimits shrink everything and keep the poll ladder on.
func pollLimits() Limits {
	return Limits{Window: 8, MaxRounds: 10,
		AckTimeout: 80 * time.Millisecond,
		SACKDelay:  5 * time.Millisecond, SendFloor: time.Millisecond,
		FrameGap: time.Millisecond, PollRetries: 2}
}

// The tombstone answers a POLL: a transfer whose every COMMIT is lost, and
// whose final SACK died with the completed inbound, confirms by ASKING —
// zero repaired frames, one short question.
func TestTheTombstoneAnswersAPoll(t *testing.T) {
	key := testKey(t)
	lim := pollLimits()

	sAir, rAir := newDelayedPair(200, key, 0, true) // every COMMIT dropped
	sender, err := NewSession(sAir, key, Options{Limits: lim})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewSession(rAir, key, Options{Limits: lim})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driveAir(ctx, receiver, rAir)
	driveAir(ctx, sender, sAir)

	if err := sender.Send(ctx, RadioAddress("peer"),
		bytes.Repeat([]byte("ask, do not resend. "), 25)); err != nil {
		t.Fatalf("a lost COMMIT should be survivable by polling: %v", err)
	}
	st := sender.Stats()
	if st.Completed != 1 {
		t.Fatalf("the transfer did not confirm: %+v", st)
	}
	if st.PollsSent == 0 {
		t.Fatal("no POLL was ever sent — the confirmation came some other way " +
			"and this proved nothing about the tombstone answering questions")
	}
	// The whole point: silence costs a question, never a burst.
	if st.RepairDataFrames > 1 {
		t.Fatalf("silence cost %d repaired DATA frames; a POLL should have "+
			"asked instead", st.RepairDataFrames)
	}
}

// A lost EOB — the burst's own turnaround marker — is recovered for the
// price of the one frame that was lost, never the burst.
func TestALostEOBIsRecoveredCheaply(t *testing.T) {
	key := testKey(t)
	lim := pollLimits()

	sAir, rAir := newDelayedPair(200, key, 0, false)
	sAir.dropFirstEOB = true
	sender, _ := NewSession(sAir, key, Options{Limits: lim})
	receiver, _ := NewSession(rAir, key, Options{Limits: lim})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arrived := make(chan []byte, 4)
	driveAirTo(ctx, receiver, rAir, arrived)
	driveAir(ctx, sender, sAir)

	msg := bytes.Repeat([]byte("the turnaround itself was lost. "), 20)
	if err := sender.Send(ctx, RadioAddress("peer"), msg); err != nil {
		t.Fatalf("a lost EOB sank the transfer: %v", err)
	}
	select {
	case got := <-arrived:
		if !bytes.Equal(got, msg) {
			t.Fatal("the message arrived changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered")
	}
	if st := sender.Stats(); st.RepairDataFrames > 2 {
		t.Fatalf("one lost frame cost %d repaired frames", st.RepairDataFrames)
	}
	if sAir.droppedEOBs == 0 {
		t.Fatal("no EOB was ever dropped, so this proved nothing")
	}
}

// A burst is sized in air. On a carrier that prices frames, the sender falls
// silent after its budget regardless of how much the window would allow.
func TestABurstIsSizedInAirNotFrames(t *testing.T) {
	key := testKey(t)
	const perFrame = 40 * time.Millisecond
	lim := Limits{Window: 8, MaxRounds: 10,
		AckTimeout: 400 * time.Millisecond,
		SACKDelay:  5 * time.Millisecond, SendFloor: time.Millisecond,
		FrameGap:         time.Millisecond,
		MaxQueuedAirtime: 8 * perFrame,
		// Two frames of air: the window says eight, the air says two, and
		// the air must win.
		BurstAirtime: 2 * perFrame}

	air, peerAir := newModelPair(200, perFrame, key, false)
	var mu sync.Mutex
	var run int
	maxRun := 0
	tr := func(ev TraceEvent) {
		if ev.Event != TraceDataTX {
			return
		}
		mu.Lock()
		run++
		if run > maxRun {
			maxRun = run
		}
		if ev.Reason == "eob" {
			run = 0
		}
		mu.Unlock()
	}
	sender, _ := NewSession(air, key, Options{Limits: lim, Trace: tr})
	receiver, _ := NewSession(peerAir, key, Options{Limits: lim})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arrived := make(chan []byte, 4)
	driveModel(ctx, receiver, peerAir, nil)
	driveModel(ctx, sender, air, nil)
	_ = arrived

	msg := bytes.Repeat([]byte("sized in air. "), 60) // several fragments
	if err := sender.Send(ctx, RadioAddress("peer"), msg); err != nil {
		t.Fatalf("the transfer failed under a burst cap: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxRun > 2 {
		t.Fatalf("a burst ran %d DATA frames without an EOB against an air "+
			"budget of two frames", maxRun)
	}
}

// RD-6f: an answer somebody is waiting for is never starved by our own
// outbound burst. One radio, both roles at once.
func TestRepliesAreNotStarvedByAnOutboundBurst(t *testing.T) {
	key := testKey(t)
	limSlow := Limits{Window: 8, MaxRounds: 10,
		AckTimeout: 2 * time.Second,
		SACKDelay:  5 * time.Millisecond, SendFloor: time.Millisecond,
		// A slow deliberate burst, so the outbound transfer takes real time.
		FrameGap: 60 * time.Millisecond, PollRetries: 2}

	xAir, yAir := newDelayedPair(200, key, 0, false)
	x, _ := NewSession(xAir, key, Options{Limits: limSlow})
	y, _ := NewSession(yAir, key, Options{Limits: limSlow})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	driveAir(ctx, x, xAir)
	driveAir(ctx, y, yAir)

	outDone := make(chan time.Duration, 1)
	inDone := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		// X's long outbound: ~12 fragments at 60 ms per frame.
		err := x.Send(ctx, RadioAddress("peer"),
			bytes.Repeat([]byte("a long deliberate burst from X. "), 50))
		if err != nil {
			t.Errorf("the long transfer failed: %v", err)
		}
		outDone <- time.Since(start)
	}()
	// Give X's burst a head start, then send a tiny message the other way.
	time.Sleep(100 * time.Millisecond)
	go func() {
		inStart := time.Now()
		err := y.Send(ctx, RadioAddress("peer"), []byte("one small ask from Y"))
		if err != nil {
			t.Errorf("the small transfer failed: %v", err)
		}
		inDone <- time.Since(inStart)
	}()

	var out, in time.Duration
	for range 2 {
		select {
		case out = <-outDone:
		case in = <-inDone:
		case <-time.After(30 * time.Second):
			t.Fatal("a transfer never finished")
		}
	}
	// The small inbound transfer must not have waited out the whole burst:
	// X's replies (SACK, COMMIT) go out BETWEEN X's own frames.
	if in > out {
		t.Fatalf("the one-fragment transfer (%s) outlasted the twelve-"+
			"fragment one (%s) — replies were starved by the burst", in, out)
	}
}
