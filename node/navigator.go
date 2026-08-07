// The Navigator, node side (NAV-0).
//
// This layer does two things and deliberately no more: it validates, and it
// persists. Every element is a link to a Terminal that exists anyway, so
// there is nothing here to resolve, rank or project — the client draws rows
// from the LIVE space list and uses this only for arrangement.
//
// Device-local is not the same as unchecked. The bounds below exist because
// a local API is still an API: a loop in a client, or a stray script with
// the session token, should not be able to write a keystore record that
// takes a second to encode on every save.
package node

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// Bounds. Generous enough that nobody organised will meet them, small
// enough that the encoded record stays trivial.
const (
	maxNavPins     = 64
	maxNavGroups   = 64
	maxNavChildren = 256
	maxNavCatalogs = 64
	maxNavRecent   = 16
	maxNavTitle    = 64 // runes, not bytes
	maxNavLabel    = 96
	// A source is a pasted share link, so it is bounded by what a link can
	// be rather than by what a person can be expected to keep track of.
	maxNavSources    = 32
	maxNavSourceLink = 512
)

// ErrNavigatorStale says somebody else wrote first. It is a 409 rather than
// a silent overwrite because the Navigator is ordered, and a lost update
// here means somebody's arrangement quietly reverted — the exact failure
// the Settings blob has and nobody notices.
var ErrNavigatorStale = errors.New("node: the Navigator changed since you read it")

// Navigator returns this device's arrangement.
func (r *Runtime) Navigator() storage.NavState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneNav(r.ks.Navigator)
}

// SetNavigator replaces the whole arrangement.
//
// Whole-document, which was the wrong shape for Settings and is the right
// one here: the Navigator IS one document with one editor, and ORDER is the
// substance of it — a field-by-field API could not express "these three
// moved". The base version is what keeps that from becoming the Settings
// problem: a concurrent write is refused loudly instead of reverting.
func (r *Runtime) SetNavigator(next storage.NavState, baseVersion uint64) (storage.NavState, error) {
	if err := validateNav(&next); err != nil {
		return storage.NavState{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ks.Navigator.Version != baseVersion {
		return storage.NavState{}, ErrNavigatorStale
	}
	next.Version = baseVersion + 1
	r.ks.Navigator = next
	if err := r.saveKeystore(); err != nil {
		return storage.NavState{}, err
	}
	return cloneNav(next), nil
}

// cloneNav hands out a copy. The caller is the API layer and a shared slice
// header would let a handler mutate the keystore without a lock.
func cloneNav(s storage.NavState) storage.NavState {
	out := s
	out.Pins = append([]storage.NavRef(nil), s.Pins...)
	out.Catalogs = append([]storage.NavRef(nil), s.Catalogs...)
	out.Recent = append([]storage.NavRef(nil), s.Recent...)
	out.Collapsed = append([]string(nil), s.Collapsed...)
	out.Sources = append([]storage.NavSource(nil), s.Sources...)
	out.Groups = make([]storage.NavGroup, len(s.Groups))
	for i, g := range s.Groups {
		g.Children = append([]storage.NavRef(nil), s.Groups[i].Children...)
		out.Groups[i] = g
	}
	return out
}

func validateNav(s *storage.NavState) error {
	if len(s.Pins) > maxNavPins {
		return errors.New("node: too many pins")
	}
	if len(s.Groups) > maxNavGroups {
		return errors.New("node: too many groups")
	}
	if len(s.Catalogs) > maxNavCatalogs {
		return errors.New("node: too many catalogs")
	}
	if len(s.Recent) > maxNavRecent {
		s.Recent = s.Recent[:maxNavRecent]
	}
	if err := validateRefs(s.Pins); err != nil {
		return err
	}
	if err := validateRefs(s.Catalogs); err != nil {
		return err
	}
	if err := validateRefs(s.Recent); err != nil {
		return err
	}
	if err := normalizeSources(s); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range s.Groups {
		g := &s.Groups[i]
		g.ID = strings.TrimSpace(g.ID)
		g.Title = strings.TrimSpace(g.Title)
		if g.ID == "" {
			return errors.New("node: a group needs an id")
		}
		if seen[g.ID] {
			return errors.New("node: two groups share an id")
		}
		seen[g.ID] = true
		if utf8.RuneCountInString(g.Title) > maxNavTitle {
			return errors.New("node: that group name is too long")
		}
		if len(g.Children) > maxNavChildren {
			return errors.New("node: too many items in one group")
		}
		if err := validateRefs(g.Children); err != nil {
			return err
		}
	}
	return nil
}

// normalizeSources trims, drops the empty ones and keeps the FIRST of any
// duplicate link.
//
// Normalising rather than refusing is the right shape here because the
// client sends the whole document on every write: a person who adds a
// directory they already have should see one row, not a 400 telling them
// their whole arrangement is invalid. Two links to the same space through
// different relays are NOT duplicates and are not treated as such — a link
// is relay-bound, and the second one exists precisely so the place survives
// the first relay going away.
func normalizeSources(s *storage.NavState) error {
	if len(s.Sources) > maxNavSources {
		return errors.New("node: too many directories")
	}
	out := make([]storage.NavSource, 0, len(s.Sources))
	seen := map[string]bool{}
	for _, src := range s.Sources {
		src.Link = strings.TrimSpace(src.Link)
		src.Label = strings.TrimSpace(src.Label)
		if src.Link == "" || seen[src.Link] {
			continue
		}
		if len(src.Link) > maxNavSourceLink {
			return errors.New("node: that directory link is too long")
		}
		if utf8.RuneCountInString(src.Label) > maxNavLabel {
			return errors.New("node: that directory name is too long")
		}
		seen[src.Link] = true
		out = append(out, src)
	}
	s.Sources = out
	return nil
}

// validateRefs bounds the fallback label. The terminal id needs no check —
// an id that resolves to nothing renders as a dangling row, which is the
// designed behaviour rather than an error: a space that comes back finds
// its pin waiting.
func validateRefs(refs []storage.NavRef) error {
	for i := range refs {
		if utf8.RuneCountInString(refs[i].Label) > maxNavLabel {
			return errors.New("node: that label is too long")
		}
	}
	return nil
}

// ---- HTTP ----

type navRefJSON struct {
	Terminal string `json:"terminal"`
	Label    string `json:"label,omitempty"`
}

type navGroupJSON struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Children  []navRefJSON `json:"children"`
	Collapsed bool         `json:"collapsed,omitempty"`
}

type navSourceJSON struct {
	Link    string `json:"link"`
	Label   string `json:"label,omitempty"`
	AddedAt uint64 `json:"added_at,omitempty"`
}

type navStateJSON struct {
	Version   uint64         `json:"version"`
	Pins      []navRefJSON   `json:"pins"`
	Groups    []navGroupJSON `json:"groups"`
	Catalogs  []navRefJSON   `json:"catalogs"`
	Recent    []navRefJSON   `json:"recent"`
	Collapsed []string       `json:"collapsed"`
	// Sources and OfficialOff are what Discover reads from (CAT-0b). They
	// travel on the SAME document as everything else, so a client that PUTs
	// the whole state after a pin toggle must send them back — the round
	// trip is the contract, and dropping them here would silently undo a
	// person's Add to Discover on their next drag.
	Sources     []navSourceJSON `json:"sources"`
	OfficialOff bool            `json:"official_off,omitempty"`
}

func navRefsOut(refs []storage.NavRef) []navRefJSON {
	out := make([]navRefJSON, 0, len(refs))
	for _, r := range refs {
		out = append(out, navRefJSON{Terminal: r.Terminal.Hex(), Label: r.Label})
	}
	return out
}

func navOut(s storage.NavState) navStateJSON {
	groups := make([]navGroupJSON, 0, len(s.Groups))
	for _, g := range s.Groups {
		groups = append(groups, navGroupJSON{
			ID: g.ID, Title: g.Title, Collapsed: g.Collapsed,
			Children: navRefsOut(g.Children),
		})
	}
	// Every list goes out as a list, never null: an empty arrangement and a
	// missing one are the same thing to a person, and a client that has to
	// write `(x || [])` five times will forget once.
	collapsed := make([]string, 0, len(s.Collapsed))
	collapsed = append(collapsed, s.Collapsed...)
	sources := make([]navSourceJSON, 0, len(s.Sources))
	for _, src := range s.Sources {
		sources = append(sources, navSourceJSON{
			Link: src.Link, Label: src.Label, AddedAt: src.AddedAt,
		})
	}
	return navStateJSON{
		Version: s.Version, Pins: navRefsOut(s.Pins), Groups: groups,
		Catalogs: navRefsOut(s.Catalogs), Recent: navRefsOut(s.Recent),
		Collapsed: collapsed, Sources: sources, OfficialOff: s.OfficialOff,
	}
}

func navRefsIn(in []navRefJSON) ([]storage.NavRef, error) {
	out := make([]storage.NavRef, 0, len(in))
	for _, r := range in {
		tid, err := id.ParseTerminalID(r.Terminal)
		if err != nil {
			return nil, errors.New("node: a Navigator entry has no readable terminal")
		}
		out = append(out, storage.NavRef{Terminal: tid, Label: r.Label})
	}
	return out, nil
}

func (a *APIServer) handleNavigator(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, navOut(a.rt.Navigator()))
}

func (a *APIServer) handleSetNavigator(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[navStateJSON](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var next storage.NavState
	if next.Pins, err = navRefsIn(body.Pins); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if next.Catalogs, err = navRefsIn(body.Catalogs); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if next.Recent, err = navRefsIn(body.Recent); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	for _, g := range body.Groups {
		children, er := navRefsIn(g.Children)
		if er != nil {
			httpErr(w, http.StatusBadRequest, er)
			return
		}
		next.Groups = append(next.Groups, storage.NavGroup{
			ID: g.ID, Title: g.Title, Collapsed: g.Collapsed, Children: children,
		})
	}
	next.Collapsed = body.Collapsed
	for _, src := range body.Sources {
		next.Sources = append(next.Sources, storage.NavSource{
			Link: src.Link, Label: src.Label, AddedAt: src.AddedAt,
		})
	}
	next.OfficialOff = body.OfficialOff

	saved, err := a.rt.SetNavigator(next, body.Version)
	if errors.Is(err, ErrNavigatorStale) {
		httpErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, navOut(saved))
}
