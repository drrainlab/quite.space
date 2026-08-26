package schemas

// observation.noted.v1 (SP-1) — a HUMAN semantic observation: "заметил
// люфт шпинделя", "тля на грядке 3". Text only, deliberately. The owner's
// line in the sand: observation.noted is the human channel and
// observation.value is the machine channel, and a structured magnitude
// inside a human note would be a second, weaker telemetry protocol born
// by accident. Keys 4+ are UNALLOCATED; if a structured value ever
// returns here it must arrive as the machine form (value+scale+unit),
// never an int64 with an implied scale.
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
)

// NotedObservation is the payload of observation.noted.v1.
type NotedObservation struct {
	Text       string    // key 1, required
	ObjectID   *[16]byte // key 2, optional: the domain object observed
	ObservedAt uint64    // key 3, optional: when it was seen (0 = event CreatedAt)
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
