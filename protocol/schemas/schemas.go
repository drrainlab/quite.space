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
	ObservationValue  = "observation.value.v1"
	ObservationNoted  = "observation.noted.v1"
	AssetAnnotated    = "asset.annotated.v1"
	ReceiptDelivery   = "receipt.delivery.v1"
	DeviceCertified   = "identity.device_certified.v1"
	DeviceRevoked     = "identity.device_revoked.v1"
	ManifestUpdated   = "terminal.manifest.updated.v1"
	MemberJoined      = "membership.joined.v1"
	MemberLeft        = "membership.left.v1"
	MembershipEpoch   = "membership.epoch.v1"
	InstrumentEpoch   = "membership.instrument_epoch.v1"
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

// TextMessage is message.text.v1:
// {1: text, 2?: reply_to event id, 3?: mentions [principal id, …]}.
//
// Mentions are a SIGNED STRUCTURAL field, not markup inside the text: the
// text stays plain text, so "someone is addressing me" never depends on
// parsing conventions. Key 3 is an append-only addition (ADR-009) — older
// decoders skip it and keep working.
type TextMessage struct {
	Text     string
	ReplyTo  *id.EventID
	Mentions []id.PrincipalID
	// Origin says this message is a QUOTATION of another one (SHARE-1).
	//
	// It is deliberately the smallest thing that is still true. It carries
	// no source space id, no participant list, no relay, no other
	// recipient and no capability to the original — any of those would turn
	// a quotation out of a closed space into a correlation nobody consented
	// to. What it does carry are CLAIMS by the sender, because that is all
	// a quotation can be: the original's payload is sealed under its own
	// space's epoch and its signature covers that ciphertext, so it cannot
	// be verified anywhere else. Forwarding is quotation, not transmission
	// of a signed statement.
	Origin *ShareOrigin
	// ProducedModel names the model behind an AI-authored message (AI-0).
	// It is a CLAIM by the signer, exactly like every other self-declared
	// field — but without it a log of answers from three different models
	// is unattributable, and the member card cannot help: v0 manifests
	// never declare a model, so MemberCard.ModelDeclared is false and the
	// card correctly says it does not know. Written only when authorship is
	// not human; ignored by any reader that predates it.
	ProducedModel string
	// Card says this message forwards a PUBLICATION out of a public space
	// (PS wave). Key 4 stays the provenance of any quotation — who said
	// this, where and when; the card says what OBJECT this points at.
	// Different concerns, and a message share never carries a card.
	Card *SharedPublication
	// External says this message was IMPORTED from an external system by a
	// gateway terminal (TR-0, key 7). Like ShareOrigin three keys over,
	// every field is a claim by the SIGNER: the gateway observed these
	// facts at its boundary, and nothing in them is checkable inside the
	// space — external authentication never becomes Quiet identity. The
	// renderer speaks in ADR-019's voice: "{gateway} says this came from
	// {address}. Their signature did not travel."
	External *ExternalOrigin
}

// ExternalOrigin is the foreign provenance of an imported message.
type ExternalOrigin struct {
	// ConnectorKind names the boundary protocol ("email").
	ConnectorKind string
	// Address is the external sender exactly as the gateway observed it.
	Address string
	// ExternalRef is the external system's own identity for this message —
	// for email the RFC 5322 Message-ID. Provenance and threading metadata,
	// NEVER the dedup key: idempotency belongs to the connector journal's
	// transport-level id, because the Internet is not obliged to send a
	// well-formed Message-ID.
	ExternalRef string
	// ThreadRef is the external parent this message answered, when the
	// gateway could resolve one (the References chain flattens here).
	ThreadRef string
	// LossFlags name what the boundary honestly dropped or changed:
	// "attachments_omitted", "text_truncated", "html_extracted", …
	LossFlags []string
}

// External-origin bounds. The address bound follows RFC 5321's outer limit
// with headroom; the ref bound covers real-world Message-ID practice.
const (
	MaxExternalKind    = 32
	MaxExternalAddress = 320
	MaxExternalRef     = 256
	MaxLossFlags       = 8
	MaxLossFlagLen     = 32
)

// SharedPublication is a post card: a snapshot of a publication's face,
// taken by the SENDER at send time. Like everything in a share it is a
// claim — nothing in it is checkable by the reader before opening.
//
// Author, source and date are NOT here; they ride in ShareOrigin (key 4),
// one source of truth with nothing to disagree with itself. The card is
// textual on purpose: no cover, no atmosphere — media follows only after
// the reader chooses to open the post.
type SharedPublication struct {
	// Title and Summary mirror the document's face at send time.
	Title   string
	Summary string
	// Reference, when present, is the way back: a standard share link,
	// base64url(relay ++ "\n" ++ "space:<tid>[:<doc>]"). OPTIONAL — the
	// card exists without it (the sender declined, or no relay was known),
	// and then it is a readable snapshot with no door. It rides here and
	// not in ShareOrigin for a wire-safety reason: key 4's inner map has a
	// fixed arity that existing quotations already carry, while this
	// structure is new and its arity is computed.
	Reference string
}

// Card bounds. Title and Summary mirror the publication document's own
// budgets (protocol/publication imports this package, so the constants
// live here); MaxShareRef generously bounds a composed share link.
const (
	MaxCardTitle   = 200
	MaxCardSummary = 1000
	MaxShareRef    = 512
)

// cardKeyCount is the computed arity of the card's inner map — only
// non-empty fields are written, exactly as Encode computes the outer map.
func cardKeyCount(c *SharedPublication) int {
	n := 0
	if c.Title != "" {
		n++
	}
	if c.Summary != "" {
		n++
	}
	if c.Reference != "" {
		n++
	}
	return n
}

// MaxTextLen bounds a chat message payload.
const MaxTextLen = 16 * 1024

// MaxMentions bounds the mention list (a message addresses people, it does
// not broadcast to a roster).
const MaxMentions = 16

// MaxModelLen bounds the declared model string.
const MaxModelLen = 64

// Quotation bounds. A share is a quotation, not a re-publication: past this
// the quote is cut on a rune boundary and says so, which keeps the whole
// payload small enough to cross a radio link.
const (
	MaxQuoteLen  = 1024
	MaxShareName = 64
)

// ShareOrigin is the provenance of a quoted message. Every string here is
// something the SENDER says; nothing in it is checkable by the reader.
type ShareOrigin struct {
	// AuthorLabel is who the sender says wrote it. Empty renders as
	// "somebody" — offered as a choice, never as an editable field, because
	// an editable attribution is a forgery tool.
	AuthorLabel string
	// SourceLabel is the space it came from. OFF BY DEFAULT EVERYWHERE,
	// including public sources: the name discloses that YOU are in that
	// space regardless of whether the space itself is public.
	SourceLabel string
	// OriginalAt is the original's author clock — advisory everywhere else
	// in this codebase, and no more trustworthy here.
	OriginalAt uint64
	// Truncated says the quote was cut, so a reader is never shown a
	// shortened sentence as if it were the whole one.
	Truncated bool
}

func (t *TextMessage) Encode() ([]byte, error) {
	if t.Text == "" || len(t.Text) > MaxTextLen {
		return nil, errors.New("schemas: text empty or too long")
	}
	if len(t.Mentions) > MaxMentions {
		return nil, errors.New("schemas: too many mentions")
	}
	if len(t.ProducedModel) > MaxModelLen {
		return nil, errors.New("schemas: model name too long")
	}
	n := 1
	if t.ReplyTo != nil {
		n++
	}
	if len(t.Mentions) > 0 {
		n++
	}
	if t.ProducedModel != "" {
		n++
	}
	if t.Origin != nil {
		n++
		if len(t.Origin.AuthorLabel) > MaxShareName ||
			len(t.Origin.SourceLabel) > MaxShareName {
			return nil, errors.New("schemas: share label too long")
		}
	}
	if t.Card != nil {
		n++
		if len(t.Card.Title) > MaxCardTitle ||
			len(t.Card.Summary) > MaxCardSummary {
			return nil, errors.New("schemas: card text too long")
		}
		if len(t.Card.Reference) > MaxShareRef {
			return nil, errors.New("schemas: card reference too long")
		}
	}
	if x := t.External; x != nil {
		n++
		if len(x.ConnectorKind) > MaxExternalKind {
			return nil, errors.New("schemas: connector kind too long")
		}
		if len(x.Address) > MaxExternalAddress {
			return nil, errors.New("schemas: external address too long")
		}
		if len(x.ExternalRef) > MaxExternalRef || len(x.ThreadRef) > MaxExternalRef {
			return nil, errors.New("schemas: external ref too long")
		}
		if len(x.LossFlags) > MaxLossFlags {
			return nil, errors.New("schemas: too many loss flags")
		}
		for _, f := range x.LossFlags {
			if len(f) > MaxLossFlagLen {
				return nil, errors.New("schemas: loss flag too long")
			}
		}
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, t.Text)
	if t.ReplyTo != nil {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendBytes(buf, t.ReplyTo[:])
	}
	if len(t.Mentions) > 0 {
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendArray(buf, len(t.Mentions))
		for i := range t.Mentions {
			buf = codec.AppendBytes(buf, t.Mentions[i][:])
		}
	}
	if o := t.Origin; o != nil {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendMap(buf, 4)
		buf = codec.AppendUint(buf, 1)
		buf = codec.AppendText(buf, o.AuthorLabel)
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendText(buf, o.SourceLabel)
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendUint(buf, o.OriginalAt)
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendBool(buf, o.Truncated)
	}
	// Key 5 is the model claim (AI-0); 4 is share provenance; 6 is the
	// post card (PS). All append-only — a decoder that knows none of them
	// skips them all.
	if t.ProducedModel != "" {
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendText(buf, t.ProducedModel)
	}
	if t.Card != nil {
		buf = codec.AppendUint(buf, 6)
		buf = codec.AppendMap(buf, cardKeyCount(t.Card))
		if t.Card.Title != "" {
			buf = codec.AppendUint(buf, 1)
			buf = codec.AppendText(buf, t.Card.Title)
		}
		if t.Card.Summary != "" {
			buf = codec.AppendUint(buf, 2)
			buf = codec.AppendText(buf, t.Card.Summary)
		}
		if t.Card.Reference != "" {
			buf = codec.AppendUint(buf, 3)
			buf = codec.AppendText(buf, t.Card.Reference)
		}
	}
	// Key 7 is foreign provenance (TR-0), computed arity like the card —
	// only what the gateway actually observed is written.
	if x := t.External; x != nil {
		buf = codec.AppendUint(buf, 7)
		buf = codec.AppendMap(buf, externalKeyCount(x))
		if x.ConnectorKind != "" {
			buf = codec.AppendUint(buf, 1)
			buf = codec.AppendText(buf, x.ConnectorKind)
		}
		if x.Address != "" {
			buf = codec.AppendUint(buf, 2)
			buf = codec.AppendText(buf, x.Address)
		}
		if x.ExternalRef != "" {
			buf = codec.AppendUint(buf, 3)
			buf = codec.AppendText(buf, x.ExternalRef)
		}
		if x.ThreadRef != "" {
			buf = codec.AppendUint(buf, 4)
			buf = codec.AppendText(buf, x.ThreadRef)
		}
		if len(x.LossFlags) > 0 {
			buf = codec.AppendUint(buf, 5)
			buf = codec.AppendArray(buf, len(x.LossFlags))
			for _, f := range x.LossFlags {
				buf = codec.AppendText(buf, f)
			}
		}
	}
	return buf, nil
}

// externalKeyCount is the computed arity of key 7's inner map.
func externalKeyCount(x *ExternalOrigin) int {
	n := 0
	if x.ConnectorKind != "" {
		n++
	}
	if x.Address != "" {
		n++
	}
	if x.ExternalRef != "" {
		n++
	}
	if x.ThreadRef != "" {
		n++
	}
	if len(x.LossFlags) > 0 {
		n++
	}
	return n
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
		case 3:
			var cnt int
			cnt, err = d.ReadArray()
			if err != nil {
				return nil, err
			}
			if cnt > MaxMentions {
				return nil, errors.New("schemas: too many mentions")
			}
			for range cnt {
				b, er := d.ReadBytes()
				if er != nil {
					return nil, er
				}
				if len(b) != id.Size {
					return nil, errors.New("schemas: mention must be 32 bytes")
				}
				var p id.PrincipalID
				copy(p[:], b)
				t.Mentions = append(t.Mentions, p)
			}
		case 4:
			var o ShareOrigin
			m2, er := d.ReadMapHeader()
			if er != nil {
				return nil, er
			}
			for {
				k2, ok2, e2 := m2.Next()
				if e2 != nil {
					return nil, e2
				}
				if !ok2 {
					break
				}
				switch k2 {
				case 1:
					o.AuthorLabel, e2 = d.ReadText()
				case 2:
					o.SourceLabel, e2 = d.ReadText()
				case 3:
					o.OriginalAt, e2 = d.ReadUint()
				case 4:
					o.Truncated, e2 = d.ReadBool()
				default:
					e2 = d.SkipItem()
				}
				if e2 != nil {
					return nil, e2
				}
			}
			if len(o.AuthorLabel) > MaxShareName || len(o.SourceLabel) > MaxShareName {
				return nil, errors.New("schemas: share label too long")
			}
			t.Origin = &o
		case 5:
			t.ProducedModel, err = d.ReadText()
			if err == nil && len(t.ProducedModel) > MaxModelLen {
				return nil, errors.New("schemas: model name too long")
			}
		case 6:
			var c SharedPublication
			m2, er := d.ReadMapHeader()
			if er != nil {
				return nil, er
			}
			for {
				k2, ok2, e2 := m2.Next()
				if e2 != nil {
					return nil, e2
				}
				if !ok2 {
					break
				}
				switch k2 {
				case 1:
					c.Title, e2 = d.ReadText()
				case 2:
					c.Summary, e2 = d.ReadText()
				case 3:
					c.Reference, e2 = d.ReadText()
				default:
					e2 = d.SkipItem()
				}
				if e2 != nil {
					return nil, e2
				}
			}
			if len(c.Title) > MaxCardTitle || len(c.Summary) > MaxCardSummary {
				return nil, errors.New("schemas: card text too long")
			}
			if len(c.Reference) > MaxShareRef {
				return nil, errors.New("schemas: card reference too long")
			}
			t.Card = &c
		case 7:
			var x ExternalOrigin
			m2, er := d.ReadMapHeader()
			if er != nil {
				return nil, er
			}
			for {
				k2, ok2, e2 := m2.Next()
				if e2 != nil {
					return nil, e2
				}
				if !ok2 {
					break
				}
				switch k2 {
				case 1:
					x.ConnectorKind, e2 = d.ReadText()
				case 2:
					x.Address, e2 = d.ReadText()
				case 3:
					x.ExternalRef, e2 = d.ReadText()
				case 4:
					x.ThreadRef, e2 = d.ReadText()
				case 5:
					var cnt int
					cnt, e2 = d.ReadArray()
					if e2 != nil {
						return nil, e2
					}
					if cnt > MaxLossFlags {
						return nil, errors.New("schemas: too many loss flags")
					}
					for range cnt {
						f, ef := d.ReadText()
						if ef != nil {
							return nil, ef
						}
						if len(f) > MaxLossFlagLen {
							return nil, errors.New("schemas: loss flag too long")
						}
						x.LossFlags = append(x.LossFlags, f)
					}
				default:
					e2 = d.SkipItem()
				}
				if e2 != nil {
					return nil, e2
				}
			}
			if len(x.ConnectorKind) > MaxExternalKind ||
				len(x.Address) > MaxExternalAddress ||
				len(x.ExternalRef) > MaxExternalRef ||
				len(x.ThreadRef) > MaxExternalRef {
				return nil, errors.New("schemas: external origin field too long")
			}
			t.External = &x
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
// {1: title, 2: status, 3?: assignee principal, 4?: origin event, 5?: card id,
// 6?: object id}. Key 6 (SP-1) attaches the task to a domain object; it is
// the RAW object id, not the derived target — reducers index it directly.
type Card struct {
	Title    string
	Status   string // open | done | dropped
	Assignee *id.PrincipalID
	Origin   *id.EventID // message this card was made from
	Card     *id.EventID // for updates: the card.created event
	ObjectID *[16]byte   // domain object this task belongs to
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
	if c.ObjectID != nil {
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
	if c.ObjectID != nil {
		buf = codec.AppendUint(buf, 6)
		buf = codec.AppendBytes(buf, c.ObjectID[:])
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
		case 6:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 16 {
					err = errors.New("schemas: object id must be 16 bytes")
				} else {
					var o [16]byte
					copy(o[:], b)
					c.ObjectID = &o
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


// ---- observation.value.v1 (QI-0) ----
//
// The general reading of one INSTRUMENT CHANNEL. Three amendments from the
// owner's plan review are load-bearing here:
//
//   - The value is a TAGGED FIXED-POINT, not "hundredths": a magnitude and
//     a decimal count (value = ±magnitude / 10^decimals). No float ever
//     enters a signed structure (ADR-003), and no precision is cemented —
//     12.734 V and 101325 Pa and 824 ppm all encode without ceremony.
//   - kind and unit are NOT in the frame. The signed manifest's channel
//     declaration (qp.instr) is the single source of truth for what a
//     channel means; a frame that could restate them could also contradict
//     them, and the receiver would have to pick a winner. Smaller on the
//     radio, too.
//   - stale_after is mandatory, exactly as observation.temperature.v1
//     insists: a reading that cannot say when it stops being current is a
//     reading that will one day be displayed as fresher than it is.
//
// {1: channel, 2: magnitude, 3: negative, 4: decimals,
//  5: bool_value, 6: enum_value, 7: observed_at, 8: stale_after,
//  9: simulated}. Exactly ONE of {magnitude(+3,4), bool_value, enum_value}
// families is present — the tag is the presence.
type ValueObservation struct {
	Channel string // paramNameRe grammar, same as live_signal params

	HasNumber bool
	Magnitude uint64 // absolute value
	Negative  bool
	Decimals  uint64 // value = ±Magnitude / 10^Decimals; ≤ 18

	HasBool   bool
	BoolValue bool

	EnumValue string // non-empty = the enum tag

	ObservedAt uint64
	StaleAfter uint64 // seconds; freshness honesty
	Simulated  bool   // must be declared, never inferred
}

// MaxEnumValueLen bounds an enum reading; a state word, not a paragraph.
const MaxEnumValueLen = 48

func (o *ValueObservation) tags() int {
	n := 0
	if o.HasNumber {
		n++
	}
	if o.HasBool {
		n++
	}
	if o.EnumValue != "" {
		n++
	}
	return n
}

func (o *ValueObservation) Encode() ([]byte, error) {
	if !paramNameRe.MatchString(o.Channel) {
		return nil, errors.New("schemas: observation channel does not parse")
	}
	if o.tags() != 1 {
		return nil, errors.New("schemas: observation carries exactly one value kind")
	}
	if o.HasNumber && o.Decimals > 18 {
		return nil, errors.New("schemas: observation decimals out of range")
	}
	if len(o.EnumValue) > MaxEnumValueLen {
		return nil, errors.New("schemas: observation enum value too long")
	}
	if o.ObservedAt == 0 {
		return nil, errors.New("schemas: observation requires observed_at")
	}
	if o.StaleAfter == 0 {
		return nil, errors.New("schemas: observation requires stale_after")
	}
	n := 3 // channel, observed_at, stale_after
	if o.HasNumber {
		n++
		if o.Negative {
			n++
		}
		if o.Decimals > 0 {
			n++
		}
	}
	if o.HasBool {
		n++
	}
	if o.EnumValue != "" {
		n++
	}
	if o.Simulated {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, o.Channel)
	if o.HasNumber {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendUint(buf, o.Magnitude)
		if o.Negative {
			buf = codec.AppendUint(buf, 3)
			buf = codec.AppendBool(buf, true)
		}
		if o.Decimals > 0 {
			buf = codec.AppendUint(buf, 4)
			buf = codec.AppendUint(buf, o.Decimals)
		}
	}
	if o.HasBool {
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendBool(buf, o.BoolValue)
	}
	if o.EnumValue != "" {
		buf = codec.AppendUint(buf, 6)
		buf = codec.AppendText(buf, o.EnumValue)
	}
	buf = codec.AppendUint(buf, 7)
	buf = codec.AppendUint(buf, o.ObservedAt)
	buf = codec.AppendUint(buf, 8)
	buf = codec.AppendUint(buf, o.StaleAfter)
	if o.Simulated {
		buf = codec.AppendUint(buf, 9)
		buf = codec.AppendBool(buf, true)
	}
	return buf, nil
}

func DecodeValueObservation(payload []byte) (*ValueObservation, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	o := &ValueObservation{}
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
			o.Channel, err = d.ReadText()
		case 2:
			o.Magnitude, err = d.ReadUint()
			o.HasNumber = true
		case 3:
			o.Negative, err = d.ReadBool()
		case 4:
			o.Decimals, err = d.ReadUint()
		case 5:
			o.BoolValue, err = d.ReadBool()
			o.HasBool = true
		case 6:
			o.EnumValue, err = d.ReadText()
		case 7:
			o.ObservedAt, err = d.ReadUint()
		case 8:
			o.StaleAfter, err = d.ReadUint()
		case 9:
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
	if !paramNameRe.MatchString(o.Channel) {
		return nil, errors.New("schemas: observation channel does not parse")
	}
	if o.tags() != 1 {
		return nil, errors.New("schemas: observation carries exactly one value kind")
	}
	if o.HasNumber && o.Decimals > 18 {
		return nil, errors.New("schemas: observation decimals out of range")
	}
	if len(o.EnumValue) > MaxEnumValueLen {
		return nil, errors.New("schemas: observation enum value too long")
	}
	if o.ObservedAt == 0 || o.StaleAfter == 0 {
		return nil, errors.New("schemas: observation requires observed_at and stale_after")
	}
	return o, nil
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
	Register(ObservationValue, func(p []byte) error { _, err := DecodeValueObservation(p); return err })
	Register(InstrumentEpoch, func(p []byte) error { _, err := DecodeEpochPayload(p); return err })
	Register(MembershipEpoch, func(p []byte) error { _, err := DecodeEpochPayload(p); return err })
}
