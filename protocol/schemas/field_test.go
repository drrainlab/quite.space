// SP-3 field schemas on the wire: a position that must expire, a marker
// that is a historical claim, a check-in whose sos flag is the truth.
package schemas

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/geo"
)

func fieldPoint(t *testing.T) geo.Point {
	t.Helper()
	p, err := geo.FromDegrees(59.3321, 18.0412)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPositionObservationWire(t *testing.T) {
	o := &PositionObservation{Point: fieldPoint(t), AccuracyM: 8, ExpiresAt: 1_790_000_000}
	enc, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// The one-frame citizen: the plan's ≈28-byte estimate must hold.
	if len(enc) > 40 {
		t.Fatalf("position payload too fat: %d bytes", len(enc))
	}
	got, err := DecodePositionObservation(enc)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *o {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// A position that never expires is a claim about the future — refused.
	if _, err := (&PositionObservation{Point: fieldPoint(t)}).Encode(); err == nil {
		t.Fatal("expiry-less position accepted")
	}
	if _, err := (&PositionObservation{Point: fieldPoint(t), ExpiresAt: 1, AccuracyM: MaxPositionAccuracyM + 1}).Encode(); err == nil {
		t.Fatal("absurd accuracy accepted")
	}
	if !Known(ObservationPosition) {
		t.Fatal("observation.position.v1 not registered")
	}
}

func TestPlacedMarkerWire(t *testing.T) {
	m := &PlacedMarker{Text: "мост непроходим", Kind: "hazard", Point: fieldPoint(t), ExpiresAt: 1_790_000_000}
	copy(m.MarkerID[:], []byte("0123456789abcdef"))
	enc, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > 120 {
		t.Fatalf("marker payload too fat: %d bytes", len(enc))
	}
	got, err := DecodePlacedMarker(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != m.Text || got.Kind != m.Kind || got.Point != m.Point ||
		got.MarkerID != m.MarkerID || got.ExpiresAt != m.ExpiresAt {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Timeless markers ("searched") carry no expiry key.
	m.ExpiresAt = 0
	enc2, _ := m.Encode()
	if len(enc2) >= len(enc) {
		t.Fatal("expiry-less marker did not shrink — phantom key")
	}
	cases := []struct {
		name string
		mut  func(*PlacedMarker)
	}{
		{"no text", func(p *PlacedMarker) { p.Text = "" }},
		{"long text", func(p *PlacedMarker) { p.Text = strings.Repeat("я", MaxMarkerTextRunes+1) }},
		{"zero id", func(p *PlacedMarker) { p.MarkerID = [16]byte{} }},
		{"kind not slug", func(p *PlacedMarker) { p.Kind = "Not Slug" }},
		{"off-globe", func(p *PlacedMarker) { p.Point = geo.Point{LatE7U: geo.LatMax + 1} }},
	}
	for _, c := range cases {
		x := *m
		c.mut(&x)
		if _, err := x.Encode(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if !Known(MarkerPlaced) {
		t.Fatal("marker.placed.v1 not registered")
	}
}

func TestCheckinWire(t *testing.T) {
	p := fieldPoint(t)
	c := &Checkin{Text: "✓ check-in", Point: &p, BatteryPct: 67, HasBattery: true}
	enc, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) > 45 {
		t.Fatalf("checkin payload too fat: %d bytes", len(enc))
	}
	got, err := DecodeCheckin(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != c.Text || *got.Point != p || !got.HasBattery || got.BatteryPct != 67 || got.SOS {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// An honest 0% battery is expressible and distinct from "undeclared".
	z := &Checkin{Text: "x", BatteryPct: 0, HasBattery: true}
	ze, _ := z.Encode()
	zg, err := DecodeCheckin(ze)
	if err != nil || !zg.HasBattery || zg.BatteryPct != 0 {
		t.Fatalf("0%% battery lost: %v %+v", err, zg)
	}
	nb := &Checkin{Text: "x"}
	ne, _ := nb.Encode()
	ng, _ := DecodeCheckin(ne)
	if ng.HasBattery {
		t.Fatal("phantom battery declaration")
	}
	// sos=false is never encoded: byte-identical to a plain check-in.
	sf := &Checkin{Text: "x", SOS: false}
	se, _ := sf.Encode()
	if string(se) != string(ne) {
		t.Fatal("sos=false leaked onto the wire")
	}
	// sos=true survives with the author's own words.
	sos := &Checkin{Text: "нужна помощь, повреждена нога", SOS: true}
	soe, _ := sos.Encode()
	sog, err := DecodeCheckin(soe)
	if err != nil || !sog.SOS {
		t.Fatalf("sos lost: %v %+v", err, sog)
	}
	if _, err := (&Checkin{Text: "x", BatteryPct: 101, HasBattery: true}).Encode(); err == nil {
		t.Fatal("battery over 100%% accepted")
	}
	if !Known(CheckinSent) {
		t.Fatal("checkin.sent.v1 not registered")
	}
}
