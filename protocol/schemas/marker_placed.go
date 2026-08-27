package schemas

// marker.placed.v1 (SP-3) — a signed claim ABOUT A PLACE: "searched",
// "hazard", "camp". Structured, so it is not observation.noted (which
// stays the pure human text channel — the SP-1 line in the sand holds).
//
// A v1 marker is IMMUTABLE — a historical claim, and the UI has no
// right to read it as an eternal present. The optional expires_at is a
// PAYLOAD field for display honesty only ("this hazard claim stops
// counting as active then"), never the envelope's expiry — a custodied
// marker must still arrive and stay in the log: «мост был опасен в
// 14:32» is a different sentence from «мост опасен сейчас», and both
// deserve to survive (ADR-031).
//
// Key 1 is the universal text fallback (the block.* convention): an old
// node renders the label, not a mystery.

import (
	"errors"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/geo"
)

// MaxMarkerTextRunes bounds the label at 120 runes — MEASURED, not
// chosen: the two-tier radio law (ADR-031) guarantees every field event
// fits one RNode frame (500 B), and a Cyrillic label rune costs two
// UTF-8 bytes; 200 runes would breach the frame. The UI hints shorter
// still (~9 Cyrillic runes) for the single-Meshtastic-frame ideal — a
// longer label honestly rides two frames and blocks nothing.
const MaxMarkerTextRunes = 120

const (
	mpKeyText    = 1
	mpKeyID      = 2
	mpKeyKind    = 3
	mpKeyGeo     = 4
	mpKeyObject  = 5
	mpKeyExpires = 6
)

// PlacedMarker is the payload of marker.placed.v1.
type PlacedMarker struct {
	Text string // required — label, doubles as the fallback
	// MarkerID: dedupe handle now, tombstone/move target in a future v2.
	MarkerID [16]byte
	// Kind is a FREE slug (hazard/searched/camp/waypoint/… — UI
	// suggestions, never a protocol vocabulary).
	Kind  string
	Point geo.Point
	// ObjectID optionally pins the marker to an object/place.
	ObjectID *[16]byte
	// ExpiresAt: when this claim stops counting as ACTIVE (0 = never —
	// "searched" is timeless; a temporary hazard expires). Display
	// honesty only; see the package comment.
	ExpiresAt uint64
}

func (p *PlacedMarker) validate() error {
	if p.Text == "" {
		return errors.New("schemas: marker requires text")
	}
	if !utf8.ValidString(p.Text) || utf8.RuneCountInString(p.Text) > MaxMarkerTextRunes {
		return errors.New("schemas: marker text invalid or too long")
	}
	if p.MarkerID == ([16]byte{}) {
		return errors.New("schemas: marker requires an id")
	}
	if !markerSlugOK(p.Kind) {
		return errors.New("schemas: marker kind is not a slug")
	}
	if !p.Point.Valid() {
		return errors.New("schemas: marker point out of range")
	}
	return nil
}

func markerSlugOK(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (p *PlacedMarker) Encode() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	n := 4 // text, id, kind, geo
	if p.ObjectID != nil {
		n++
	}
	if p.ExpiresAt != 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, mpKeyText)
	buf = codec.AppendText(buf, p.Text)
	buf = codec.AppendUint(buf, mpKeyID)
	buf = codec.AppendBytes(buf, p.MarkerID[:])
	buf = codec.AppendUint(buf, mpKeyKind)
	buf = codec.AppendText(buf, p.Kind)
	buf = codec.AppendUint(buf, mpKeyGeo)
	buf = geo.AppendPoint(buf, p.Point)
	if p.ObjectID != nil {
		buf = codec.AppendUint(buf, mpKeyObject)
		buf = codec.AppendBytes(buf, p.ObjectID[:])
	}
	if p.ExpiresAt != 0 {
		buf = codec.AppendUint(buf, mpKeyExpires)
		buf = codec.AppendUint(buf, p.ExpiresAt)
	}
	return buf, nil
}

func DecodePlacedMarker(payload []byte) (*PlacedMarker, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &PlacedMarker{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case mpKeyText:
			p.Text, err = d.ReadText()
		case mpKeyID:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 16 {
					err = errors.New("schemas: marker id must be 16 bytes")
				} else {
					copy(p.MarkerID[:], b)
				}
			}
		case mpKeyKind:
			p.Kind, err = d.ReadText()
		case mpKeyGeo:
			p.Point, err = geo.ReadPoint(d)
		case mpKeyObject:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 16 {
					err = errors.New("schemas: object id must be 16 bytes")
				} else {
					var oid [16]byte
					copy(oid[:], b)
					p.ObjectID = &oid
				}
			}
		case mpKeyExpires:
			p.ExpiresAt, err = d.ReadUint()
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
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func init() {
	Register(MarkerPlaced, func(p []byte) error { _, err := DecodePlacedMarker(p); return err })
}
