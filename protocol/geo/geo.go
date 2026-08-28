// Package geo is the wire idiom for geographic claims (SP-3, ADR-031).
//
// A coordinate here is an AUTHOR'S CLAIM about where something is,
// carrying the author's stated accuracy — never a measured truth
// (ADR-008 voice). The map that renders these is a projection of
// knowledge, not a window onto reality.
//
// Encoding: offset-shifted fixed point, no floats on the wire, one
// representation per point (the house codec deliberately has no
// signed-integer encoder, and Magnitude+Negative would give ±0 two
// spellings — a determinism smell):
//
//	lat_e7u = round((lat_deg +  90) × 1e7) ∈ [0, 1_800_000_000]
//	lon_e7u = round((lon_deg + 180) × 1e7) ∈ [0, 3_600_000_000]
//
// ~1.1 cm of resolution; both fit a CBOR uint in ≤ 5 bytes. JSON at the
// node boundary speaks float degrees; the wire never does.
package geo

import (
	"errors"
	"math"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

const (
	// LatMax/LonMax bound the shifted fixed-point ranges.
	LatMax uint64 = 1_800_000_000
	LonMax uint64 = 3_600_000_000
	// e7 is the fixed-point scale.
	e7 = 1e7
)

// Point is one geographic claim in wire units.
type Point struct {
	LatE7U uint64
	LonE7U uint64
}

// Valid reports whether the point is inside the globe.
func (p Point) Valid() bool {
	return p.LatE7U <= LatMax && p.LonE7U <= LonMax
}

// FromDegrees converts JSON-boundary float degrees to a wire point.
func FromDegrees(latDeg, lonDeg float64) (Point, error) {
	if math.IsNaN(latDeg) || math.IsNaN(lonDeg) ||
		latDeg < -90 || latDeg > 90 || lonDeg < -180 || lonDeg > 180 {
		return Point{}, errors.New("geo: degrees out of range")
	}
	return Point{
		LatE7U: uint64(math.Round((latDeg + 90) * e7)),
		LonE7U: uint64(math.Round((lonDeg + 180) * e7)),
	}, nil
}

// LatDeg / LonDeg convert back to degrees for the JSON boundary.
func (p Point) LatDeg() float64 { return float64(p.LatE7U)/e7 - 90 }
func (p Point) LonDeg() float64 { return float64(p.LonE7U)/e7 - 180 }

// AppendPoint emits the wire form: a 2-element array [lat_e7u, lon_e7u]
// (typically 11 bytes).
func AppendPoint(buf []byte, p Point) []byte {
	buf = codec.AppendArray(buf, 2)
	buf = codec.AppendUint(buf, p.LatE7U)
	buf = codec.AppendUint(buf, p.LonE7U)
	return buf
}

// ReadPoint parses and validates a wire point.
func ReadPoint(d *codec.Decoder) (Point, error) {
	n, err := d.ReadArray()
	if err != nil {
		return Point{}, err
	}
	if n != 2 {
		return Point{}, errors.New("geo: point is not a pair")
	}
	var p Point
	if p.LatE7U, err = d.ReadUint(); err != nil {
		return Point{}, err
	}
	if p.LonE7U, err = d.ReadUint(); err != nil {
		return Point{}, err
	}
	if !p.Valid() {
		return Point{}, errors.New("geo: point out of range")
	}
	return p, nil
}
