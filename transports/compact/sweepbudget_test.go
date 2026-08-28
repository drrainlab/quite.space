package compact

// SP-3.2 (ADR-034): sweep.completed.v1 must ride ONE RNode frame
// (500 B) — the completion of an operation must never depend on how the
// radio session happens to be feeling. Two measurements, because the
// margin is thin (~60 B):
//
//   WARM — the TN-2B id table is interned, the steady case;
//   COLD — the completion is the FIRST event of a fresh radio session,
//   nothing interned yet. A sweep ends hours after it started, often on
//   a link that was born in between, so cold is not a corner case here
//   the way it is for chat.
//
// If cold ever breaks the budget, the choice is made OUT LOUD: shrink
// the payload (the fallback cap is the valve) or state the guarantee as
// warm-only in ADR-034 — never discover it in a valley.
//
// The deferred coarse polyline (key 9) is measured too, so the ADR can
// cite a number instead of a taste.

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/loopback"
)

func TestSweepCompletedBudgets(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x61}, ed25519.SeedSize))
	var term id.TerminalID
	term[0] = 0xF3
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))

	frame := func(t *testing.T, seq uint64, prev *id.EventID, schema string, payload []byte) []byte {
		t.Helper()
		env := &signal.Envelope{
			Terminal: term, Principal: id.PrincipalID{9}, Device: dev,
			Sequence: seq, Previous: prev, Schema: schema,
			LogicalClock: seq, CreatedAt: 1_790_000_000,
			ProducedBy:      signal.AuthorshipHuman,
			PayloadEncoding: signal.PayloadCBOR, Payload: payload,
			Priority: signal.PriorityMessage,
		}
		f, err := env.Sign(priv)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	// Worst case: the fallback cap filled with pseudo-random Cyrillic
	// (the routebudget LCG — repetition would hand DEFLATE a free win).
	ruLong := func(runes int) string {
		s := make([]rune, runes)
		state := uint32(0x9E3779B9)
		for i := range s {
			state = state*1664525 + 1013904223
			s[i] = rune('а' + (state>>24)%32)
		}
		return string(s)
	}
	pmin, err := geo.FromDegrees(46.6180, 8.0290)
	if err != nil {
		t.Fatal(err)
	}
	pmax, err := geo.FromDegrees(46.6321, 8.0517)
	if err != nil {
		t.Fatal(err)
	}
	sw := &schemas.CompletedSweep{
		Fallback:  ruLong(schemas.MaxSweepFallbackRunes),
		StartedAt: 1_789_990_000, EndedAt: 1_790_000_000,
		DistanceM: 987_654,                   // absurdly long walk: worst uint width short of silly
		Result:    schemas.SweepNothingFound, // the longest slug
		BBoxMin:   pmin, BBoxMax: pmax,
	}
	copy(sw.ObjectID[:], bytes.Repeat([]byte{0x35}, 16))
	copy(sw.TrackAsset[:], bytes.Repeat([]byte{0x77}, 32))
	payload, err := sw.Encode()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("payload worst-RU: %d B", len(payload))

	const rnodeBudget = 500

	// COLD: the very first frame of a fresh compact session.
	{
		pair := loopback.NewPair(loopback.Faults{Seed: 3})
		tap := &sizeTap{inner: pair.A}
		a := WrapStateful(tap).(*statefulWrap)
		b := WrapStateful(pair.B).(*statefulWrap)
		f := frame(t, 1, nil, schemas.SweepCompleted, payload)
		if err := a.Send(kernelsync.EncodeFramesMessage(term, [][]byte{f})); err != nil {
			t.Fatal(err)
		}
		b.Poll()
		a.Poll()
		cold := tap.sizes[0]
		t.Logf("sweep.completed worst-RU → %3d B COLD-compact (budget %d)", cold, rnodeBudget)
		if cold > rnodeBudget {
			t.Errorf("cold sweep.completed breaks the guarantee: %d B > %d B — shrink the payload or restate ADR-034 out loud", cold, rnodeBudget)
		}
	}

	// WARM: after one interned frame.
	{
		pair := loopback.NewPair(loopback.Faults{Seed: 4})
		tap := &sizeTap{inner: pair.A}
		a := WrapStateful(tap).(*statefulWrap)
		b := WrapStateful(pair.B).(*statefulWrap)
		pt, _ := geo.FromDegrees(46.62, 8.03)
		posPayload, err := (&schemas.PositionObservation{Point: pt, AccuracyM: 8, ExpiresAt: 1_790_000_600}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		warm := frame(t, 1, nil, schemas.ObservationPosition, posPayload)
		if err := a.Send(kernelsync.EncodeFramesMessage(term, [][]byte{warm})); err != nil {
			t.Fatal(err)
		}
		b.Poll()
		a.Poll()
		prev := id.EventIDOf(warm)
		f := frame(t, 2, &prev, schemas.SweepCompleted, payload)
		before := len(tap.sizes)
		if err := a.Send(kernelsync.EncodeFramesMessage(term, [][]byte{f})); err != nil {
			t.Fatal(err)
		}
		b.Poll()
		a.Poll()
		size := tap.sizes[before]
		t.Logf("sweep.completed worst-RU → %3d B warm-compact (budget %d)", size, rnodeBudget)
		if size > rnodeBudget {
			t.Errorf("warm sweep.completed breaks the guarantee: %d B > %d B", size, rnodeBudget)
		}
		// The deferred polyline, measured for the ADR: how many raw
		// [lat,lon] pairs fit in what remains of the budget.
		perPoint := 11 // two ~5B uints + array header, the routebudget arithmetic
		room := (rnodeBudget - size) / perPoint
		t.Logf("coarse polyline (key 9, deferred): budget remainder fits ~%d points — a bbox, not a shape (ADR-034 cites this)", room)
	}
}
