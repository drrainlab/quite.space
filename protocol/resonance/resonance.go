// Package resonance is the Resonance Protocol (RP-0): reactions as small
// semantic traces of presence, not likes. A reaction is either a SEMANTIC
// meaning (a stable key with a wire fallback so unknown keys degrade
// honestly) or a plain UNICODE emoji. Meaning is separated from rendering:
//
//	semantic reaction → signed event → aggregate projection
//	→ reaction palette → surface effect → client renderer
//
// Cardinality is SINGLE: one active persistent reaction per (actor, target);
// a new set replaces the previous one, clear releases it. The palette is an
// event-sourced, controller-authored document that defines which semantic
// meanings a space speaks and how they present.
package resonance

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/contract"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// Schema ids (2-segment grammar — the spec's space.reaction.set.v1 form is
// invalid under ADR-009 ids).
const (
	SchemaSet     = "reaction.set.v1"
	SchemaClear   = "reaction.clear.v1"
	SchemaPalette = "reaction.palette.v1"
)

// Reaction kinds (wire discriminator).
const (
	KindSemantic = 1
	KindUnicode  = 2
)

// Bounds.
const (
	MaxKeyLen   = 64
	MaxLabelLen = 48
	MaxSlots    = 6
	MinSlots    = 1
)

// keyRe is the semantic key grammar. Dots are permitted so namespaced
// custom keys ("pinevibes.drift") already pass v1 validation.
var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// ValidKey reports whether s is a well-formed semantic key.
func ValidKey(s string) bool { return len(s) <= MaxKeyLen && keyRe.MatchString(s) }

// Reaction is the tagged union carried inside set events:
// {1: kind, 2: key (semantic), 3: value (unicode), 4: fallback (semantic)}.
// Exactly one arm is populated; Validate enforces the discriminator.
type Reaction struct {
	Kind     int
	Key      string // semantic
	Value    string // unicode (NormalizeEmoji'd)
	Fallback string // semantic wire fallback (NormalizeEmoji'd)
}

// Validate structurally checks the union.
func (r *Reaction) Validate() error {
	switch r.Kind {
	case KindSemantic:
		if !ValidKey(r.Key) {
			return fmt.Errorf("resonance: bad semantic key %q", r.Key)
		}
		if r.Value != "" {
			return errors.New("resonance: semantic reaction must not carry a unicode value")
		}
		if _, err := schemas.NormalizeEmoji(r.Fallback); err != nil {
			return errors.New("resonance: semantic reaction requires an emoji fallback")
		}
	case KindUnicode:
		if r.Key != "" || r.Fallback != "" {
			return errors.New("resonance: unicode reaction must not carry key/fallback")
		}
		if _, err := schemas.NormalizeEmoji(r.Value); err != nil {
			return errors.New("resonance: unicode reaction requires a valid emoji")
		}
	default:
		return fmt.Errorf("resonance: unknown reaction kind %d", r.Kind)
	}
	return nil
}

// GroupKey is the canonical aggregation identity: semantic groups by key,
// unicode groups by NFC value. Also the canonical sort order of groups.
func (r *Reaction) GroupKey() string {
	if r.Kind == KindSemantic {
		return "s:" + r.Key
	}
	return "u:" + r.Value
}

func (r *Reaction) encodeInto(buf []byte) []byte {
	n := 1
	if r.Key != "" {
		n++
	}
	if r.Value != "" {
		n++
	}
	if r.Fallback != "" {
		n++
	}
	buf = codec.AppendMap(buf, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendUint(buf, uint64(r.Kind))
	if r.Key != "" {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendText(buf, r.Key)
	}
	if r.Value != "" {
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendText(buf, r.Value)
	}
	if r.Fallback != "" {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendText(buf, r.Fallback)
	}
	return buf
}

func decodeReaction(d *codec.Decoder) (*Reaction, error) {
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	r := &Reaction{}
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
			var v uint64
			v, er = d.ReadUint()
			r.Kind = int(v)
		case 2:
			r.Key, er = d.ReadText()
		case 3:
			r.Value, er = d.ReadText()
		case 4:
			r.Fallback, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	// Normalize emoji fields on decode (NFC) before validating.
	if r.Value != "" {
		if r.Value, err = schemas.NormalizeEmoji(r.Value); err != nil {
			return nil, err
		}
	}
	if r.Fallback != "" {
		if r.Fallback, err = schemas.NormalizeEmoji(r.Fallback); err != nil {
			return nil, err
		}
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// ---- reaction.set.v1 / reaction.clear.v1 ----

// SetPayload is reaction.set.v1: {1: target event id, 2: Reaction}.
type SetPayload struct {
	Target   id.EventID
	Reaction Reaction
}

func (p *SetPayload) Encode() ([]byte, error) {
	if err := p.Reaction.Validate(); err != nil {
		return nil, err
	}
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendBytes(buf, p.Target[:])
	buf = codec.AppendUint(buf, 2)
	buf = p.Reaction.encodeInto(buf)
	return buf, nil
}

func DecodeSet(payload []byte) (*SetPayload, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &SetPayload{}
	var seenT, seenR bool
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
			var b []byte
			b, er = d.ReadBytes()
			if er == nil {
				if len(b) != id.Size {
					return nil, errors.New("resonance: target must be 32 bytes")
				}
				copy(p.Target[:], b)
				seenT = true
			}
		case 2:
			var r *Reaction
			r, er = decodeReaction(d)
			if er == nil {
				p.Reaction = *r
				seenR = true
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
	if !seenT || !seenR {
		return nil, errors.New("resonance: set requires target and reaction")
	}
	return p, nil
}

// ClearPayload is reaction.clear.v1: {1: target}. No reaction field —
// SINGLE cardinality means clear releases the actor's whole slot.
type ClearPayload struct {
	Target id.EventID
}

func (p *ClearPayload) Encode() []byte {
	buf := codec.AppendMap(nil, 1)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendBytes(buf, p.Target[:])
	return buf
}

func DecodeClear(payload []byte) (*ClearPayload, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &ClearPayload{}
	seen := false
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
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
				return nil, errors.New("resonance: target must be 32 bytes")
			}
			copy(p.Target[:], b)
			seen = true
		} else if err := d.SkipItem(); err != nil {
			return nil, err
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if !seen {
		return nil, errors.New("resonance: clear requires target")
	}
	return p, nil
}

// ---- reaction.palette.v1 ----

// Actor visibility policy values. v1 emits only ActorsMembers; the decoder
// accepts all values so the field can grow without a wire change.
const (
	ActorsMembers       = 1
	ActorsAuthorOnly    = 2
	ActorsAggregateOnly = 3
	ActorsHidden        = 4
)

// PaletteSlot is one semantic meaning a space offers in its quick menu.
type PaletteSlot struct {
	Key        string
	Label      string
	Fallback   string
	RendererID string // resolves only through the client's allowlist
	EffectID   string // resolves only through the client's allowlist
}

// PalettePolicy: presentation policy in this wave (data still reaches
// members — counts drive effect intensity; the UI hides per policy). Real
// privacy filtering arrives with actor visibility modes.
type PalettePolicy struct {
	Cardinality    int // 1 = single (only valid value in v1)
	AllowUnicode   bool
	ShowCounts     bool
	ShowActors     int // ActorsMembers.. — v1 emit gate accepts only ActorsMembers
	SurfaceEffects bool
}

// Palette is reaction.palette.v1:
// {1: palette_id, 2: slots, 3: policy, 4: default_key}.
type Palette struct {
	PaletteID  string
	Slots      []PaletteSlot
	Policy     PalettePolicy
	DefaultKey string
}

// Validate is the authoring gate (decoder also runs it — a malformed
// palette never folds).
func (p *Palette) Validate() error {
	if p.PaletteID == "" || len(p.PaletteID) > MaxKeyLen {
		return errors.New("resonance: palette id required (≤64 bytes)")
	}
	if len(p.Slots) < MinSlots || len(p.Slots) > MaxSlots {
		return fmt.Errorf("resonance: palette needs %d..%d slots", MinSlots, MaxSlots)
	}
	seen := map[string]bool{}
	for i := range p.Slots {
		s := &p.Slots[i]
		if !ValidKey(s.Key) {
			return fmt.Errorf("resonance: bad slot key %q", s.Key)
		}
		if seen[s.Key] {
			return fmt.Errorf("resonance: duplicate slot key %q", s.Key)
		}
		seen[s.Key] = true
		if s.Label == "" || len(s.Label) > MaxLabelLen {
			return fmt.Errorf("resonance: slot %q label required (≤%d)", s.Key, MaxLabelLen)
		}
		if _, err := schemas.NormalizeEmoji(s.Fallback); err != nil {
			return fmt.Errorf("resonance: slot %q requires an emoji fallback", s.Key)
		}
	}
	if !seen[p.DefaultKey] {
		return fmt.Errorf("resonance: default_key %q must be one of the slots", p.DefaultKey)
	}
	if p.Policy.Cardinality != 1 {
		return errors.New("resonance: only cardinality=single is valid in v1")
	}
	if p.Policy.ShowActors < ActorsMembers || p.Policy.ShowActors > ActorsHidden {
		return errors.New("resonance: bad show_actors policy")
	}
	return nil
}

func (p *Palette) Encode() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	buf := codec.AppendMap(nil, 4)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, p.PaletteID)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendArray(buf, len(p.Slots))
	for _, s := range p.Slots {
		n := 3
		if s.RendererID != "" {
			n++
		}
		if s.EffectID != "" {
			n++
		}
		buf = codec.AppendMap(buf, n)
		buf = codec.AppendUint(buf, 1)
		buf = codec.AppendText(buf, s.Key)
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendText(buf, s.Label)
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendText(buf, s.Fallback)
		if s.RendererID != "" {
			buf = codec.AppendUint(buf, 4)
			buf = codec.AppendText(buf, s.RendererID)
		}
		if s.EffectID != "" {
			buf = codec.AppendUint(buf, 5)
			buf = codec.AppendText(buf, s.EffectID)
		}
	}
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendMap(buf, 5)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendUint(buf, uint64(p.Policy.Cardinality))
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendBool(buf, p.Policy.AllowUnicode)
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendBool(buf, p.Policy.ShowCounts)
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendUint(buf, uint64(p.Policy.ShowActors))
	buf = codec.AppendUint(buf, 5)
	buf = codec.AppendBool(buf, p.Policy.SurfaceEffects)
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendText(buf, p.DefaultKey)
	return buf, nil
}

func DecodePalette(payload []byte) (*Palette, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	p := &Palette{}
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
			p.PaletteID, er = d.ReadText()
		case 2:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			for range cnt {
				slot, e := decodeSlot(d)
				if e != nil {
					return nil, e
				}
				p.Slots = append(p.Slots, *slot)
			}
		case 3:
			er = decodePolicy(d, &p.Policy)
		case 4:
			p.DefaultKey, er = d.ReadText()
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
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func decodeSlot(d *codec.Decoder) (*PaletteSlot, error) {
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	s := &PaletteSlot{}
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
			s.Key, er = d.ReadText()
		case 2:
			s.Label, er = d.ReadText()
		case 3:
			s.Fallback, er = d.ReadText()
		case 4:
			s.RendererID, er = d.ReadText()
		case 5:
			s.EffectID, er = d.ReadText()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if s.Fallback != "" {
		if s.Fallback, err = schemas.NormalizeEmoji(s.Fallback); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func decodePolicy(d *codec.Decoder, p *PalettePolicy) error {
	m, err := d.ReadMapHeader()
	if err != nil {
		return err
	}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return er
		}
		if !ok {
			return nil
		}
		switch k {
		case 1:
			var v uint64
			v, er = d.ReadUint()
			p.Cardinality = int(v)
		case 2:
			p.AllowUnicode, er = d.ReadBool()
		case 3:
			p.ShowCounts, er = d.ReadBool()
		case 4:
			var v uint64
			v, er = d.ReadUint()
			p.ShowActors = int(v)
		case 5:
			p.SurfaceEffects, er = d.ReadBool()
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return er
		}
	}
}

// ---- core semantic registry ----

// CoreMeaning is one protocol-level semantic every client must understand.
type CoreMeaning struct {
	Key      string
	Label    string // English; UI localizes via i18n
	Fallback string
}

// CoreRegistry is the stable protocol vocabulary — the ONE source of truth,
// served to clients through the node (never mirrored by hand in JS).
var CoreRegistry = []CoreMeaning{
	{Key: "resonates", Label: "Resonates", Fallback: "〰️"},
	{Key: "warmth", Label: "Warmth", Fallback: "♡"},
	{Key: "spark", Label: "Spark", Fallback: "✦"},
	{Key: "support", Label: "With you", Fallback: "◌"},
	{Key: "curious", Label: "Curious", Fallback: "⌁"},
	{Key: "join", Label: "Count me in", Fallback: "↗"},
	{Key: "weight", Label: "This is heavy", Fallback: "●"},
}

// CoreMeaningByKey resolves a core key.
func CoreMeaningByKey(key string) (CoreMeaning, bool) {
	for _, m := range CoreRegistry {
		if m.Key == key {
			return m, true
		}
	}
	return CoreMeaning{}, false
}

// DefaultPalette is the built-in pine-vibes.v1 palette used until a space
// publishes its own.
func DefaultPalette() Palette {
	slots := make([]PaletteSlot, 0, 6)
	for _, m := range CoreRegistry[:6] {
		slots = append(slots, PaletteSlot{Key: m.Key, Label: m.Label, Fallback: m.Fallback})
	}
	return Palette{
		PaletteID:  "pine-vibes.v1",
		Slots:      slots,
		DefaultKey: "resonates",
		Policy: PalettePolicy{
			Cardinality: 1, AllowUnicode: true, ShowCounts: true,
			ShowActors: ActorsMembers, SurfaceEffects: true,
		},
	}
}

// ResolveFallback returns the authoritative glyph for a semantic key
// (correction 2): active palette slot → CoreRegistry → deterministically
// chosen wire fallback → generic marker. For KNOWN core keys the wire
// fallback is never used at render time — a set event claiming
// {key:"warmth", fallback:"💩"} cannot repaint a known meaning.
func ResolveFallback(key string, palette *Palette, wireFallback string) string {
	if palette != nil {
		for _, s := range palette.Slots {
			if s.Key == key {
				return s.Fallback
			}
		}
	}
	if m, ok := CoreMeaningByKey(key); ok {
		return m.Fallback
	}
	if wireFallback != "" {
		return wireFallback
	}
	return "◈" // generic reaction marker
}

// ---- contracts (LR-0a registry) ----

type setContract struct{}

func (setContract) SchemaID() string        { return SchemaSet }
func (setContract) Validate(p []byte) error { _, err := DecodeSet(p); return err }
func (setContract) Fallback(p []byte) (string, error) {
	s, err := DecodeSet(p)
	if err != nil {
		return "", err
	}
	if s.Reaction.Kind == KindSemantic {
		return "resonated " + ResolveFallback(s.Reaction.Key, nil, s.Reaction.Fallback), nil
	}
	return "resonated " + s.Reaction.Value, nil
}
func (setContract) AssetRefs(p []byte) ([]schemas.AssetRef, error) { return nil, nil }

type clearContract struct{}

func (clearContract) SchemaID() string        { return SchemaClear }
func (clearContract) Validate(p []byte) error { _, err := DecodeClear(p); return err }
func (clearContract) Fallback(p []byte) (string, error) {
	if _, err := DecodeClear(p); err != nil {
		return "", err
	}
	return "released a resonance", nil
}
func (clearContract) AssetRefs(p []byte) ([]schemas.AssetRef, error) { return nil, nil }

type paletteContract struct{}

func (paletteContract) SchemaID() string        { return SchemaPalette }
func (paletteContract) Validate(p []byte) error { _, err := DecodePalette(p); return err }
func (paletteContract) Fallback(p []byte) (string, error) {
	pl, err := DecodePalette(p)
	if err != nil {
		return "", err
	}
	return "tuned the space's palette · " + pl.PaletteID, nil
}
func (paletteContract) AssetRefs(p []byte) ([]schemas.AssetRef, error) { return nil, nil }

func init() {
	contract.Register(setContract{}, contract.Descriptor{SchemaID: SchemaSet})
	contract.Register(clearContract{}, contract.Descriptor{SchemaID: SchemaClear})
	contract.Register(paletteContract{}, contract.Descriptor{SchemaID: SchemaPalette})
}
