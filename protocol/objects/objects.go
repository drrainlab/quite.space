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
	"github.com/drrainlab/quiet_places/protocol/geo"
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
	recKeyParent  = 8
	recKeyGeo     = 9
	recKeyPath    = 10

	// maxKnownRecKey is the highest key this build understands; higher
	// keys ride RawExtra verbatim so an older editor cannot strip a
	// newer field by re-saving.
	maxKnownRecKey = recKeyPath
)

// Geo bounds (SP-3, ADR-031).
const (
	// MaxGeoRadiusM bounds a zone. Circles, not polygons — v1 says so
	// out loud: point+radius covers base/camp/search-vicinity/rendezvous
	// and proves the vertical without a geometry library.
	MaxGeoRadiusM = 100_000
	// MaxRoutePoints is DERIVED BY MEASUREMENT, not chosen — see
	// TestMaxRoutePointsIsMeasured (transports/compact). The two-tier
	// radio law (ADR-031): a route revision's envelope FLOOR (~246 B of
	// signature + metadata + revision scaffolding) can never fit one
	// Meshtastic frame, and chain blocking (C3) only bites at
	// ErrTooLarge — so the HARD guarantee is one RNode frame (500 B)
	// for the full signed warm-compact envelope with a worst-case
	// 32-rune Cyrillic name. A route here is an operational INTENT
	// (BASE → WP1 → ridge → RV), never a GPS track; breadcrumbs are
	// position observations, a track log is a future bulk artifact.
	MaxRoutePoints = 21
)

// GeoShape is an object's geographic claim: a point, optionally widened
// to a zone by a radius. RadiusM 0 means "a point" and is never encoded —
// one representation.
type GeoShape struct {
	Point   geo.Point
	RadiusM uint64
}

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
	// Parent models PRIMARY CONTAINMENT — the one tree the object lives
	// in (Track lives in a Release, Session lives in a Track). It is NOT
	// an arbitrary object relationship: a track that also appears on a
	// compilation is a future, separate edge primitive, and stretching
	// this single pointer into that role would quietly turn a tree into
	// an unmodelled graph (SP-2, ADR-030). The edge is DERIVED at
	// projection time (ChildrenOf) — a parent record never lists its
	// children.
	Parent *[16]byte
	// Geo places the object in the world (SP-3): ANY object may carry a
	// coordinate — a machine on the shop-floor map as much as a base
	// camp. A "Place" is not a new entity; it is a Record with Geo and a
	// kind the renderer understands (ADR-029/031).
	Geo *GeoShape
	// Path is an ordered sequence of points — an operational route's
	// INTENT. Meaningful on kind=route but not restricted to it; the
	// renderer decides. Bounded by MaxRoutePoints (measured, see above).
	Path []geo.Point
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
	if r.Parent != nil {
		if *r.Parent == ([16]byte{}) {
			return errors.New("objects: parent id is zero")
		}
		if *r.Parent == r.ObjectID {
			return errors.New("objects: an object cannot be its own parent")
		}
	}
	if r.Geo != nil {
		if !r.Geo.Point.Valid() {
			return errors.New("objects: geo point out of range")
		}
		if r.Geo.RadiusM > MaxGeoRadiusM {
			return errors.New("objects: geo radius too large")
		}
	}
	if len(r.Path) > 0 {
		if len(r.Path) < 2 {
			return errors.New("objects: a path needs at least two points")
		}
		if len(r.Path) > MaxRoutePoints {
			return errors.New("objects: path exceeds the one-frame route bound")
		}
		for _, p := range r.Path {
			if !p.Valid() {
				return errors.New("objects: path point out of range")
			}
		}
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
	if r.Parent != nil {
		n++
	}
	if r.Geo != nil {
		n++
	}
	if len(r.Path) > 0 {
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
	if r.Parent != nil {
		buf = codec.AppendUint(buf, recKeyParent)
		buf = codec.AppendBytes(buf, r.Parent[:])
	}
	if r.Geo != nil {
		buf = codec.AppendUint(buf, recKeyGeo)
		if r.Geo.RadiusM > 0 {
			buf = codec.AppendArray(buf, 3)
			buf = codec.AppendUint(buf, r.Geo.Point.LatE7U)
			buf = codec.AppendUint(buf, r.Geo.Point.LonE7U)
			buf = codec.AppendUint(buf, r.Geo.RadiusM)
		} else {
			buf = geo.AppendPoint(buf, r.Geo.Point)
		}
	}
	if len(r.Path) > 0 {
		// A flat array of 2N uints: the cheapest deterministic wire for
		// an ordered point sequence.
		buf = codec.AppendUint(buf, recKeyPath)
		buf = codec.AppendArray(buf, len(r.Path)*2)
		for _, p := range r.Path {
			buf = codec.AppendUint(buf, p.LatE7U)
			buf = codec.AppendUint(buf, p.LonE7U)
		}
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
		case recKeyParent:
			var p [16]byte
			er = read16(d, p[:])
			r.Parent = &p
		case recKeyGeo:
			var cnt int
			cnt, er = d.ReadArray()
			if er == nil && cnt != 2 && cnt != 3 {
				er = errors.New("objects: geo is not [lat,lon] or [lat,lon,radius]")
			}
			if er == nil {
				g := &GeoShape{}
				if g.Point.LatE7U, er = d.ReadUint(); er == nil {
					if g.Point.LonE7U, er = d.ReadUint(); er == nil && cnt == 3 {
						g.RadiusM, er = d.ReadUint()
					}
				}
				if er == nil && cnt == 3 && g.RadiusM == 0 {
					er = errors.New("objects: zero radius must be omitted")
				}
				r.Geo = g
			}
		case recKeyPath:
			var cnt int
			cnt, er = d.ReadArray()
			if er == nil {
				if cnt%2 != 0 || cnt > MaxRoutePoints*2 {
					er = errors.New("objects: malformed path")
				}
				for i := 0; i < cnt/2 && er == nil; i++ {
					var p geo.Point
					if p.LatE7U, er = d.ReadUint(); er == nil {
						p.LonE7U, er = d.ReadUint()
					}
					r.Path = append(r.Path, p)
				}
			}
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
