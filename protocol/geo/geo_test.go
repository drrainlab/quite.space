package geo

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

func TestPointRoundTripAndBounds(t *testing.T) {
	p, err := FromDegrees(59.33210, 18.04120) // Stockholm
	if err != nil {
		t.Fatal(err)
	}
	buf := AppendPoint(nil, p)
	if len(buf) > 12 {
		t.Fatalf("point too fat on the wire: %d bytes", len(buf))
	}
	d := codec.NewDecoder(buf)
	got, err := ReadPoint(d)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, p)
	}
	// ~1.1 cm resolution: degrees survive to 7 decimals.
	if lat := got.LatDeg(); lat < 59.3320999 || lat > 59.3321001 {
		t.Fatalf("lat drifted: %v", lat)
	}
	if lon := got.LonDeg(); lon < 18.0411999 || lon > 18.0412001 {
		t.Fatalf("lon drifted: %v", lon)
	}
	// Poles and the antimeridian are legal claims.
	for _, c := range [][2]float64{{-90, -180}, {90, 180}, {0, 0}} {
		if _, err := FromDegrees(c[0], c[1]); err != nil {
			t.Fatalf("edge %v refused: %v", c, err)
		}
	}
	// Out of range refused at both boundaries.
	if _, err := FromDegrees(90.1, 0); err == nil {
		t.Fatal("lat out of range accepted")
	}
	if _, err := FromDegrees(0, -180.1); err == nil {
		t.Fatal("lon out of range accepted")
	}
	bad := codec.AppendArray(nil, 2)
	bad = codec.AppendUint(bad, LatMax+1)
	bad = codec.AppendUint(bad, 0)
	if _, err := ReadPoint(codec.NewDecoder(bad)); err == nil {
		t.Fatal("decoder accepted an off-globe point")
	}
}
