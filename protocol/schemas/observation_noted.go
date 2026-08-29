package schemas

// observation.noted.v1 (SP-1) — a HUMAN semantic observation: "заметил
// люфт шпинделя", "тля на грядке 3". Text only, deliberately. The owner's
// line in the sand: observation.noted is the human channel and
// observation.value is the machine channel, and a structured magnitude
// inside a human note would be a second, weaker telemetry protocol born
// by accident. Keys 5+ are UNALLOCATED; if a structured value ever
// returns here it must arrive as the machine form (value+scale+unit),
// never an int64 with an implied scale.
//
// Key 4 (SP-3.2 follow-up) is a PHOTO, and it does not cross that line:
// an asset reference is evidence a person points at — "нашёл ботинок,
// вот фото" — not a magnitude a machine measured. ONE asset per
// observation, by decision: an observation is a sentence plus one piece
// of evidence; three photos are three observations. The event stays
// radio-sized (32 reference bytes); the bytes themselves ride the media
// plane and arrive when broadband does, like every asset.
//
// One event, two projections: reducers install it as a quiet feed entry
// AND on the object's bounded timeline (when ObjectID is set). Without
// an ObjectID it is a space-journal note.

import (
	"errors"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// MaxNotedTextRunes bounds the note. Same order as a chat message: an
// observation is a sentence someone typed, not a document.
const MaxNotedTextRunes = 1000

const (
	onKeyText     = 1
	onKeyObject   = 2
	onKeyObserved = 3
	onKeyAsset    = 4
)

// NotedObservation is the payload of observation.noted.v1.
type NotedObservation struct {
	Text       string    // key 1, required
	ObjectID   *[16]byte // key 2, optional: the domain object observed
	ObservedAt uint64    // key 3, optional: when it was seen (0 = event CreatedAt)
	Asset      *[32]byte // key 4, optional: ONE piece of evidence (blob id)
}

func (o *NotedObservation) Encode() ([]byte, error) {
	if o.Text == "" {
		return nil, errors.New("schemas: observation requires text")
	}
	if !utf8.ValidString(o.Text) || utf8.RuneCountInString(o.Text) > MaxNotedTextRunes {
		return nil, errors.New("schemas: observation text invalid or too long")
	}
	n := 1
	if o.ObjectID != nil {
		n++
	}
	if o.ObservedAt != 0 {
		n++
	}
	if o.Asset != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, onKeyText)
	buf = codec.AppendText(buf, o.Text)
	if o.ObjectID != nil {
		buf = codec.AppendUint(buf, onKeyObject)
		buf = codec.AppendBytes(buf, o.ObjectID[:])
	}
	if o.ObservedAt != 0 {
		buf = codec.AppendUint(buf, onKeyObserved)
		buf = codec.AppendUint(buf, o.ObservedAt)
	}
	if o.Asset != nil {
		buf = codec.AppendUint(buf, onKeyAsset)
		buf = codec.AppendBytes(buf, o.Asset[:])
	}
	return buf, nil
}

func DecodeNotedObservation(payload []byte) (*NotedObservation, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	o := &NotedObservation{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case onKeyText:
			o.Text, err = d.ReadText()
		case onKeyObject:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 16 {
					err = errors.New("schemas: object id must be 16 bytes")
				} else {
					var oid [16]byte
					copy(oid[:], b)
					o.ObjectID = &oid
				}
			}
		case onKeyObserved:
			o.ObservedAt, err = d.ReadUint()
		case onKeyAsset:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 32 {
					err = errors.New("schemas: observation asset must be 32 bytes")
				} else {
					var a [32]byte
					copy(a[:], b)
					o.Asset = &a
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
	if o.Text == "" {
		return nil, errors.New("schemas: observation requires text")
	}
	if !utf8.ValidString(o.Text) || utf8.RuneCountInString(o.Text) > MaxNotedTextRunes {
		return nil, errors.New("schemas: observation text invalid or too long")
	}
	return o, nil
}

func init() {
	Register(ObservationNoted, func(p []byte) error { _, err := DecodeNotedObservation(p); return err })
}
