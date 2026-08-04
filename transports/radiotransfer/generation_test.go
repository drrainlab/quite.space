// Generations: telling a report from a history.
//
// The measured pathology was not loss but LAG — fourteen SACKs delivered
// intact, every one describing a window the sender had left a minute earlier.
// A generation number on DATA, echoed on SACK, is the one bit of state that
// lets the sender know which kind of frame it is holding.
package radiotransfer

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

// sackFor builds a receiver's report by hand: which fragments of the window
// starting at base are held, under which generation.
func sackFor(o *Outbound, gen uint64, held ...int) *Frame {
	bitmap := make([]byte, MaxBitmapBytes)
	for _, i := range held {
		SetBit(bitmap, i)
	}
	return &Frame{Kind: KindSACK, Transfer: o.ID, Count: uint64(o.Count()),
		Base: 0, Bitmap: bitmap, Generation: gen}
}

// The delayed-history case, exactly as traced: a drip of stale reports must
// merge as evidence and decide nothing.
func TestAStaleSACKMergesEvidenceButDecidesNothing(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("g"), 900), 200, Limits{Window: 8}, key)
	if err != nil {
		t.Fatal(err)
	}
	// Two bursts have gone out; the transfer is on generation 2.
	o.NextGeneration()
	o.NextGeneration()

	// History arrives: SACKs from burst 1, each acknowledging a little more.
	for held := 1; held <= 3; held++ {
		frags := make([]int, held)
		for i := range frags {
			frags[i] = i
		}
		if class := o.NoteSACK(sackFor(o, 1, frags...)); class != sackStale {
			t.Fatalf("a burst-1 SACK during burst 2 classified as %v, want stale", class)
		}
	}
	// The evidence was kept: fragments 0-2 are acknowledged...
	if got := o.Pending(); len(got) == 0 || got[0] != 3 {
		t.Fatalf("stale evidence was not merged: pending %v, want it to start at 3", got)
	}
	// ...and the futility budget was NOT renewed by it.
	if !o.NextRound() {
		t.Fatal("one round should still be affordable")
	}
	if o.Rounds() != 1 {
		t.Fatalf("stale progress renewed the futility budget: rounds %d, want 1",
			o.Rounds())
	}

	// Now the answer to the burst in flight arrives and IS allowed to renew.
	if class := o.NoteSACK(sackFor(o, 2, 0, 1, 2, 3)); class != sackCurrent {
		t.Fatal("the current burst's echo did not classify as current")
	}
	if !o.NextRound() || o.Rounds() != 0 {
		t.Fatalf("fresh progress did not renew the budget: rounds %d", o.Rounds())
	}
}

// A receiver that has never spoken a generation is an older build, and for it
// nothing changes: every SACK stays current.
func TestALegacyPeerKeepsTheOldBehaviour(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("l"), 900), 200, Limits{Window: 8}, key)
	if err != nil {
		t.Fatal(err)
	}
	o.NextGeneration()
	o.NextGeneration()
	o.NextGeneration() // burst 3, and the peer has no idea

	if class := o.NoteSACK(sackFor(o, 0, 0, 1)); class != sackCurrent {
		t.Fatalf("a generation-less SACK classified as %v, want current — an old "+
			"receiver must keep the old behaviour", class)
	}
	if !o.NextRound() || o.Rounds() != 0 {
		t.Fatal("legacy progress must still renew the futility budget")
	}

	// But once the peer HAS spoken a generation, zero stops meaning legacy.
	o.NoteSACK(sackFor(o, 3, 2))
	if class := o.NoteSACK(sackFor(o, 0, 3)); class != sackStale {
		t.Fatalf("a zero-generation SACK from a peer that speaks generations "+
			"classified as %v, want stale", class)
	}
}

// A burst number this sender never sent is a desync: evidence kept, authority
// denied.
func TestAFutureGenerationIsMergedButIgnored(t *testing.T) {
	key := testKey(t)
	o, err := NewOutbound(bytes.Repeat([]byte("f"), 400), 200, Limits{Window: 4}, key)
	if err != nil {
		t.Fatal(err)
	}
	o.NextGeneration() // burst 1
	if class := o.NoteSACK(sackFor(o, 7, 0)); class != sackFuture {
		t.Fatalf("generation 7 against a current burst of 1 classified as %v", class)
	}
	if got := o.Pending(); len(got) > 0 && got[0] == 0 {
		t.Fatal("the future SACK's evidence was discarded rather than merged")
	}
	if !o.NextRound() || o.Rounds() == 0 {
		t.Fatal("a future SACK renewed the futility budget")
	}
}

// The wire: generation survives a round trip on DATA and SACK, and its
// absence decodes as zero — the compatibility the whole design leans on.
func TestGenerationSurvivesTheWire(t *testing.T) {
	key := testKey(t)

	data := &Frame{Kind: KindData, Transfer: TransferID{1}, Index: 2, Count: 4,
		Total: 500, Chunk: []byte("chunk"), Generation: 3}
	b, err := data.Encode(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 3 {
		t.Fatalf("DATA generation %d, want 3", got.Generation)
	}

	sack := &Frame{Kind: KindSACK, Transfer: TransferID{1}, Count: 4,
		Base: 0, Bitmap: []byte{0x0F}, Generation: 5}
	b, err = sack.Encode(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err = Decode(b, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 5 {
		t.Fatalf("SACK generation %d, want 5", got.Generation)
	}

	// No generation written, none read: byte-compatible with an old sender.
	plain := &Frame{Kind: KindSACK, Transfer: TransferID{1}, Count: 4,
		Base: 0, Bitmap: []byte{0x0F}}
	b, err = plain.Encode(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err = Decode(b, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 0 {
		t.Fatalf("an old-style SACK decoded generation %d, want 0", got.Generation)
	}
}

// End to end over the queue that caused it all: with generations, a drip of
// stale feedback no longer drives retransmission. The same carrier and the
// same lost COMMITs that once produced 191 frames now cost a handful of
// repair frames, because the sender waits for the answer to ITS burst
// instead of reacting to the oldest view in the backlog.
func TestStaleFeedbackNoLongerDrivesRepair(t *testing.T) {
	key := testKey(t)
	lim := budgetLimits(40, 5*time.Second)
	// Feedback is delayed but INSIDE the sender's patience, the frames are
	// spaced far enough apart that the receiver reports per frame (the real
	// boards' shape), and the coalesce window is wide enough to absorb that
	// drip. What remains under test is the decision discipline itself.
	lim.AckTimeout = 400 * time.Millisecond
	lim.FrameGap = 30 * time.Millisecond
	lim.SACKCoalesce = 200 * time.Millisecond

	sAir, rAir := newDelayedPair(200, key, 60*time.Millisecond, true)
	var traceMu sync.Mutex
	var events []string
	tr := func(side string) Tracer {
		return func(ev TraceEvent) {
			traceMu.Lock()
			events = append(events, side+" "+ev.String())
			traceMu.Unlock()
		}
	}
	sender, err := NewSession(sAir, key, Options{Limits: lim, Trace: tr("SEND")})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewSession(rAir, key, Options{Limits: lim, Trace: tr("RECV")})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driveAir(ctx, receiver, rAir)
	driveAir(ctx, sender, sAir)

	err = sender.Send(ctx, RadioAddress("peer"),
		bytes.Repeat([]byte("a report, not a history. "), 25))
	// With generations, coalescing AND the tombstone, dropped COMMITs are no
	// longer fatal: the one duplicate of the last fragment buys a repeatable
	// complete-SACK, and the transfer confirms.
	if err != nil {
		t.Fatalf("with COMMITs dropped the transfer should now CONFIRM via "+
			"the tombstone; got %v", err)
	}

	st := sender.Stats()
	// The point of the whole gate: without generations and coalescing, the
	// drip of partial reports re-sent whole windows against the oldest view.
	// With them, a clean-delivery run (only the confirmations are lost)
	// repairs NOTHING — the first current answer is held open until the rest
	// of the drip lands, and the merged view shows everything delivered.
	if st.RepairDataFrames > 4 {
		traceMu.Lock()
		for _, e := range events {
			t.Log(e)
		}
		traceMu.Unlock()
		t.Fatalf("stale feedback still drives repair: %d repair frames for a "+
			"~5-fragment message with nothing actually lost", st.RepairDataFrames)
	}
	if st.FramesIn == 0 {
		t.Fatal("no feedback reached the sender, so this proved nothing")
	}
}
