// Package keep is the Keep in Space protocol (LR-1): a member marks an
// existing event as part of the space's memory. Shelf semantics are OR
// across people, LWW within one person:
//
//	Shelf object = OR of every member's keep state for a target
//	User keep    = LWW register of the pair (author, target): kept | unkept
//
// A kept event carries ONLY a reference — never a snapshot of the target's
// content. Unkeeping someone else's keep is a moderation action reserved for
// the space controller; a regular unkeep must be signed by the keep author
// (both enforced against the envelope signature, in the reducer and the API).
package keep

import (
	"errors"
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/contract"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

const (
	SchemaKept   = "space.kept.v1"
	SchemaUnkept = "space.unkept.v1"
)

// MaxNoteLen bounds a keep note (bytes). Empty notes are normal.
const MaxNoteLen = 500

// Kept is space.kept.v1: {1: target_event_id, 2?: note}.
type Kept struct {
	Target id.EventID
	Note   string
}

func (k *Kept) Encode() ([]byte, error) {
	if len(k.Note) > MaxNoteLen {
		return nil, fmt.Errorf("keep: note exceeds %d bytes", MaxNoteLen)
	}
	n := 1
	if k.Note != "" {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendBytes(buf, k.Target[:])
	if k.Note != "" {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendText(buf, k.Note)
	}
	return buf, nil
}

func DecodeKept(payload []byte) (*Kept, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	k := &Kept{}
	seen := false
	for {
		key, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch key {
		case 1:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil {
				if len(b) != id.Size {
					return nil, errors.New("keep: target must be 32 bytes")
				}
				copy(k.Target[:], b)
				seen = true
			}
		case 2:
			k.Note, er = d.ReadText()
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
	if !seen {
		return nil, errors.New("keep: missing target")
	}
	if len(k.Note) > MaxNoteLen {
		return nil, fmt.Errorf("keep: note exceeds %d bytes", MaxNoteLen)
	}
	return k, nil
}

// Unkept is space.unkept.v1: {1: target_event_id, 2: keep_author}. It
// addresses one person's keep state — the (target, keep_author) pair — so
// unkeeping never touches anyone else's keep.
type Unkept struct {
	Target     id.EventID
	KeepAuthor id.PrincipalID
}

func (u *Unkept) Encode() []byte {
	buf := codec.AppendMap(nil, 2)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendBytes(buf, u.Target[:])
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendBytes(buf, u.KeepAuthor[:])
	return buf
}

func DecodeUnkept(payload []byte) (*Unkept, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	u := &Unkept{}
	var seenT, seenA bool
	for {
		key, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch key {
		case 1:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil {
				if len(b) != id.Size {
					return nil, errors.New("keep: target must be 32 bytes")
				}
				copy(u.Target[:], b)
				seenT = true
			}
		case 2:
			var b []byte
			b, er = d.ReadBytes()
			if er == nil {
				if len(b) != id.Size {
					return nil, errors.New("keep: keep_author must be 32 bytes")
				}
				copy(u.KeepAuthor[:], b)
				seenA = true
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
	if !seenT || !seenA {
		return nil, errors.New("keep: unkept missing target or keep_author")
	}
	return u, nil
}

// ---- Keepable allowlist (LR-1) ----
//
// v1 keepable content: text, image, audio (incl. voice), video, file, link,
// publications, app instances. Explicitly NOT keepable: reactions, listening
// and app commands, delivery/system events, permission/key events,
// tombstones, unkeep itself. The list is checked at emit (node API) AND at
// fold (reducer). Formalized into contract descriptors in LR-0b.

var keepableSchemas = map[string]bool{
	schemas.MessageText: true,
	schemas.BlockVisual: true,
	schemas.BlockVideo:  true,
	schemas.BlockVoice:  true,
	schemas.BlockAudio:  true,
	schemas.BlockFile:   true,
	schemas.BlockLink:   true,
}

// KeepableSchema reports whether events of this schema may be kept.
// Publications and app instances resolve through their own projections
// (stable pub target / instance event), not through this schema check.
func KeepableSchema(schema string) bool { return keepableSchemas[schema] }

// ---- Contracts (LR-0a registry) ----

type keptContract struct{}

func (keptContract) SchemaID() string        { return SchemaKept }
func (keptContract) Validate(p []byte) error { _, err := DecodeKept(p); return err }
func (keptContract) Fallback(p []byte) (string, error) {
	k, err := DecodeKept(p)
	if err != nil {
		return "", err
	}
	if k.Note != "" {
		return "kept a moment · " + k.Note, nil
	}
	return "kept a moment", nil
}
func (keptContract) AssetRefs(p []byte) ([]schemas.AssetRef, error) { return nil, nil }

type unkeptContract struct{}

func (unkeptContract) SchemaID() string        { return SchemaUnkept }
func (unkeptContract) Validate(p []byte) error { _, err := DecodeUnkept(p); return err }
func (unkeptContract) Fallback(p []byte) (string, error) {
	if _, err := DecodeUnkept(p); err != nil {
		return "", err
	}
	return "released a kept moment", nil
}
func (unkeptContract) AssetRefs(p []byte) ([]schemas.AssetRef, error) { return nil, nil }

func init() {
	contract.Register(keptContract{}, contract.Descriptor{SchemaID: SchemaKept})
	contract.Register(unkeptContract{}, contract.Descriptor{SchemaID: SchemaUnkept})
}
