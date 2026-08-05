// What a carrier will carry, and what it refuses.
//
// The beta cut promised media would not travel over LoRa — "join, text, small
// control and bounded history only" — and nothing enforced it. A photo went
// out as one signed event of 41 KB, measured at ninety-nine frames and six and
// a half minutes of air, during which nothing else on that radio moved. The
// promise was prose; there was no lock.
package sync

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/signal"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports"
)

// carryEndpoint is a carrier with an opinion about what belongs on it.
type carryEndpoint struct {
	mtu    int
	ceil   int
	noBlob bool
}

func (carryEndpoint) Send([]byte) error { return nil }
func (carryEndpoint) Poll() [][]byte    { return nil }
func (e carryEndpoint) Capabilities() transports.Capabilities {
	return transports.Capabilities{
		MaxPayload: e.mtu, MaxEventBytes: e.ceil, BlobsRefused: e.noBlob,
	}
}

// The rule, at the seam where it is actually asked.
//
// Sizes are the MEASURED ones from node/radiocarry_measure_test.go on the
// profile the boards run, so this test fails if either the envelope or the
// ceiling drifts into disagreeing with the physics.
func TestACarrierCarriesWhatItSaysAndRefusesTheRest(t *testing.T) {
	// ~1.5 KiB is what twenty seconds of RU long-fast air buys.
	radio := carryEndpoint{mtu: 500, ceil: 1536}.Capabilities()

	for _, c := range []struct {
		what string
		size int
		want bool
	}{
		{"a short message", 340, true},
		{"a reaction", 387, true},
		{"a long message", 857, true},
		{"an image with a 2 KiB preview", 2388, false},
		{"an image with a 40 KiB preview", 41300, false},
	} {
		if got := radio.CarriesEvent(c.size); got != c.want {
			t.Errorf("%s (%d bytes): carried=%v, want %v", c.what, c.size, got, c.want)
		}
	}

	// A carrier with no opinion carries everything, which is every carrier
	// that exists today and must stay exactly as it was.
	open := carryEndpoint{mtu: 0}.Capabilities()
	if !open.CarriesEvent(41300) {
		t.Fatal("a carrier that declared no ceiling started refusing things")
	}
}

// A refusal must reach somebody. Silence is what this whole wave exists to
// end: a message that simply never arrives is indistinguishable from a
// message nobody sent.
func TestAnOversizeEventIsReportedRatherThanDropped(t *testing.T) {
	var seen struct {
		size, ceiling int
		fired         int
	}
	e := &Engine{OnTooLarge: func(_ id.EventID, size, ceiling int) {
		seen.size, seen.ceiling, seen.fired = size, ceiling, seen.fired+1
	}}
	// The seam is a field, so the check here is that it is WIRED — that the
	// engine holds somewhere for the node to listen. Without this the refusal
	// added by this wave would be a silent drop, which is worse than the jam
	// it replaces: at least a jam eventually finishes.
	if e.OnTooLarge == nil {
		t.Fatal("the engine has no way to report a refusal")
	}
	e.OnTooLarge(id.EventID{}, 41300, 1536)
	if seen.fired != 1 || seen.size != 41300 || seen.ceiling != 1536 {
		t.Fatalf("the report did not carry what it must: %+v", seen)
	}
}

// pushMissing MUST RETURN when a chain contains a frame the carrier refuses.
//
// The first version of this gate used `continue`, and c.next advances at the
// BOTTOM of that loop — so the refused frame was re-read forever, at full
// speed, while the runtime lock was held. Every API call on the node wedged
// behind it. That is strictly worse than the jam this whole wave replaces: a
// jam finishes.
//
// It took two boards, a live send and a goroutine dump to find, and this test
// would have found it in milliseconds. It has a deadline because a live-lock
// does not FAIL a test — it hangs one, and a hung suite teaches people to run
// the suite less.
func TestARefusedFrameEndsItsChainRatherThanSpinningForever(t *testing.T) {
	var term id.TerminalID
	term[0] = 0xC0
	log := eventlog.New(term, nil)
	a := newAuthor(t, term, 0x61)

	// Small, HUGE, small — the oversize frame in the middle, which is where a
	// skip would also have produced a hole in a chain that cannot have one.
	for _, f := range [][]byte{
		a.framePri(t, "message.text.v1", textPayload(t, "before"), signal.PriorityMessage, 0),
		a.framePri(t, "message.text.v1",
			textPayload(t, strings.Repeat("x", 4000)), signal.PriorityMessage, 0),
		a.framePri(t, "message.text.v1", textPayload(t, "after"), signal.PriorityMessage, 0),
	} {
		if _, err := log.Ingest(f); err != nil {
			t.Fatal(err)
		}
	}

	var refused int
	e := &Engine{Log: log, OnTooLarge: func(id.EventID, int, int) { refused++ }}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.pushMissing(carryEndpoint{mtu: 500, ceil: 1536},
			&summary{terminal: term, chains: map[id.DeviceID]uint64{}})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pushMissing did not return: a frame the carrier refuses is " +
			"being re-read forever, and it holds the runtime lock while it does")
	}
	if refused != 1 {
		t.Fatalf("the oversize frame was reported %d times, want exactly once — "+
			"more than one means the chain is being re-walked", refused)
	}
}
