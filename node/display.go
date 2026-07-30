// What to call a space (QL-3).
//
// This used to live in quicklink.go and trigger on the literal string
// "my line", which was a sentinel standing in for "nobody named this". Once
// a link opens a NEW place every time, that sentinel is not even related to
// the spaces it would be naming — a three-person room called "my line" is
// simply wrong.
//
// So the marker becomes explicit and LOCAL, and the projection answers a
// question the sentinel never could: who is in here?
//
// Two things this deliberately does not do. It does not put an "unnamed"
// label into the manifest — that would push a purely local concept into
// every published copy, forever. And it does not render English in Go: the
// projection returns a KEY and the names, so the interface says it in the
// reader's own language.
package node

import (
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// SpaceDisplay is how a space asks to be shown. Exactly one of Text or Key
// is set: a name somebody chose, or a projection to render.
type SpaceDisplay struct {
	// Text is a name a person chose — local or in the manifest. Shown
	// verbatim, never translated.
	Text string
	// Key is the i18n key for a projected name (space.waiting,
	// space.with_one, space.someone_arrived, space.with_many).
	Key string
	// Names are the OTHER people here, deduplicated by person and never
	// including you.
	Names []string
}

// memberLike is the slice of a member card this projection needs. Keeping
// it narrow makes the rule testable without building a whole space.
type memberLike struct {
	principal id.PrincipalID
	name      string
}

// maxShownNames bounds a rendered name. Past this the interface says "and N
// more" rather than growing without limit.
const maxShownNames = 3

// projectDisplay is the whole rule, in one place.
//
// Dedup is by PRINCIPAL, not terminal. MemberCards returns one card per
// terminal, so a person carrying a laptop and a phone used to be two
// entries — enough to push a two-person space past the "exactly 2" branch
// and silently fall back to the raw title. A person is a person.
//
// Honest limit, recorded because it is load-bearing: the principal on a
// card is SELF-DECLARED (the manifest is signed by the terminal key, and no
// principal-key attestation exists in this codebase). Everyone here was
// already admitted by the owner, and the worst a lie achieves is a
// cosmetically collapsed name — so this is fine for DISPLAY and would not
// be fine for counting entrants. See the pass path, which counts devices.
func projectDisplay(cards []memberLike, me id.PrincipalID) SpaceDisplay {
	seen := map[id.PrincipalID]bool{me: true}
	var names []string
	unnamed := 0
	for _, c := range cards {
		if seen[c.principal] {
			continue
		}
		seen[c.principal] = true
		if c.name == "" {
			unnamed++
			continue
		}
		names = append(names, c.name)
	}
	switch {
	case len(names) == 0 && unnamed == 0:
		return SpaceDisplay{Key: "space.waiting"}
	case len(names) == 0:
		// Somebody is here but their manifest has not arrived. We know that
		// someone came and not yet who — say exactly that, rather than a hex
		// prefix or a pretence that the space is still empty.
		return SpaceDisplay{Key: "space.someone_arrived"}
	case len(names)+unnamed == 1:
		return SpaceDisplay{Key: "space.with_one", Names: names}
	}
	if len(names) > maxShownNames {
		names = names[:maxShownNames]
	}
	return SpaceDisplay{Key: "space.with_many", Names: names}
}

// displayFor decides what to call one space. Precedence, highest first:
//
//  1. a name THIS NODE chose — it always wins, including over the manifest
//  2. a name in the manifest
//  3. a projection from who is here
//
// The legacy "my line" sentinel is treated as case 3, so lines that already
// exist keep working and stop showing a string nobody chose.
func displayFor(meta spaceNaming, sp *terminals.Space, me id.PrincipalID) SpaceDisplay {
	if meta.LocalTitle != "" {
		return SpaceDisplay{Text: meta.LocalTitle}
	}
	if !meta.Unnamed && meta.Title != "" && meta.Title != LineTitle {
		return SpaceDisplay{Text: meta.Title}
	}
	if sp == nil {
		return SpaceDisplay{Key: "space.waiting"}
	}
	raw := sp.MemberCards(uint64(time.Now().Unix()))
	cards := make([]memberLike, 0, len(raw))
	for _, c := range raw {
		cards = append(cards, memberLike{principal: c.Principal, name: c.Name})
	}
	return projectDisplay(cards, me)
}

// spaceNaming is the naming slice of SpaceMeta, so displayFor can be tested
// without a keystore.
type spaceNaming struct {
	Title      string
	LocalTitle string
	Unnamed    bool
}

// englishDisplay renders a projection for callers that cannot translate —
// the space list's sort key, and any client that is not our web UI. The
// interface prefers the structured Display and only falls back to this.
func englishDisplay(d SpaceDisplay) string {
	if d.Text != "" {
		return d.Text
	}
	switch d.Key {
	case "space.waiting":
		return "waiting for someone"
	case "space.someone_arrived":
		return "someone arrived"
	case "space.with_one":
		if len(d.Names) > 0 {
			return d.Names[0]
		}
	case "space.with_many":
		return joinNames(d.Names)
	}
	return ""
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	out := ""
	for i, n := range names[:len(names)-1] {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out + " and " + names[len(names)-1]
}

// SetLocalTitle names a space for THIS DEVICE. It emits nothing, publishes
// nothing and touches no manifest — a private space's manifest is
// creation-immutable, so a shared name is not something this can offer.
// The interface must say so plainly rather than letting someone assume
// their counterpart sees it too.
func (r *Runtime) SetLocalTitle(tid id.TerminalID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta, ok := r.ks.Spaces[tid]
	if !ok {
		return errUnknownSpace
	}
	meta.LocalTitle = strings.TrimSpace(name)
	r.ks.Spaces[tid] = meta
	return r.saveKeystore()
}

// CreateSpaceUnnamed opens a space nobody has named. The manifest carries
// an empty title deliberately, and the local flag records that it was a
// choice rather than a failure to read.
func (r *Runtime) CreateSpaceUnnamed() (id.TerminalID, error) {
	tid, err := r.CreateSpace("")
	if err != nil {
		return id.TerminalID{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	meta := r.ks.Spaces[tid]
	meta.Unnamed = true
	r.ks.Spaces[tid] = meta
	return tid, r.saveKeystore()
}
