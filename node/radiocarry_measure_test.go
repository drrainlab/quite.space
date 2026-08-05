// What things actually WEIGH, before anything is designed around them.
//
// The RD wave cost nine days largely to the habit of reasoning about the air
// instead of measuring it, so this wave starts the other way round: real
// signed envelopes, the same bytes sync would hand a radio, priced in frames
// and in seconds of airtime on the profile the boards actually run.
//
// Run it to see the table:
//
//	go test ./node/ -run TestWhatOneEventWeighsOnTheAir -v
package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/resonance"
	"github.com/drrainlab/quiet_places/transports/rnode"
)

// framesFor prices a payload the way the transfer layer will: whole frames of
// the modem's MTU, with the layer's own header and MAC inside each one.
//
// perFrame is deliberately conservative — the exact header is measured by
// sender.go at run time and varies by a few bytes. Being a little pessimistic
// here is the right direction: it never promises air it cannot buy.
func framesFor(n int) (frames int, seconds float64) {
	const perFrame = rnode.MaxFrame - 80 // transfer header + MAC, rounded up
	if n <= 0 {
		return 0, 0
	}
	frames = (n + perFrame - 1) / perFrame
	// The SAME cost the ceiling is derived from — Settings.Airtime PLUS the
	// transmit guard, which is the measured gap between modelled air and the
	// wall clock. Pricing a frame without it understates every number here by
	// 700 ms, which is exactly what it did: the ceiling computed on real
	// hardware came out 2155 bytes while this test predicted 2586, and the
	// difference was entirely one function disagreeing with another about
	// what a frame costs.
	full := (rnode.LongFastRU().Airtime(rnode.MaxFrame) + rnode.TxGuard).Seconds()
	return frames, float64(frames) * full
}

func TestWhatOneEventWeighsOnTheAir(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	tid, err := rt.CreateSpace("weights")
	if err != nil {
		t.Fatal(err)
	}

	// Every measurement below is the ENCODED SIGNED FRAME — what the log
	// holds and what pushMissing hands to a carrier verbatim.
	measure := func(label string, eid id.EventID) int {
		t.Helper()
		var size int
		rt.mu.Lock()
		st := rt.spaces[tid]
		rt.mu.Unlock()
		for _, f := range st.space.Log.FramesInRange(rt.Device.ID, 0, 1<<62) {
			if id.EventIDOf(f) == eid {
				size = len(f)
			}
		}
		if size == 0 {
			t.Fatalf("%s: could not find its own frame in the log", label)
		}
		n, secs := framesFor(size)
		t.Logf("%-34s %7d bytes  %4d frames  %7.1f s of air", label, size, n, secs)
		return size
	}

	short, err := rt.Say(tid, "on my way", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	textSize := measure("a short message (9 chars)", short)

	long, err := rt.Say(tid, strings.Repeat("a sentence that keeps going. ", 18), SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	measure("a long message (~500 chars)", long)

	// A reaction: the thing the owner asked about directly.
	set := resonance.SetPayload{
		Target: short,
		Reaction: resonance.Reaction{
			Kind:     resonance.KindSemantic,
			Key:      resonance.CoreRegistry[0].Key,
			Fallback: resonance.CoreRegistry[0].Fallback,
		},
	}
	payload, err := set.Encode()
	if err != nil {
		t.Fatal(err)
	}
	rid, err := rt.EmitBlock(tid, resonance.SchemaSet, payload)
	if err != nil {
		t.Fatal(err)
	}
	reactionSize := measure("a reaction", rid)

	// And the thing that jammed the channel. An inline preview is carried
	// INSIDE the signed block, so it travels as log history to every peer on
	// every carrier — it is not fetched on demand the way the full asset is.
	for _, kb := range []int{2, 8, 40} {
		n, secs := framesFor(textSize + (kb << 10))
		t.Logf("%-34s %7d bytes  %4d frames  %7.1f s of air",
			"an image block, "+itoa(kb)+" KiB preview", textSize+(kb<<10), n, secs)
	}

	t.Logf("")
	t.Logf("for scale: BetaOutboundCap is %d bytes, and the ledger already", 1536)
	t.Logf("refuses a radio route above it — while sync, which is what")
	t.Logf("actually moves a space over a radio, never asks.")
	t.Logf("a reaction is %.1f%% of a short message.",
		100*float64(reactionSize)/float64(textSize))
}
