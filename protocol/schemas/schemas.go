// Package schemas is the payload Schema Registry (ADR-009): schema ids of
// the form <domain>.<type>.v<N>, each with a validator. Envelopes with
// unknown schema ids are stored and forwarded opaque, never reduced.
package schemas

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Core schema ids, v0.
const (
	MessageText       = "message.text.v1"
	MessageRevised    = "message.revised.v1"
	MessageTombstoned = "message.tombstoned.v1"
	CardCreated       = "card.created.v1"
	CardUpdated       = "card.updated.v1"
	PresenceUpdate    = "presence.update.v1"
	ObservationTemp   = "observation.temperature.v1"
	ReceiptDelivery   = "receipt.delivery.v1"
	DeviceCertified   = "identity.device_certified.v1"
	DeviceRevoked     = "identity.device_revoked.v1"
	ManifestUpdated   = "terminal.manifest.updated.v1"
	MemberJoined      = "membership.joined.v1"
	MemberLeft        = "membership.left.v1"
	MembershipEpoch   = "membership.epoch.v1"
	MemberAdded       = "membership.member_added.v1"
)

var schemaIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.v[1-9][0-9]*$`)

// ValidID reports whether s is a well-formed schema id.
func ValidID(s string) bool { return len(s) <= 128 && schemaIDRe.MatchString(s) }

// Validator structurally checks a payload for its schema.
type Validator func(payload []byte) error

var registry = map[string]Validator{}

// Register adds a validator; overwriting is a programming error.
func Register(schemaID string, v Validator) {
	if !ValidID(schemaID) {
		panic(fmt.Sprintf("schemas: invalid id %q", schemaID))
	}
	if _, dup := registry[schemaID]; dup {
		panic(fmt.Sprintf("schemas: duplicate registration %q", schemaID))
	}
	registry[schemaID] = v
}

// Known reports whether the schema id is registered on this node.
func Known(schemaID string) bool {
	_, ok := registry[schemaID]
	return ok
}

// Validate checks a payload against its schema. Unknown schemas return
// ErrUnknownSchema: the caller stores the event opaque (ADR-009), it never
// treats unknown as invalid.
var ErrUnknownSchema = errors.New("schemas: unknown schema id")

func Validate(schemaID string, payload []byte) error {
	v, ok := registry[schemaID]
	if !ok {
		return ErrUnknownSchema
	}
	return v(payload)
}

// ---- Core payload types ----

// TextMessage is message.text.v1: {1: text, 2?: reply_to event id}.
type TextMessage struct {
	Text    string
	ReplyTo *id.EventID
}

// MaxTextLen bounds a chat message payload.
const MaxTextLen = 16 * 1024

func (t *TextMessage) Encode() ([]byte, error) {
	if t.Text == "" || len(t.Text) > MaxTextLen {
		return nil, errors.New("schemas: text empty or too long")
	}
	n := 1
	if t.ReplyTo != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, t.Text)
	if t.ReplyTo != nil {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendBytes(buf, t.ReplyTo[:])
	}
	return buf, nil
}

func DecodeTextMessage(payload []byte) (*TextMessage, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	t := &TextMessage{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			t.Text, err = d.ReadText()
		case 2:
			var b []byte
			b, err = d.ReadBytes()
			if err == nil {
				if len(b) != id.Size {
					return nil, errors.New("schemas: reply_to must be 32 bytes")
				}
				var e id.EventID
				copy(e[:], b)
				t.ReplyTo = &e
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
	if t.Text == "" || len(t.Text) > MaxTextLen {
		return nil, errors.New("schemas: text empty or too long")
	}
	return t, nil
}

// Tombstone is message.tombstoned.v1 / card.* deletion: {1: target}.
type Tombstone struct{ Target id.EventID }

func (t *Tombstone) Encode() []byte {
	buf := codec.AppendMap(nil, 1)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendBytes(buf, t.Target[:])
	return buf
}

func DecodeTombstone(payload []byte) (*Tombstone, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	t := &Tombstone{}
	seen := false
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if k == 1 {
			b, e := d.ReadBytes()
			if e != nil {
				return nil, e
			}
			if len(b) != id.Size {
				return nil, errors.New("schemas: target must be 32 bytes")
			}
			copy(t.Target[:], b)
			seen = true
		} else if err := d.SkipItem(); err != nil {
			return nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if !seen {
		return nil, errors.New("schemas: tombstone missing target")
	}
	return t, nil
}

// Card is card.created.v1 / card.updated.v1 (plan §6.3 Object Block):
// {1: title, 2: status, 3?: assignee principal, 4?: origin event, 5?: card id}.
type Card struct {
	Title    string
	Status   string // open | done | dropped
	Assignee *id.PrincipalID
	Origin   *id.EventID // message this card was made from
	Card     *id.EventID // for updates: the card.created event
}

func (c *Card) Encode() ([]byte, error) {
	if c.Title == "" || len(c.Title) > 512 {
		return nil, errors.New("schemas: card title empty or too long")
	}
	switch c.Status {
	case "open", "done", "dropped":
	default:
		return nil, fmt.Errorf("schemas: bad card status %q", c.Status)
	}
	n := 2
	if c.Assignee != nil {
		n++
	}
	if c.Origin != nil {
		n++
	}
	if c.Card != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, c.Title)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, c.Status)
	if c.Assignee != nil {
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendBytes(buf, c.Assignee[:])
	}
	if c.Origin != nil {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendBytes(buf, c.Origin[:])
	}
	if c.Card != nil {
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendBytes(buf, c.Card[:])
	}
	return buf, nil
}

func DecodeCard(payload []byte) (*Card, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	c := &Card{}
	read32 := func() ([]byte, error) {
		b, err := d.ReadBytes()
		if err != nil {
			return nil, err
		}
		if len(b) != id.Size {
			return nil, errors.New("schemas: id field must be 32 bytes")
		}
		return b, nil
	}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			c.Title, err = d.ReadText()
		case 2:
			c.Status, err = d.ReadText()
		case 3:
			var b []byte
			if b, err = read32(); err == nil {
				var p id.PrincipalID
				copy(p[:], b)
				c.Assignee = &p
			}
		case 4:
			var b []byte
			if b, err = read32(); err == nil {
				var e id.EventID
				copy(e[:], b)
				c.Origin = &e
			}
		case 5:
			var b []byte
			if b, err = read32(); err == nil {
				var e id.EventID
				copy(e[:], b)
				c.Card = &e
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
	if c.Title == "" {
		return nil, errors.New("schemas: card missing title")
	}
	return c, nil
}

// PresencePayload is presence.update.v1: {1: state, 2: expires_at}.
// EmittedAt comes from the envelope, not the payload.
type PresencePayload struct {
	State     string
	ExpiresAt uint64
}

func (p *PresencePayload) Encode() ([]byte, error) {
	if p.State == "" || len(p.State) > 64 {
		return nil, errors.New("schemas: presence state empty or too long")
	}
	if p.ExpiresAt == 0 {
		return nil, errors.New("schemas: presence requires expires_at (plan §8.3)")
	}
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, p.State)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendUint(buf, p.ExpiresAt)
	return buf, nil
}

func DecodePresence(payload []byte) (*PresencePayload, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &PresencePayload{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			p.State, err = d.ReadText()
		case 2:
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
	if p.State == "" || p.ExpiresAt == 0 {
		return nil, errors.New("schemas: presence missing state or expiry")
	}
	return p, nil
}

// Observation is observation.temperature.v1 (plan §9, minimal): values are
// scaled integers — no floats in signed structures (ADR-003).
// {1: centi_value (int as uint with sign key), 2: negative flag, 3: unit,
//
//	4: observed_at, 5: stale_after_seconds, 6: simulated}.
type Observation struct {
	CentiValue uint64 // absolute value, hundredths of a unit
	Negative   bool
	Unit       string // "celsius"
	ObservedAt uint64
	StaleAfter uint64 // seconds; freshness honesty (plan §9 quality)
	Simulated  bool   // must be declared, never inferred
}

func (o *Observation) Encode() ([]byte, error) {
	if o.Unit == "" {
		return nil, errors.New("schemas: observation requires unit")
	}
	if o.ObservedAt == 0 {
		return nil, errors.New("schemas: observation requires observed_at")
	}
	if o.StaleAfter == 0 {
		return nil, errors.New("schemas: observation requires stale_after (plan §9)")
	}
	n := 4
	if o.Negative {
		n++
	}
	if o.Simulated {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendUint(buf, o.CentiValue)
	if o.Negative {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendBool(buf, true)
	}
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendText(buf, o.Unit)
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendUint(buf, o.ObservedAt)
	buf = codec.AppendUint(buf, 5)
	buf = codec.AppendUint(buf, o.StaleAfter)
	if o.Simulated {
		buf = codec.AppendUint(buf, 6)
		buf = codec.AppendBool(buf, true)
	}
	return buf, nil
}

func DecodeObservation(payload []byte) (*Observation, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	o := &Observation{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			o.CentiValue, err = d.ReadUint()
		case 2:
			o.Negative, err = d.ReadBool()
		case 3:
			o.Unit, err = d.ReadText()
		case 4:
			o.ObservedAt, err = d.ReadUint()
		case 5:
			o.StaleAfter, err = d.ReadUint()
		case 6:
			o.Simulated, err = d.ReadBool()
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
	if o.Unit == "" || o.ObservedAt == 0 || o.StaleAfter == 0 {
		return nil, errors.New("schemas: observation missing required fields")
	}
	return o, nil
}

// ---- membership.epoch.v1 ----

// EpochWrap is one member device's encrypted copy of an epoch key.
type EpochWrap struct {
	Device id.DeviceID
	Enc    []byte // HPKE encapsulated key
	CT     []byte // sealed epoch key
}

// EpochPayload is membership.epoch.v1: {1: epoch n, 2: wraps}.
type EpochPayload struct {
	N     uint64
	Wraps []EpochWrap
}

func (e *EpochPayload) Encode() ([]byte, error) {
	if e.N == 0 {
		return nil, errors.New("schemas: epoch numbers start at 1")
	}
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendUint(buf, e.N)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendArray(buf, len(e.Wraps))
	for _, w := range e.Wraps {
		buf = codec.AppendArray(buf, 3)
		buf = codec.AppendBytes(buf, w.Device[:])
		buf = codec.AppendBytes(buf, w.Enc)
		buf = codec.AppendBytes(buf, w.CT)
	}
	return buf, nil
}

func DecodeEpochPayload(payload []byte) (*EpochPayload, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	e := &EpochPayload{}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			e.N, er = d.ReadUint()
		case 2:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				if _, er = d.ReadArray(); er != nil {
					return nil, er
				}
				var w EpochWrap
				var devBytes []byte
				devBytes, er = d.ReadBytes()
				if er != nil {
					return nil, er
				}
				if len(devBytes) != id.Size {
					return nil, errors.New("schemas: bad wrap device id")
				}
				copy(w.Device[:], devBytes)
				var enc, ct []byte
				if enc, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				if ct, er = d.ReadBytes(); er != nil {
					return nil, er
				}
				w.Enc = append([]byte(nil), enc...)
				w.CT = append([]byte(nil), ct...)
				e.Wraps = append(e.Wraps, w)
			}
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if e.N == 0 {
		return nil, errors.New("schemas: epoch payload missing epoch number")
	}
	return e, nil
}

// MemberAddedPayload is membership.member_added.v1 — the canonical in-log
// event through which ALL participants learn a new member joined via a pass
// (ADR-012). {1: device, 2: name, 3: accepted_by principal, 4: accepted_at}.
type MemberAddedPayload struct {
	Device     id.DeviceID
	Name       string
	AcceptedBy id.PrincipalID
	AcceptedAt uint64
}

func (m *MemberAddedPayload) Encode() []byte {
	buf := codec.AppendMap(nil, 4)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendBytes(buf, m.Device[:])
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, m.Name)
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendBytes(buf, m.AcceptedBy[:])
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendUint(buf, m.AcceptedAt)
	return buf
}

func DecodeMemberAdded(payload []byte) (*MemberAddedPayload, error) {
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	m := &MemberAddedPayload{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case 1:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil && len(b) == id.Size {
				copy(m.Device[:], b)
			}
		case 2:
			m.Name, er = d.ReadText()
		case 3:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil && len(b) == id.Size {
				copy(m.AcceptedBy[:], b)
			}
		case 4:
			m.AcceptedAt, er = d.ReadUint()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	return m, nil
}

func init() {
	Register(MessageText, func(p []byte) error { _, err := DecodeTextMessage(p); return err })
	Register(MemberAdded, func(p []byte) error { _, err := DecodeMemberAdded(p); return err })
	Register(MessageRevised, func(p []byte) error { _, err := DecodeTextMessage(p); return err })
	Register(MessageTombstoned, func(p []byte) error { _, err := DecodeTombstone(p); return err })
	Register(CardCreated, func(p []byte) error { _, err := DecodeCard(p); return err })
	Register(CardUpdated, func(p []byte) error { _, err := DecodeCard(p); return err })
	Register(PresenceUpdate, func(p []byte) error { _, err := DecodePresence(p); return err })
	Register(ObservationTemp, func(p []byte) error { _, err := DecodeObservation(p); return err })
	Register(MembershipEpoch, func(p []byte) error { _, err := DecodeEpochPayload(p); return err })
}
