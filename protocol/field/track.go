// Package field holds the Field's ASSET CONTENT formats (SP-3.2,
// ADR-034) — binary shapes that live inside an asset's bytes, never on
// the wire as an event payload. That is why nothing here calls
// schemas.Register: that registry admits envelope payloads, and a track
// travels as an attached asset with a media type, exactly so that its
// size can never crowd a radio frame (the Route/C3 lesson).
package field

// field.track.v1 — the canonical recorded trajectory of one sweep.
//
// A GAP IS A SAMPLE, NOT A SILENCE. A bare list of points cannot tell
// "GPS was genuinely absent" from "the device slept" from "two fixes
// happened to be far apart" — four different claims rendered the same,
// forcing the reader to guess, which is what the never-interpolate law
// forbids (ADR-031 extended to trajectories). Here the gap is an item
// the reader must CONSUME: there is no absence to overlook, there is a
// thing in the stream saying "here I did not know". A renderer that
// iterates samples cannot join across one by accident.
//
// The recorder never invents a cause: reason may be "unknown", and a
// recorder that does not know why it stopped hearing says so.
//
// GPX / GeoJSON / CSV are EXPORT PROJECTIONS of this form, free per
// ADR-033 ("what this device decides for itself is free"); this format
// is the storage truth and holds still.

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/geo"
)

// MediaType is the asset media type field.track.v1 travels under.
const MediaType = "application/vnd.quiet.track.v1"

// Track key table — append-only forever.
const (
	trKeyStartedAt = 1
	trKeySamples   = 2

	// maxKnownTrackKey: higher keys ride RawExtra verbatim, so an older
	// build re-sealing a newer track cannot strip a field it does not
	// understand.
	maxKnownTrackKey = trKeySamples
)

// Sample tags. A sample is a CBOR array whose element 0 is the tag.
// Unknown tags are preserved opaquely and NEVER rendered as points.
const (
	SampleQPoint = 1
	SampleQGap   = 2
)

// Gap reasons — a closed vocabulary, because the reason is a claim.
const (
	GapNoFix     = 1 // the provider said so (never inferred from silence)
	GapSuspended = 2 // the recorder's own lifecycle covered the span
	GapUnknown   = 3 // the honest default: something was missed, cause unproven
)

// Bounds.
const (
	// MaxTrackSamples bounds one track (~8 h at a 5 s cadence). A sweep
	// is a bounded session; a track that big is two sweeps.
	MaxTrackSamples = 6000
	// MaxTrackRawExtra bounds unknown-key passengers (the objects.Record
	// budget, same reasoning).
	MaxTrackRawExtra = 4 << 10
)

// Sample is one element of the stream: exactly one of Point or Gap, or
// an unknown tag carried raw.
type Sample struct {
	Tag uint64
	// Point (Tag == SampleQPoint):
	DtMS      uint64 // delta from the previous sample; first from StartedAt
	Point     geo.Point
	AccuracyM uint64
	// Gap (Tag == SampleQGap): DtMS as above, plus
	DurationMS uint64
	Reason     uint64
	// Unknown tags: the whole sample array, one canonical CBOR item.
	Raw []byte
}

// Extra is an unknown top-level key carried through a re-seal.
type Extra struct {
	Key uint64
	Raw []byte
}

// Track is the decoded form.
type Track struct {
	StartedAt uint64
	Samples   []Sample
	RawExtra  []Extra
}

// Validate enforces the format's own honesty. Called by Encode AND
// Decode — a track that would be refused at sealing is refused at
// reading, one truth (the objects.Record discipline).
func (t *Track) Validate() error {
	if t.StartedAt == 0 {
		return errors.New("field: track requires started_at")
	}
	if len(t.Samples) > MaxTrackSamples {
		return fmt.Errorf("field: track of %d samples exceeds %d", len(t.Samples), MaxTrackSamples)
	}
	// An EMPTY samples list is valid: a sweep of pure gaps — started,
	// heard nothing, stopped — is an honest record, not an error.
	for i, s := range t.Samples {
		switch s.Tag {
		case SampleQPoint:
			if !s.Point.Valid() {
				return fmt.Errorf("field: sample %d point out of range", i)
			}
		case SampleQGap:
			if s.DurationMS == 0 {
				return fmt.Errorf("field: sample %d gap of zero duration", i)
			}
			switch s.Reason {
			case GapNoFix, GapSuspended, GapUnknown:
			default:
				return fmt.Errorf("field: sample %d gap reason %d not in the vocabulary", i, s.Reason)
			}
		default:
			if len(s.Raw) == 0 {
				return fmt.Errorf("field: sample %d has unknown tag %d and no raw form", i, s.Tag)
			}
		}
	}
	return nil
}

// Encode seals the track into canonical CBOR.
func (t *Track) Encode() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	extra := retainExtra(t.RawExtra)
	buf := codec.AppendMap(nil, 2+len(extra))
	buf = codec.AppendUint(buf, trKeyStartedAt)
	buf = codec.AppendUint(buf, t.StartedAt)
	buf = codec.AppendUint(buf, trKeySamples)
	buf = codec.AppendArray(buf, len(t.Samples))
	for _, s := range t.Samples {
		switch s.Tag {
		case SampleQPoint:
			buf = codec.AppendArray(buf, 5)
			buf = codec.AppendUint(buf, SampleQPoint)
			buf = codec.AppendUint(buf, s.DtMS)
			buf = codec.AppendUint(buf, s.Point.LatE7U)
			buf = codec.AppendUint(buf, s.Point.LonE7U)
			buf = codec.AppendUint(buf, s.AccuracyM)
		case SampleQGap:
			buf = codec.AppendArray(buf, 4)
			buf = codec.AppendUint(buf, SampleQGap)
			buf = codec.AppendUint(buf, s.DtMS)
			buf = codec.AppendUint(buf, s.DurationMS)
			buf = codec.AppendUint(buf, s.Reason)
		default:
			// Preserved verbatim: element 0 of Raw is its own tag.
			buf = append(buf, s.Raw...)
		}
	}
	for _, e := range extra {
		buf = codec.AppendUint(buf, e.Key)
		buf = append(buf, e.Raw...)
	}
	return buf, nil
}

// Decode opens a sealed track.
func Decode(data []byte) (*Track, error) {
	d := codec.NewDecoder(data)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	t := &Track{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case trKeyStartedAt:
			if t.StartedAt, err = d.ReadUint(); err != nil {
				return nil, err
			}
		case trKeySamples:
			n, err := d.ReadArray()
			if err != nil {
				return nil, err
			}
			if n > MaxTrackSamples {
				return nil, fmt.Errorf("field: track of %d samples exceeds %d", n, MaxTrackSamples)
			}
			t.Samples = make([]Sample, 0, n)
			for i := 0; i < n; i++ {
				s, err := decodeSample(d)
				if err != nil {
					return nil, fmt.Errorf("field: sample %d: %w", i, err)
				}
				t.Samples = append(t.Samples, s)
			}
		default:
			if k <= maxKnownTrackKey {
				if err := d.SkipItem(); err != nil {
					return nil, err
				}
				continue
			}
			raw, err := d.ReadRawItem()
			if err != nil {
				return nil, err
			}
			t.RawExtra = append(t.RawExtra, Extra{Key: k, Raw: raw})
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

func decodeSample(d *codec.Decoder) (Sample, error) {
	// Peek by re-reading raw: an unknown tag must be carried verbatim,
	// and only the raw item preserves its exact bytes.
	raw, err := d.ReadRawItem()
	if err != nil {
		return Sample{}, err
	}
	sd := codec.NewDecoder(raw)
	n, err := sd.ReadArray()
	if err != nil {
		return Sample{}, err
	}
	if n < 1 {
		return Sample{}, errors.New("empty sample array")
	}
	tag, err := sd.ReadUint()
	if err != nil {
		return Sample{}, err
	}
	s := Sample{Tag: tag}
	switch tag {
	case SampleQPoint:
		if n != 5 {
			return Sample{}, fmt.Errorf("point sample of arity %d", n)
		}
		if s.DtMS, err = sd.ReadUint(); err != nil {
			return Sample{}, err
		}
		if s.Point.LatE7U, err = sd.ReadUint(); err != nil {
			return Sample{}, err
		}
		if s.Point.LonE7U, err = sd.ReadUint(); err != nil {
			return Sample{}, err
		}
		if s.AccuracyM, err = sd.ReadUint(); err != nil {
			return Sample{}, err
		}
	case SampleQGap:
		if n != 4 {
			return Sample{}, fmt.Errorf("gap sample of arity %d", n)
		}
		if s.DtMS, err = sd.ReadUint(); err != nil {
			return Sample{}, err
		}
		if s.DurationMS, err = sd.ReadUint(); err != nil {
			return Sample{}, err
		}
		if s.Reason, err = sd.ReadUint(); err != nil {
			return Sample{}, err
		}
	default:
		// Unknown tag: keep the whole item. Forward compatibility is a
		// promise about PRESERVATION, not understanding — and it is
		// never rendered as a point.
		s.Raw = raw
		return s, nil
	}
	if err := sd.Done(); err != nil {
		return Sample{}, err
	}
	return s, nil
}

func retainExtra(list []Extra) []Extra {
	if len(list) == 0 {
		return nil
	}
	out := make([]Extra, 0, len(list))
	var last uint64
	var total int
	for _, e := range list {
		if e.Key <= maxKnownTrackKey || e.Key <= last || len(e.Raw) == 0 {
			continue
		}
		total += len(e.Raw)
		if total > MaxTrackRawExtra {
			break
		}
		out = append(out, e)
		last = e.Key
	}
	return out
}
