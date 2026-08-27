package compact

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	pcodec "github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports"
	"github.com/drrainlab/quiet_places/transports/loopback"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// encodeRouteRecordForMeasurement hand-builds the canonical wire form of
// a realistic Field-authored route record with n path points — the SAME
// bytes objects.Record.Encode would produce, but free of the authoring
// bound, so the measurement can find where that bound belongs.
func encodeRouteRecordForMeasurement(t *testing.T, n int) []byte {
	t.Helper()
	// Worst-case Cyrillic name at the Field authoring profile's 32-rune
	// cap — two UTF-8 bytes per rune is the honest budget, not ASCII.
	const ruName = "Северный маршрут через перевал Х"
	buf := pcodec.AppendMap(nil, 6) // id, kind, name, status, parent, path
	buf = pcodec.AppendUint(buf, 1)
	buf = pcodec.AppendBytes(buf, bytes.Repeat([]byte{0x7A}, 16))
	buf = pcodec.AppendUint(buf, 2)
	buf = pcodec.AppendText(buf, "route")
	buf = pcodec.AppendUint(buf, 3)
	buf = pcodec.AppendText(buf, ruName)
	buf = pcodec.AppendUint(buf, 4)
	buf = pcodec.AppendText(buf, "active")
	buf = pcodec.AppendUint(buf, 8)
	buf = pcodec.AppendBytes(buf, bytes.Repeat([]byte{0x7B}, 16))
	buf = pcodec.AppendUint(buf, 10)
	buf = pcodec.AppendArray(buf, n*2)
	for i := 0; i < n; i++ {
		p, err := geo.FromDegrees(59.3+float64(i)*0.011, 18.0+float64(i)*0.013)
		if err != nil {
			t.Fatal(err)
		}
		buf = pcodec.AppendUint(buf, p.LatE7U)
		buf = pcodec.AppendUint(buf, p.LonE7U)
	}
	return buf
}

// sizeTap records the wire size of everything sent through it.
type sizeTap struct {
	inner transports.Endpoint
	sizes []int
}

func (s *sizeTap) Send(p []byte) error { s.sizes = append(s.sizes, len(p)); return s.inner.Send(p) }
func (s *sizeTap) Poll() [][]byte      { return s.inner.Poll() }
func (s *sizeTap) Capabilities() transports.Capabilities {
	return s.inner.Capabilities()
}

// TestMaxRoutePointsIsMeasured pins objects.MaxRoutePoints to a
// MEASUREMENT, not an aesthetic. The two-tier radio law (ADR-031),
// revised by measurement: a route revision's envelope floor (~246 B —
// signature, envelope metadata, revision scaffolding) can NEVER fit one
// Meshtastic frame, and chain blocking (C3) only bites at ErrTooLarge —
// a two-frame event just rides two frames. So the HARD guarantee is one
// RNode frame (500 B) for every field event with worst-case Cyrillic
// text: far below every ErrTooLarge ceiling, an SOS is never stuck
// behind a conforming route. The single-Meshtastic-frame IDEAL is
// proven separately for position and the default check-in.
func TestMaxRoutePointsIsMeasured(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	var term id.TerminalID
	term[0] = 0xF1
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))

	// A realistic Field-authored route record — the WHOLE record, not
	// "N points + minimal object": kind, a real name, a status, a parent
	// (the route lives under an incident/place), and the path.
	routeFrame := func(t *testing.T, seq uint64, prev *id.EventID, n int) []byte {
		t.Helper()
		// The record is HAND-ENCODED so the measurement can walk PAST the
		// authoring bound it exists to determine — a validated Encode
		// would refuse the very sizes we need to observe.
		enc := encodeRouteRecordForMeasurement(t, n)
		payload := (&objects.RevisionPayload{Fallback: "Северный маршрут через перевал Х", Record: enc}).Encode()
		env := &signal.Envelope{
			Terminal: term, Principal: id.PrincipalID{7}, Device: dev,
			Sequence: seq, Previous: prev, Schema: objects.SchemaRevised,
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

	// Warm-compact wire size of the route frame: a stateful pair, one
	// warm-up frame to define and ACK the id table, then the measured
	// frame rides short indexes — the steady state of a field link.
	warmSize := func(t *testing.T, n int) int {
		t.Helper()
		pair := loopback.NewPair(loopback.Faults{Seed: 1})
		tapA := &sizeTap{inner: pair.A}
		a := WrapStateful(tapA).(*statefulWrap)
		b := WrapStateful(pair.B).(*statefulWrap)

		warm := routeFrame(t, 1, nil, 2)
		if err := a.Send(kernelsync.EncodeFramesMessage(term, [][]byte{warm})); err != nil {
			t.Fatal(err)
		}
		b.Poll()
		a.Poll() // drain TABLE_ACK — the table is now warm
		prev := id.EventIDOf(warm)
		measured := routeFrame(t, 2, &prev, n)
		before := len(tapA.sizes)
		if err := a.Send(kernelsync.EncodeFramesMessage(term, [][]byte{measured})); err != nil {
			t.Fatal(err)
		}
		if got := b.Poll(); len(got) == 0 {
			t.Fatal("measured frame not delivered")
		}
		if len(tapA.sizes) != before+1 {
			t.Fatalf("expected one wire packet, got %d", len(tapA.sizes)-before)
		}
		return tapA.sizes[before]
	}

	rnodeBudget := 500 // rnode.MaxFrame — the hard tier
	maxFitRNode := 0
	for n := 2; n <= 40; n++ {
		size := warmSize(t, n)
		t.Logf("route with %2d points → %3d B warm-compact (rnode budget %d)",
			n, size, rnodeBudget)
		if size <= rnodeBudget {
			maxFitRNode = n
		} else {
			break
		}
	}
	t.Logf("maxFit rnode=%d", maxFitRNode)
	if objects.MaxRoutePoints != maxFitRNode {
		t.Fatalf("MaxRoutePoints=%d but measurement says %d — the constant loses (update it and the ADR)",
			objects.MaxRoutePoints, maxFitRNode)
	}
}

// TestFieldEventTiers proves both tiers of the radio law (ADR-031):
//   - IDEAL: position and the default check-in ride ONE Meshtastic
//     frame (233 B), warm;
//   - GUARANTEE: every field event — worst-case Cyrillic texts at their
//     caps included — rides ONE RNode frame (500 B), so a conforming
//     field event can never block its author's chain (C3 bites only at
//     ErrTooLarge, far above).
func TestFieldEventTiers(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x52}, ed25519.SeedSize))
	var term id.TerminalID
	term[0] = 0xF2
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))
	pt, err := geo.FromDegrees(59.3321, 18.0412)
	if err != nil {
		t.Fatal(err)
	}

	frame := func(t *testing.T, seq uint64, prev *id.EventID, schema string, payload []byte) []byte {
		t.Helper()
		env := &signal.Envelope{
			Terminal: term, Principal: id.PrincipalID{8}, Device: dev,
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

	posPayload, err := (&schemas.PositionObservation{Point: pt, AccuracyM: 8, ExpiresAt: 1_790_000_600}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	ciPayload, err := (&schemas.Checkin{Text: "✓ check-in", Point: &pt, BatteryPct: 67, HasBattery: true}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Worst cases for the RNode guarantee: caps filled with
	// PSEUDO-RANDOM Cyrillic (two UTF-8 bytes per rune, seeded LCG so
	// the test is deterministic) — repeated characters would hand
	// DEFLATE a free win and make the "worst case" a best case.
	ruLong := func(runes int) string {
		s := make([]rune, runes)
		state := uint32(0x9E3779B9)
		for i := range s {
			state = state*1664525 + 1013904223
			s[i] = rune('а' + (state>>24)%32)
		}
		return string(s)
	}
	mk := &schemas.PlacedMarker{Text: ruLong(schemas.MaxMarkerTextRunes), Kind: "hazard", Point: pt, ExpiresAt: 1_790_000_600}
	copy(mk.MarkerID[:], bytes.Repeat([]byte{0x33}, 16))
	var mkObj [16]byte
	copy(mkObj[:], bytes.Repeat([]byte{0x34}, 16))
	mk.ObjectID = &mkObj
	mkPayload, err := mk.Encode()
	if err != nil {
		t.Fatal(err)
	}
	ciWorst := &schemas.Checkin{Text: ruLong(schemas.MaxCheckinTextRunes), Point: &pt, BatteryPct: 100, HasBattery: true, SOS: true}
	ciWorstPayload, err := ciWorst.Encode()
	if err != nil {
		t.Fatal(err)
	}

	pair := loopback.NewPair(loopback.Faults{Seed: 2})
	tapA := &sizeTap{inner: pair.A}
	a := WrapStateful(tapA).(*statefulWrap)
	b := WrapStateful(pair.B).(*statefulWrap)
	warm := frame(t, 1, nil, schemas.ObservationPosition, posPayload)
	if err := a.Send(kernelsync.EncodeFramesMessage(term, [][]byte{warm})); err != nil {
		t.Fatal(err)
	}
	b.Poll()
	a.Poll()
	prev := id.EventIDOf(warm)
	const rnodeBudget = 500
	cases := []struct {
		name    string
		schema  string
		payload []byte
		budget  int
	}{
		{"position (ideal)", schemas.ObservationPosition, posPayload, meshtastic.DataPayloadMax},
		{"checkin default (ideal)", schemas.CheckinSent, ciPayload, meshtastic.DataPayloadMax},
		{"marker worst-RU (guarantee)", schemas.MarkerPlaced, mkPayload, rnodeBudget},
		{"checkin worst-RU+SOS (guarantee)", schemas.CheckinSent, ciWorstPayload, rnodeBudget},
	}
	for i, c := range cases {
		f := frame(t, uint64(2+i), &prev, c.schema, c.payload)
		before := len(tapA.sizes)
		if err := a.Send(kernelsync.EncodeFramesMessage(term, [][]byte{f})); err != nil {
			t.Fatal(err)
		}
		b.Poll()
		a.Poll()
		size := tapA.sizes[before]
		t.Logf("%-32s → %3d B warm-compact (budget %d)", c.name, size, c.budget)
		if size > c.budget {
			t.Errorf("%s breaks its tier: %d B > %d B", c.name, size, c.budget)
		}
		prev = id.EventIDOf(f)
	}
}
