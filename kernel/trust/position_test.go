package trust

import (
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// The position register must converge in ANY arrival order — including
// the equal-EmittedAt case where presence's plain ">" guard would let
// arrival order decide (the hole ADR-031 closes with the EventID
// tiebreak).
func TestPositionLWWOrderIndependent(t *testing.T) {
	var src id.TerminalID
	src[0] = 0xAA
	mk := func(emitted uint64, eidByte byte, lat uint64) claims.Position {
		var eid id.EventID
		eid[0] = eidByte
		return claims.Position{LatE7U: lat, LonE7U: 1, EmittedAt: emitted,
			ExpiresAt: emitted + 600, Source: src, EventID: eid}
	}
	a := mk(100, 0x01, 111)
	b := mk(200, 0x02, 222) // later — must win over a
	c := mk(200, 0x09, 333) // equal time, higher event id — must win over b
	d := mk(150, 0xFF, 444) // older — must never win
	all := []claims.Position{a, b, c, d}

	for trial := 0; trial < 20; trial++ {
		e := NewEngine()
		for _, i := range rand.Perm(len(all)) {
			e.UpdatePosition(all[i])
		}
		got := e.positions[src]
		if got.LatE7U != 333 {
			t.Fatalf("trial %d: winner %d, want 333 (equal-EmittedAt decided by arrival?)", trial, got.LatE7U)
		}
	}
}

func TestPositionProjectionHonestAgeing(t *testing.T) {
	var src id.TerminalID
	src[0] = 0xBB
	e := NewEngine()
	// Unknown before any claim.
	if p := e.Position(src, 1000); p.Known {
		t.Fatal("phantom position")
	}
	var eid id.EventID
	e.UpdatePosition(claims.Position{LatE7U: 5, LonE7U: 6, AccuracyM: 8,
		EmittedAt: 1000, ExpiresAt: 1600, Source: src, EventID: eid})
	// Live: current, remaining from the SIGNED expiry.
	p := e.Position(src, 1030)
	if !p.Known || !p.Current || p.RemainingSeconds != 570 || p.AgeSeconds != 30 {
		t.Fatalf("live projection wrong: %+v", p)
	}
	// Expired: last known + age, never current — the map ages honestly.
	p = e.Position(src, 5000)
	if !p.Known || p.Current || p.RemainingSeconds != 0 || p.AgeSeconds != 4000 {
		t.Fatalf("stale projection wrong: %+v", p)
	}
	if p.LatE7U != 5 {
		t.Fatal("stale projection lost the last known place")
	}
}
