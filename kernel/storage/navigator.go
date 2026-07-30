// The Navigator: how THIS DEVICE organises what it already has (NAV-0).
//
// Nothing here is a protocol event, nothing syncs, and nothing travels. Every
// element is a REFERENCE to a Terminal that exists anyway — a person, a
// space, an agent, a catalog — so a group is a collection of links and never
// an owner. The protocol's own type boundary is untouched: Human, Agent and
// Space terminals stay different things, and the Navigator simply declines to
// know their internals. Kind, title, status and capabilities are resolved
// from the Terminal's CURRENT state every time a row is drawn, which is why
// none of them is stored: a stored kind could only ever go stale.
//
// It lives in the encrypted keystore rather than in localStorage or a plain
// file, for reasons that are about honesty rather than convenience.
// localStorage is keyed by origin and port, so a port change would silently
// empty somebody's groups, and it sits outside what node/backup.go captures —
// groups would survive a disk failure but not a new port. Settings is a
// full-replace blob, so every pin toggle would re-send the LLM config. And
// group names are personal: "therapy", "job hunt". SpaceMeta.LocalTitle
// already put "what this device privately calls a space" in the keystore;
// splitting the same idea across two stores is the drift you pay for later.
package storage

import (
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// NavRef is one link in the Navigator. It is a reference, never a copy.
type NavRef struct {
	Terminal id.TerminalID
	// Label is the name this had when it was put here, and it is read ONLY
	// when Terminal no longer resolves — so a dangling pin can say "Anna"
	// instead of eight hex characters. It is never consulted while the
	// terminal is present, because the live name is the true one.
	Label string
}

// NavGroup is a named collection of links. One terminal may sit in several
// groups at once and still be exactly one terminal; nothing is moved and
// nothing is owned.
type NavGroup struct {
	ID        string // device-local, minted on create
	Title     string
	Children  []NavRef
	Collapsed bool
}

// NavState is the whole of a person's local arrangement.
type NavState struct {
	// Version increments on every write. It is what makes a lost update
	// loud (a 409) instead of a silent revert.
	Version uint64
	Pins    []NavRef
	Groups  []NavGroup
	// Catalogs is a separate list rather than a stored kind, for the same
	// reason it is a separate section.
	Catalogs []NavRef
	// Recent is what the share picker offers first.
	Recent []NavRef
	// Collapsed holds built-in section ids: pinned|groups|spaces|people|catalogs.
	Collapsed []string
}

// navRefFields, navGroupFields and navStateFields are the CURRENT arities.
// A later wave appends at the end and bumps the number; every reader skips
// the tail it does not know (see the loops below), which is what keeps an
// upgrade from being one-way.
const (
	navRefFields   = 2
	navGroupFields = 4
	navStateFields = 6
)

func appendNavRefs(buf []byte, refs []NavRef) []byte {
	buf = codec.AppendArray(buf, len(refs))
	for _, r := range refs {
		buf = codec.AppendArray(buf, navRefFields)
		buf = codec.AppendBytes(buf, r.Terminal[:])
		buf = codec.AppendText(buf, r.Label)
	}
	return buf
}

func readNavRefs(d *codec.Decoder) ([]NavRef, error) {
	n, err := d.ReadArray()
	if err != nil {
		return nil, err
	}
	out := make([]NavRef, 0, n)
	for range n {
		acount, er := d.ReadArray()
		if er != nil {
			return nil, er
		}
		var r NavRef
		b, er := d.ReadBytes()
		if er != nil {
			return nil, er
		}
		copy(r.Terminal[:], b)
		if r.Label, er = d.ReadText(); er != nil {
			return nil, er
		}
		for i := navRefFields; i < acount; i++ {
			if e := d.SkipItem(); e != nil {
				return nil, e
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// appendNavState writes the whole arrangement. Every field is an ordered
// slice, so there is no map to sort and determinism is free.
func appendNavState(buf []byte, s NavState) []byte {
	buf = codec.AppendArray(buf, navStateFields)
	buf = codec.AppendUint(buf, s.Version)
	buf = appendNavRefs(buf, s.Pins)
	buf = codec.AppendArray(buf, len(s.Groups))
	for _, g := range s.Groups {
		buf = codec.AppendArray(buf, navGroupFields)
		buf = codec.AppendText(buf, g.ID)
		buf = codec.AppendText(buf, g.Title)
		buf = codec.AppendBool(buf, g.Collapsed)
		buf = appendNavRefs(buf, g.Children)
	}
	buf = appendNavRefs(buf, s.Catalogs)
	buf = appendNavRefs(buf, s.Recent)
	buf = codec.AppendArray(buf, len(s.Collapsed))
	for _, c := range s.Collapsed {
		buf = codec.AppendText(buf, c)
	}
	return buf
}

func readNavState(d *codec.Decoder) (NavState, error) {
	var s NavState
	acount, err := d.ReadArray()
	if err != nil {
		return s, err
	}
	if s.Version, err = d.ReadUint(); err != nil {
		return s, err
	}
	if s.Pins, err = readNavRefs(d); err != nil {
		return s, err
	}
	gn, err := d.ReadArray()
	if err != nil {
		return s, err
	}
	s.Groups = make([]NavGroup, 0, gn)
	for range gn {
		gcount, er := d.ReadArray()
		if er != nil {
			return s, er
		}
		var g NavGroup
		if g.ID, er = d.ReadText(); er != nil {
			return s, er
		}
		if g.Title, er = d.ReadText(); er != nil {
			return s, er
		}
		if g.Collapsed, er = d.ReadBool(); er != nil {
			return s, er
		}
		if g.Children, er = readNavRefs(d); er != nil {
			return s, er
		}
		for i := navGroupFields; i < gcount; i++ {
			if e := d.SkipItem(); e != nil {
				return s, e
			}
		}
		s.Groups = append(s.Groups, g)
	}
	if s.Catalogs, err = readNavRefs(d); err != nil {
		return s, err
	}
	if s.Recent, err = readNavRefs(d); err != nil {
		return s, err
	}
	cn, err := d.ReadArray()
	if err != nil {
		return s, err
	}
	s.Collapsed = make([]string, 0, cn)
	for range cn {
		c, er := d.ReadText()
		if er != nil {
			return s, er
		}
		s.Collapsed = append(s.Collapsed, c)
	}
	for i := navStateFields; i < acount; i++ {
		if e := d.SkipItem(); e != nil {
			return s, e
		}
	}
	return s, nil
}
