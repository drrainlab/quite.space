// Package objects defines the DOMAIN OBJECT of a space (SP-1): a machine,
// a part, a project, an order — a long-lived, revisable entity that owns
// its state, follows the publication pattern (stable 16-byte id, full
// signed revisions, optimistic concurrency, archive/restore), and serves
// as the stable target tasks, observations, keeps and reactions point at.
//
// NAMING, deliberately: the type is Record, never Object. Three different
// things wear the word in this codebase and must never be confused:
//
//	objects.Record        — THIS: a domain entity that owns state
//	composition.Object    — a placed visual reference (SC-0), owns nothing
//	publication "card"    — a share card of a document
//
// Two invariants the owner fixed before the first commit:
//
//	Status is DOMAIN-LOCAL DISPLAY STATE. The kernel never branches on
//	its value — there is no universal meaning to "done" across machines,
//	parts and orders, and pretending otherwise would freeze a vocabulary
//	nobody agreed on.
//
//	Props in a revision are THE FULL NEW SET, not a patch. A revision
//	carries the whole record; an omitted prop is a deleted prop.
package objects

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Bounds. A record is STRUCTURAL in public projections — it never ages
// out — so it is obliged to stay cheap.
const (
	MaxName        = 200
	MaxSummary     = 1000
	MaxKindLen     = 32
	MaxStatusLen   = 40
	MaxProps       = 16
	MaxPropKeyLen  = 40
	MaxPropValLen  = 200
	MaxRecordBytes = 8 << 10
	// MaxRawExtraBytes bounds unknown-key passengers (ADR-009 retention),
	// the publication discipline.
	MaxRawExtraBytes = 4 << 10
)

// Record key table — append-only forever.
const (
	recKeyID      = 1
	recKeyKind    = 2
	recKeyName    = 3
	recKeyStatus  = 4
	recKeySummary = 5
	recKeyProps   = 6
	recKeyCover   = 7

	// maxKnownRecKey is the highest key this build understands; higher
	// keys ride RawExtra verbatim so an older editor cannot strip a
	// newer field by re-saving.
	maxKnownRecKey = recKeyCover
)

// Prop is one key/value pair. Encoded as a sorted array of pairs rather
// than a text-keyed map: the codec's canonical maps are integer-keyed,
// and inventing text-key map support for one field would be a protocol
// change wearing a convenience's clothes.
type Prop struct {
	Key   string
	Value string
}

// Extra holds record-level keys this build does not understand.
type Extra struct {
	Key uint64
	Raw []byte // one canonical CBOR item
}

// Record is the full object state. Every revision carries all of it.
type Record struct {
	ObjectID [16]byte
	// Kind is a FREE slug (machine, part, project, order, material, tool,
	// other — suggested by UIs, never enforced): kind is presentational,
	// not authorizing, and closed vocabularies here have needed ceremony
	// to extend every time one was tried.
	Kind    string
	Name    string
	Status  string // domain-local display state; kernel never reads it
	Summary string
	Props   []Prop // sorted by Key, unique
	Cover   string // hex asset id, optional
	// RawExtra carries unknown higher keys through a re-save.
	RawExtra []Extra
}

func slugOK(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
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

// Validate enforces authoring bounds. Decoders call it too: a record that
// would be refused at authoring is refused at reading — one truth.
func (r *Record) Validate() error {
	if r.ObjectID == ([16]byte{}) {
		return errors.New("objects: object id is required")
	}
	if !slugOK(r.Kind, MaxKindLen) {
		return fmt.Errorf("objects: kind %q is not a slug", r.Kind)
	}
	if r.Name == "" || len(r.Name) > MaxName {
		return errors.New("objects: name empty or too long")
	}
	if r.Status != "" && !slugOK(r.Status, MaxStatusLen) {
		return fmt.Errorf("objects: status %q is not a slug", r.Status)
	}
	if len(r.Summary) > MaxSummary {
		return errors.New("objects: summary too long")
	}
	if len(r.Props) > MaxProps {
		return errors.New("objects: too many props")
	}
	prev := ""
	for i, p := range r.Props {
		if !slugOK(p.Key, MaxPropKeyLen) {
			return fmt.Errorf("objects: prop key %q is not a slug", p.Key)
		}
		if len(p.Value) > MaxPropValLen {
			return fmt.Errorf("objects: prop %q value too long", p.Key)
		}
		if i > 0 && p.Key <= prev {
			return errors.New("objects: props must be sorted and unique")
		}
		prev = p.Key
	}
	if len(r.Cover) > 128 {
		return errors.New("objects: cover id too long")
	}
	return nil
}

// Encode emits the canonical record. Props must arrive sorted (Validate
// checks); RawExtra is re-emitted after the known keys.
func (r *Record) Encode() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	extra := retainExtra(r.RawExtra, maxKnownRecKey, MaxRawExtraBytes)
	n := 3 // id, kind, name
	if r.Status != "" {
		n++
	}
	if r.Summary != "" {
		n++
	}
	if len(r.Props) > 0 {
		n++
	}
	if r.Cover != "" {
		n++
	}
	n += len(extra)
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, recKeyID)
	buf = codec.AppendBytes(buf, r.ObjectID[:])
	buf = codec.AppendUint(buf, recKeyKind)
	buf = codec.AppendText(buf, r.Kind)
	buf = codec.AppendUint(buf, recKeyName)
	buf = codec.AppendText(buf, r.Name)
	if r.Status != "" {
		buf = codec.AppendUint(buf, recKeyStatus)
		buf = codec.AppendText(buf, r.Status)
	}
	if r.Summary != "" {
		buf = codec.AppendUint(buf, recKeySummary)
		buf = codec.AppendText(buf, r.Summary)
	}
	if len(r.Props) > 0 {
		buf = codec.AppendUint(buf, recKeyProps)
		buf = codec.AppendArray(buf, len(r.Props))
		for _, p := range r.Props {
			buf = codec.AppendArray(buf, 2)
			buf = codec.AppendText(buf, p.Key)
			buf = codec.AppendText(buf, p.Value)
		}
	}
	if r.Cover != "" {
		buf = codec.AppendUint(buf, recKeyCover)
		buf = codec.AppendText(buf, r.Cover)
	}
	for _, e := range extra {
		buf = codec.AppendUint(buf, e.Key)
		buf = append(buf, e.Raw...)
	}
	if len(buf) > MaxRecordBytes {
		return nil, errors.New("objects: record too large")
	}
	return buf, nil
}

// Decode parses and validates a record, keeping unknown higher keys.
func Decode(payload []byte) (*Record, error) {
	if len(payload) > MaxRecordBytes {
		return nil, errors.New("objects: record too large")
	}
	d := codec.NewDecoder(payload)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	r := &Record{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case recKeyID:
			er = read16(d, r.ObjectID[:])
		case recKeyKind:
			r.Kind, er = d.ReadText()
		case recKeyName:
			r.Name, er = d.ReadText()
		case recKeyStatus:
			r.Status, er = d.ReadText()
		case recKeySummary:
			r.Summary, er = d.ReadText()
		case recKeyProps:
			var cnt int
			cnt, er = d.ReadArray()
			if er == nil {
				if cnt > MaxProps {
					er = errors.New("objects: too many props")
				}
				for i := 0; i < cnt && er == nil; i++ {
					var two int
					two, er = d.ReadArray()
					if er == nil && two != 2 {
						er = errors.New("objects: prop is not a pair")
					}
					if er == nil {
						var p Prop
						p.Key, er = d.ReadText()
						if er == nil {
							p.Value, er = d.ReadText()
						}
						r.Props = append(r.Props, p)
					}
				}
			}
		case recKeyCover:
			r.Cover, er = d.ReadText()
		default:
			er = readExtra(d, k, maxKnownRecKey, &r.RawExtra)
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// Target derives the STABLE id keeps, reactions and deep links address —
// a function of the object id alone, so it never moves when the record
// is revised. Domain-separated from the publication target.
func Target(objectID [16]byte) id.EventID {
	sum := sha256.Sum256(append([]byte("qs.object.v1"), objectID[:]...))
	var out id.EventID
	copy(out[:], sum[:])
	return out
}

func read16(d *codec.Decoder, dst []byte) error {
	b, err := d.ReadBytes()
	if err != nil {
		return err
	}
	if len(b) != 16 {
		return errors.New("objects: expected 16 bytes")
	}
	copy(dst, b)
	return nil
}

func read32(d *codec.Decoder, dst []byte) error {
	b, err := d.ReadBytes()
	if err != nil {
		return err
	}
	if len(b) != 32 {
		return errors.New("objects: expected 32 bytes")
	}
	copy(dst, b)
	return nil
}

// retainExtra / readExtra — the publication RawExtra discipline, copied
// rather than imported: the two packages stay independent, and forty
// lines is cheaper than a dependency.
func retainExtra(list []Extra, maxKnown uint64, budget int) []Extra {
	if len(list) == 0 {
		return nil
	}
	out := make([]Extra, 0, len(list))
	var last uint64
	var total int
	for _, e := range list {
		if e.Key <= maxKnown || e.Key <= last || len(e.Raw) == 0 {
			continue
		}
		total += len(e.Raw)
		if total > budget {
			break
		}
		out = append(out, e)
		last = e.Key
	}
	return out
}

func readExtra(d *codec.Decoder, k, maxKnown uint64, into *[]Extra) error {
	if k <= maxKnown {
		return d.SkipItem()
	}
	raw, err := d.ReadRawItem()
	if err != nil {
		return err
	}
	*into = append(*into, Extra{Key: k, Raw: raw})
	return nil
}
