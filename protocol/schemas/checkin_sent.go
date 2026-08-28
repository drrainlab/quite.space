package schemas

// checkin.sent.v1 (SP-3) — a durable CONTACT FACT: "I was in touch, I'm
// ok" (optionally: where, with how much battery). Custodied — a check-in
// must arrive; it is a fact, not a state, so it carries no expiry.
//
// SOS is a flag, and the flag is the TRUTH: protocol consumers MUST key
// on `sos`, never infer emergency state from the fallback text — the
// wire does not parse the meaning of Unicode prose (ADR-031). Text is
// exclusively the human fallback; the node's emit path defaults it to an
// emergency-semantic string when sos is set and the author wrote
// nothing. An author's own words ("нужна помощь, повреждена нога") with
// sos=true are a perfectly good SOS.
//
// Battery lives here deliberately: a check-in is a structured,
// machine-ish radio event — not the human prose channel observation.
// noted.v1 protects (its line in the sand stands).

import (
	"errors"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/geo"
)

// MaxCheckinTextRunes bounds the note at 120 runes — the same measured
// two-tier law as the marker label (ADR-031): a Cyrillic note at 200
// runes would breach the one-RNode-frame guarantee every field event
// carries.
const MaxCheckinTextRunes = 120

const (
	ciKeyText    = 1
	ciKeyGeo     = 2
	ciKeyBattery = 3
	ciKeySOS     = 4
)

// Checkin is the payload of checkin.sent.v1.
type Checkin struct {
	Text  string // required — note, doubles as the fallback
	Point *geo.Point
	// BatteryPct 0-100; presence of the key IS the declaration
	// (HasBattery), so an honest 0% stays expressible.
	BatteryPct uint64
	HasBattery bool
	// SOS: encoded only when true — one representation.
	SOS bool
}

func (c *Checkin) validate() error {
	if c.Text == "" {
		return errors.New("schemas: checkin requires text")
	}
	if !utf8.ValidString(c.Text) || utf8.RuneCountInString(c.Text) > MaxCheckinTextRunes {
		return errors.New("schemas: checkin text invalid or too long")
	}
	if c.Point != nil && !c.Point.Valid() {
		return errors.New("schemas: checkin point out of range")
	}
	if c.HasBattery && c.BatteryPct > 100 {
		return errors.New("schemas: battery over 100%")
	}
	return nil
}

func (c *Checkin) Encode() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	n := 1
	if c.Point != nil {
		n++
	}
	if c.HasBattery {
		n++
	}
	if c.SOS {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, ciKeyText)
	buf = codec.AppendText(buf, c.Text)
	if c.Point != nil {
		buf = codec.AppendUint(buf, ciKeyGeo)
		buf = geo.AppendPoint(buf, *c.Point)
	}
	if c.HasBattery {
		buf = codec.AppendUint(buf, ciKeyBattery)
		buf = codec.AppendUint(buf, c.BatteryPct)
	}
	if c.SOS {
		buf = codec.AppendUint(buf, ciKeySOS)
		buf = codec.AppendBool(buf, true)
	}
	return buf, nil
}

func DecodeCheckin(payload []byte) (*Checkin, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	c := &Checkin{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case ciKeyText:
			c.Text, err = d.ReadText()
		case ciKeyGeo:
			var p geo.Point
			if p, err = geo.ReadPoint(d); err == nil {
				c.Point = &p
			}
		case ciKeyBattery:
			c.BatteryPct, err = d.ReadUint()
			c.HasBattery = true
		case ciKeySOS:
			c.SOS, err = d.ReadBool()
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
	Register(CheckinSent, func(p []byte) error { _, err := DecodeCheckin(p); return err })
}
