package schemas

// observation.position.v1 (SP-3) — a person's geographic position as a
// PRESENCE-LIKE claim: "this terminal stated it was here, with this
// accuracy, and the statement expires then". Not a tracker: emission is
// gated by an explicit per-space act, and when the author falls silent
// the claim honestly ages out — the map shows last KNOWN + age, never a
// fabricated present (ADR-031).
//
// Rides the admitted `observation.` radio family; ~28 encoded bytes —
// a single frame on every supported bearer with margin.

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/geo"
)

// MaxPositionAccuracyM bounds the accuracy claim: beyond 60 km the claim
// is not a position.
const MaxPositionAccuracyM = 60_000

const (
	opKeyLat      = 1
	opKeyLon      = 2
	opKeyAccuracy = 3
	opKeyExpires  = 4
	opKeyObserved = 5
)

// PositionObservation is the payload of observation.position.v1.
type PositionObservation struct {
	Point geo.Point
	// AccuracyM is the AUTHOR'S stated accuracy; 0 = undeclared.
	AccuracyM uint64
	// ExpiresAt is the signed TTL the honesty ladder derives from —
	// required, like presence's: a position that never expires would be
	// a claim about the future.
	ExpiresAt uint64
	// ObservedAt optionally states when the fix was taken; 0 = CreatedAt.
	ObservedAt uint64
}

func (o *PositionObservation) validate() error {
	if !o.Point.Valid() {
		return errors.New("schemas: position out of range")
	}
	if o.AccuracyM > MaxPositionAccuracyM {
		return errors.New("schemas: accuracy claim too large")
	}
	if o.ExpiresAt == 0 {
		return errors.New("schemas: position requires expires_at")
	}
	return nil
}

func (o *PositionObservation) Encode() ([]byte, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	n := 3 // lat, lon, expires
	if o.AccuracyM != 0 {
		n++
	}
	if o.ObservedAt != 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, opKeyLat)
	buf = codec.AppendUint(buf, o.Point.LatE7U)
	buf = codec.AppendUint(buf, opKeyLon)
	buf = codec.AppendUint(buf, o.Point.LonE7U)
	if o.AccuracyM != 0 {
		buf = codec.AppendUint(buf, opKeyAccuracy)
		buf = codec.AppendUint(buf, o.AccuracyM)
	}
	buf = codec.AppendUint(buf, opKeyExpires)
	buf = codec.AppendUint(buf, o.ExpiresAt)
	if o.ObservedAt != 0 {
		buf = codec.AppendUint(buf, opKeyObserved)
		buf = codec.AppendUint(buf, o.ObservedAt)
	}
	return buf, nil
}

func DecodePositionObservation(payload []byte) (*PositionObservation, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	o := &PositionObservation{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case opKeyLat:
			o.Point.LatE7U, err = d.ReadUint()
		case opKeyLon:
			o.Point.LonE7U, err = d.ReadUint()
		case opKeyAccuracy:
			o.AccuracyM, err = d.ReadUint()
		case opKeyExpires:
			o.ExpiresAt, err = d.ReadUint()
		case opKeyObserved:
			o.ObservedAt, err = d.ReadUint()
		default:
			err = d.SkipItem()
		}
		if err != nil {
			return nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	return o, nil
}

func init() {
	Register(ObservationPosition, func(p []byte) error { _, err := DecodePositionObservation(p); return err })
}
