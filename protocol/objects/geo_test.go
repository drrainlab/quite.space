package objects

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/geo"
)

func mustPoint(t *testing.T, lat, lon float64) geo.Point {
	t.Helper()
	p, err := geo.FromDegrees(lat, lon)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecordGeoAndPathRoundTrip(t *testing.T) {
	r := sampleRecord()
	r.Geo = &GeoShape{Point: mustPoint(t, 59.3321, 18.0412), RadiusM: 250}
	r.Path = []geo.Point{
		mustPoint(t, 59.3321, 18.0412),
		mustPoint(t, 59.3400, 18.0500),
		mustPoint(t, 59.3500, 18.0600),
	}
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Geo == nil || *got.Geo != *r.Geo {
		t.Fatalf("geo lost: %+v", got.Geo)
	}
	if len(got.Path) != 3 || got.Path[1] != r.Path[1] {
		t.Fatalf("path lost: %+v", got.Path)
	}
	// A point (radius 0) round-trips as the 2-element form.
	r.Geo = &GeoShape{Point: mustPoint(t, -33.9, 151.2)}
	enc2, _ := r.Encode()
	got2, err := Decode(enc2)
	if err != nil || got2.Geo.RadiusM != 0 {
		t.Fatalf("point form broken: %v %+v", err, got2.Geo)
	}
}

func TestRecordGeoRefusals(t *testing.T) {
	base := sampleRecord()
	cases := []struct {
		name string
		mut  func(*Record)
	}{
		{"off-globe geo", func(r *Record) { r.Geo = &GeoShape{Point: geo.Point{LatE7U: geo.LatMax + 1}} }},
		{"radius too large", func(r *Record) {
			r.Geo = &GeoShape{Point: geo.Point{}, RadiusM: MaxGeoRadiusM + 1}
		}},
		{"single-point path", func(r *Record) { r.Path = []geo.Point{{LatE7U: 1, LonE7U: 1}} }},
		{"over-bound path", func(r *Record) {
			for i := 0; i <= MaxRoutePoints; i++ {
				r.Path = append(r.Path, geo.Point{LatE7U: uint64(i + 1), LonE7U: 1})
			}
		}},
		{"off-globe path point", func(r *Record) {
			r.Path = []geo.Point{{LatE7U: 1, LonE7U: 1}, {LonE7U: geo.LonMax + 1}}
		}},
	}
	for _, c := range cases {
		r := *base
		r.Path = nil
		c.mut(&r)
		if _, err := r.Encode(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// The append-only obligation: a record without geo stays byte-identical
// to its SP-2 encoding, so pre-SP-3 digests stand.
func TestGeolessRecordBytesUnchanged(t *testing.T) {
	r := sampleRecord()
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	r2 := sampleRecord()
	r2.Geo = nil
	r2.Path = nil
	enc2, _ := r2.Encode()
	if string(enc) != string(enc2) {
		t.Fatal("geoless record no longer byte-identical")
	}
	// And RawExtra retention now guards keys ≥ 11.
	r3 := sampleRecord()
	r3.RawExtra = []Extra{{Key: 11, Raw: []byte{0x01}}}
	enc3, err := r3.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc3)
	if err != nil || len(got.RawExtra) != 1 || got.RawExtra[0].Key != 11 {
		t.Fatalf("key-11 passenger lost: %v %+v", err, got.RawExtra)
	}
}
