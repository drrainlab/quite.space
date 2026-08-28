package schemas

// sweep.completed.v1 (SP-3.2, ADR-034) — THE CANONICAL COMPLETION FACT
// of a recording session. The Sweep Object is the container of identity
// and relations; its status slug is a render cache, and where the two
// disagree, this event wins. That way finishing an operation never
// depends on how large its Object has grown — an Object thick with
// observations and edges would otherwise make its own completion
// unsendable on the day it matters most.
//
// The detailed trajectory rides SEPARATELY, as an attached asset named
// here by its 32-byte content id: LoRa carries what happened, broadband
// carries the full how. Raw bytes, not hex — half the size, and the
// biggest single item in the payload.
//
// Result is a CLOSED vocabulary. The wire never parses prose: the
// operator's own words travel as an observation on the sector, a
// different event with a different job. `undeclared` is the honest slug
// for "Stop was pressed, judgement not yet given" — the fact of
// completion (times, distance, track) is known and travels at once
// rather than waiting for somebody to reach a keyboard; `interrupted`
// means the recording ended without anyone pressing Stop, and a linked
// task deliberately stays open for it.
//
// Key 1 is the universal text fallback (the block.* convention).

import (
	"errors"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/geo"
)

// MaxSweepFallbackRunes bounds the fallback sentence — MEASURED against
// the one-frame budgets, not chosen; see TestSweepCompletedBudgets
// (transports/compact). Cyrillic runes cost two UTF-8 bytes.
const MaxSweepFallbackRunes = 40

// The closed result vocabulary.
const (
	SweepNothingFound = "nothing_found"
	SweepFound        = "found"
	SweepInterrupted  = "interrupted"
	SweepUndeclared   = "undeclared"
)

const (
	scKeyFallback  = 1
	scKeyObject    = 2
	scKeyStartedAt = 3
	scKeyEndedAt   = 4
	scKeyDistanceM = 5
	scKeyResult    = 6
	scKeyBBox      = 7
	scKeyAsset     = 8
)

// CompletedSweep is the payload of sweep.completed.v1.
type CompletedSweep struct {
	Fallback  string   // required — the sentence an old client renders
	ObjectID  [16]byte // the Sweep Object this fact is about
	StartedAt uint64   // deliberate copy: a LoRa-only receiver needs
	// nothing else to render the sentence. It is not a second opinion
	// about the start — the Object's creation owns "this sweep began";
	// this event owns "this is how it ended".
	EndedAt   uint64
	DistanceM uint64
	Result    string // closed vocabulary above
	// BBox: min then max corner of the recorded area.
	BBoxMin geo.Point
	BBoxMax geo.Point
	// TrackAsset is the field.track.v1 asset's 32-byte content id. The
	// ref+key ride separately in the block.attached.v1 carrier — an
	// AssetRef inside this event would be invisible to the asset index,
	// which is gated on the block. schema prefix.
	TrackAsset [32]byte
}

func sweepResultOK(s string) bool {
	switch s {
	case SweepNothingFound, SweepFound, SweepInterrupted, SweepUndeclared:
		return true
	}
	return false
}

func (c *CompletedSweep) validate() error {
	if c.Fallback == "" {
		return errors.New("schemas: sweep requires a fallback sentence")
	}
	if !utf8.ValidString(c.Fallback) || utf8.RuneCountInString(c.Fallback) > MaxSweepFallbackRunes {
		return errors.New("schemas: sweep fallback invalid or too long")
	}
	if c.ObjectID == ([16]byte{}) {
		return errors.New("schemas: sweep requires its object id")
	}
	if c.StartedAt == 0 || c.EndedAt == 0 || c.EndedAt < c.StartedAt {
		return errors.New("schemas: sweep times missing or reversed")
	}
	if !sweepResultOK(c.Result) {
		return errors.New("schemas: sweep result not in the vocabulary")
	}
	if !c.BBoxMin.Valid() || !c.BBoxMax.Valid() {
		return errors.New("schemas: sweep bbox out of range")
	}
	if c.TrackAsset == ([32]byte{}) {
		return errors.New("schemas: sweep requires its track asset id")
	}
	return nil
}

func (c *CompletedSweep) Encode() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	buf := codec.AppendMap(nil, 8)
	buf = codec.AppendUint(buf, scKeyFallback)
	buf = codec.AppendText(buf, c.Fallback)
	buf = codec.AppendUint(buf, scKeyObject)
	buf = codec.AppendBytes(buf, c.ObjectID[:])
	buf = codec.AppendUint(buf, scKeyStartedAt)
	buf = codec.AppendUint(buf, c.StartedAt)
	buf = codec.AppendUint(buf, scKeyEndedAt)
	buf = codec.AppendUint(buf, c.EndedAt)
	buf = codec.AppendUint(buf, scKeyDistanceM)
	buf = codec.AppendUint(buf, c.DistanceM)
	buf = codec.AppendUint(buf, scKeyResult)
	buf = codec.AppendText(buf, c.Result)
	buf = codec.AppendUint(buf, scKeyBBox)
	buf = codec.AppendArray(buf, 4)
	buf = codec.AppendUint(buf, c.BBoxMin.LatE7U)
	buf = codec.AppendUint(buf, c.BBoxMin.LonE7U)
	buf = codec.AppendUint(buf, c.BBoxMax.LatE7U)
	buf = codec.AppendUint(buf, c.BBoxMax.LonE7U)
	buf = codec.AppendUint(buf, scKeyAsset)
	buf = codec.AppendBytes(buf, c.TrackAsset[:])
	return buf, nil
}

func DecodeCompletedSweep(payload []byte) (*CompletedSweep, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	c := &CompletedSweep{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case scKeyFallback:
			c.Fallback, err = d.ReadText()
		case scKeyObject:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 16 {
					err = errors.New("schemas: sweep object id must be 16 bytes")
				} else {
					copy(c.ObjectID[:], b)
				}
			}
		case scKeyStartedAt:
			c.StartedAt, err = d.ReadUint()
		case scKeyEndedAt:
			c.EndedAt, err = d.ReadUint()
		case scKeyDistanceM:
			c.DistanceM, err = d.ReadUint()
		case scKeyResult:
			c.Result, err = d.ReadText()
		case scKeyBBox:
			var n int
			if n, err = d.ReadArray(); err == nil {
				if n != 4 {
					err = errors.New("schemas: sweep bbox must be 4 coordinates")
				} else {
					for i, dst := range []*uint64{&c.BBoxMin.LatE7U, &c.BBoxMin.LonE7U,
						&c.BBoxMax.LatE7U, &c.BBoxMax.LonE7U} {
						if *dst, err = d.ReadUint(); err != nil {
							_ = i
							break
						}
					}
				}
			}
		case scKeyAsset:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 32 {
					err = errors.New("schemas: sweep track asset id must be 32 bytes")
				} else {
					copy(c.TrackAsset[:], b)
				}
			}
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
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func init() {
	Register(SweepCompleted, func(p []byte) error { _, err := DecodeCompletedSweep(p); return err })
}
