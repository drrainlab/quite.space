package schemas

// asset.annotated.v1 (SP-2) — a UNIVERSAL media annotation: a human note
// on an asset, optionally at a position. "01:42 вокал суховат" on a mix
// today; a frame note on a video, a margin note on a PDF page tomorrow —
// the primitive is deliberately not music-shaped and not objects-scoped.
//
// Exactly two meanings, fixed (ADR-030):
//   - WITHOUT PositionMs: a note about the WHOLE asset;
//   - WITH PositionMs:    a point-in-time note.
// No ranges (01:42–01:51), no regions in v1 — that is the next level of
// complexity and it arrives as its own wave, not as an optional key here.
//
// Annotations are flat (no parent), immutable (no edit/delete in v1), and
// NEVER keep an asset alive: commentary must not immortalize a deleted
// take. The structural truth — what is attached, what is current — lives
// on object edges; this is conversation, and it ages like conversation.

import (
	"errors"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/protocol/codec"
)

// MaxAnnotationTextRunes bounds the note — a sentence, not a document.
const MaxAnnotationTextRunes = 1000

const (
	anKeyText     = 1
	anKeyID       = 2
	anKeyAsset    = 3
	anKeyPosition = 4
	anKeyObject   = 5
)

// AssetAnnotation is the payload of asset.annotated.v1.
type AssetAnnotation struct {
	Text string // key 1, required (doubles as the fallback)
	// AnnotationID is a node-minted handle: a replay-dedupe aid at the
	// API and a future deep-link anchor. It is NOT a reducer dedupe key —
	// with bounded timelines, first-sight-wins by id would be
	// arrival-order dependent; the reducer dedupes by EventID alone.
	AnnotationID [16]byte
	// Asset is the bare public asset id, 32 or 64 lowercase hex.
	Asset string
	// PositionMs: 0/absent = a note about the whole asset.
	PositionMs uint64
	hasPos     bool
	// ObjectID optionally scopes the note to a domain object's card.
	ObjectID *[16]byte
}

// SetPosition marks the note as point-in-time. Position 0 is a legal
// instant ("the very start"), so presence is tracked apart from the value.
func (a *AssetAnnotation) SetPosition(ms uint64) {
	a.PositionMs, a.hasPos = ms, true
}

// HasPosition reports whether this is a point-in-time note.
func (a *AssetAnnotation) HasPosition() bool { return a.hasPos }

func validAnnotationAssetHex(s string) bool {
	if len(s) != 32 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (a *AssetAnnotation) validate() error {
	if a.Text == "" {
		return errors.New("schemas: annotation requires text")
	}
	if !utf8.ValidString(a.Text) || utf8.RuneCountInString(a.Text) > MaxAnnotationTextRunes {
		return errors.New("schemas: annotation text invalid or too long")
	}
	if a.AnnotationID == ([16]byte{}) {
		return errors.New("schemas: annotation requires an id")
	}
	if !validAnnotationAssetHex(a.Asset) {
		return errors.New("schemas: annotation asset id is not 32/64 lowercase hex")
	}
	return nil
}

func (a *AssetAnnotation) Encode() ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	n := 3 // text, id, asset
	if a.hasPos {
		n++
	}
	if a.ObjectID != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, anKeyText)
	buf = codec.AppendText(buf, a.Text)
	buf = codec.AppendUint(buf, anKeyID)
	buf = codec.AppendBytes(buf, a.AnnotationID[:])
	buf = codec.AppendUint(buf, anKeyAsset)
	buf = codec.AppendText(buf, a.Asset)
	if a.hasPos {
		buf = codec.AppendUint(buf, anKeyPosition)
		buf = codec.AppendUint(buf, a.PositionMs)
	}
	if a.ObjectID != nil {
		buf = codec.AppendUint(buf, anKeyObject)
		buf = codec.AppendBytes(buf, a.ObjectID[:])
	}
	return buf, nil
}

func DecodeAssetAnnotation(payload []byte) (*AssetAnnotation, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	a := &AssetAnnotation{}
	for {
		k, ok, err := m.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch k {
		case anKeyText:
			a.Text, err = d.ReadText()
		case anKeyID:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 16 {
					err = errors.New("schemas: annotation id must be 16 bytes")
				} else {
					copy(a.AnnotationID[:], b)
				}
			}
		case anKeyAsset:
			a.Asset, err = d.ReadText()
		case anKeyPosition:
			a.PositionMs, err = d.ReadUint()
			a.hasPos = true
		case anKeyObject:
			var b []byte
			if b, err = d.ReadBytes(); err == nil {
				if len(b) != 16 {
					err = errors.New("schemas: object id must be 16 bytes")
				} else {
					var oid [16]byte
					copy(oid[:], b)
					a.ObjectID = &oid
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
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}

func init() {
	Register(AssetAnnotated, func(p []byte) error { _, err := DecodeAssetAnnotation(p); return err })
}
