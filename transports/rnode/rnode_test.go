// The queue model, without a board.
//
// Everything here is arithmetic over the driver's own bookkeeping: what the
// modem was handed, how long the model says that occupies the air, and when
// the queue is therefore expected to drain. No serial port is opened.
package rnode

import (
	"testing"
	"time"
)

// The airtime model against the measurements it was built from. The model is
// documented as deliberately BELOW wall clock (serial transfer, firmware and
// scheduling are real and are not air), and FrameAirtime adds the measured
// guard back — so the model must sit under the measurement, and model+guard
// must sit near or above it.
func TestTheAirtimeModelMatchesTheMeasuredBoards(t *testing.T) {
	s := LongFastRU()
	measured := []struct {
		bytes int
		wall  time.Duration
	}{
		{36, 750 * time.Millisecond},
		{200, 2010 * time.Millisecond},
		{400, 3820 * time.Millisecond},
	}
	for _, m := range measured {
		air := s.Airtime(m.bytes)
		if air >= m.wall {
			t.Fatalf("the model prices %d bytes at %s, above the measured %s — "+
				"it is documented as a floor and has stopped being one",
				m.bytes, air, m.wall)
		}
		if withGuard := air + txGuard; withGuard < m.wall-300*time.Millisecond {
			t.Fatalf("model+guard prices %d bytes at %s against a measured %s — "+
				"optimistic by more than the tolerance, which rebuilds the queue",
				m.bytes, withGuard, m.wall)
		}
	}
}

// The queue model: consecutive accepted frames stack, an idle queue owes
// nothing backwards, and the estimate is exactly the sum of the parts.
func TestTheQueueModelStacksAndDrains(t *testing.T) {
	r := &Radio{set: LongFastRU(), mtu: MaxFrame}

	per := r.FrameAirtime(100)
	if per <= txGuard {
		t.Fatalf("a 100-byte frame prices at %s, at or under the bare guard", per)
	}

	// Idle: nothing queued, the drain estimate must not be in the future.
	if end := r.EstimatedTxEnd(); time.Until(end) > 0 {
		t.Fatalf("an idle radio claims %s of queued air", time.Until(end))
	}

	// Three frames accepted back to back must stack to three frames of air.
	// The accounting is exercised directly, the way Send does it, because
	// Send itself needs a serial port.
	for range 3 {
		r.mu.Lock()
		start := time.Now()
		if r.estTxEnd.After(start) {
			start = r.estTxEnd
		}
		r.estTxEnd = start.Add(r.FrameAirtime(100))
		r.mu.Unlock()
	}
	backlog := time.Until(r.EstimatedTxEnd())
	want := 3 * per
	if backlog < want-50*time.Millisecond || backlog > want+50*time.Millisecond {
		t.Fatalf("three stacked frames model %s of backlog, want ~%s", backlog, want)
	}
}

// The frame-size derivation stays inspectable and the floor holds: at SF12
// the airtime target and a useful frame genuinely conflict, and the floor
// must win — a frame carrying four payload bytes is not slow, it is useless.
func TestTheFrameFloorBeatsTheAirtimeTarget(t *testing.T) {
	for _, tc := range []struct {
		sf   uint8
		want int
	}{
		{7, MaxFrame},  // fast PHY: the modem's limit is the only limit
		{9, MaxFrame},  //
		{11, MinFrame}, // the measured PHY: capped by the airtime target
		{12, MinFrame}, // the conflict case: the floor wins over the target
	} {
		s := Settings{FrequencyHz: 868_950_000, BandwidthHz: 250_000,
			SpreadingF: tc.sf, CodingRate: 5}
		if got := MTUFor(s); got != tc.want {
			t.Fatalf("SF%d derives an MTU of %d, want %d", tc.sf, got, tc.want)
		}
	}
}
