// Resonance projection (RP-1). Cardinality SINGLE: one LWW register per
// (target, actor) — a set replaces the actor's whole slot, a clear occupies
// it with {active:false} so a clear folding before an older set still wins
// (the keep.go unkeep-before-keep construction).
//
// Convergence over boundedness (review correction 1): unresolved registers —
// reactions whose target has not been seen — stay in the SAME map and are
// simply not projected until the target resolves. There is NO eviction:
// every register corresponds to at least one event already stored in the
// append-only log, so register memory is bounded by the log itself, and
// identical event sets fold to identical state in every arrival order.
//
// The palette is a single LWW register accepted only from the space
// Controller. The Controller is fixed in the space manifest at creation and
// never changes in M1 (invariant; a future controller-transfer design must
// revisit palette authority resolution at the event's causal position).
//
// Resonance state is NOT part of Digest (precedent: keep/pubs/apps).
// ResonanceDigest() exists for tests: permutation matrices must show equal
// main Digest AND equal resonance state.
package reducers

import (
	"crypto/sha256"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/resonance"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

type resReg struct {
	active   bool
	reaction resonance.Reaction
	clock    uint64
	eid      id.EventID
}

type resRec struct {
	byActor map[id.PrincipalID]*resReg
}

type resPaletteReg struct {
	palette resonance.Palette
	clock   uint64
	eid     id.EventID
}

// ResonanceGroup is one aggregated reaction on a target.
type ResonanceGroup struct {
	Reaction resonance.Reaction
	Count    int
	Actors   []id.PrincipalID // sorted by bytes
	// WireClock/WireEID: the latest register in the group — the
	// deterministic source for an unknown key's wire fallback.
	WireClock uint64
	WireEID   id.EventID
}

// ResonanceAggregate is the projection of one target.
type ResonanceAggregate struct {
	Groups []ResonanceGroup // canonical order: GroupKey lexicographic
	Total  int
	// Revision: max (clock, event id) across the target's registers —
	// changes whenever the aggregate could have changed (a clear+set that
	// keeps the count still bumps it). Client arrival effects key off this.
	RevClock uint64
	RevEID   id.EventID
}

// ResonanceTargetStatus reports whether a target is a known reactable
// object: a live feed entry, a publication stable target, or an app
// instance creation event.
func (s *State) ResonanceTargetStatus(target id.EventID) (resolved bool) {
	if rec, ok := s.entries[target]; ok && rec.entry.Kind != "" {
		return true
	}
	if _, ok := s.pubTargets[target]; ok {
		return true
	}
	if _, ok := s.appInstanceEvents[target]; ok {
		return true
	}
	if _, ok := s.objTargets[target]; ok {
		return true
	}
	return false
}

func (s *State) resRecFor(target id.EventID) *resRec {
	if s.resonance == nil {
		s.resonance = map[id.EventID]*resRec{}
	}
	rec, ok := s.resonance[target]
	if !ok {
		rec = &resRec{byActor: map[id.PrincipalID]*resReg{}}
		s.resonance[target] = rec
	}
	return rec
}

func (s *State) applyResonanceSet(env *signal.Envelope, eid id.EventID) {
	p, err := resonance.DecodeSet(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.resRecFor(p.Target)
	cur := rec.byActor[env.Principal]
	if cur != nil && !later(env.LogicalClock, eid, cur.clock, cur.eid) {
		return // stale or duplicate: idempotent
	}
	rec.byActor[env.Principal] = &resReg{active: true, reaction: p.Reaction,
		clock: env.LogicalClock, eid: eid}
}

func (s *State) applyResonanceClear(env *signal.Envelope, eid id.EventID) {
	p, err := resonance.DecodeClear(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	// The register is created even when the clear arrives first — an older
	// set folding later must LOSE to it.
	rec := s.resRecFor(p.Target)
	cur := rec.byActor[env.Principal]
	if cur != nil && !later(env.LogicalClock, eid, cur.clock, cur.eid) {
		return
	}
	rec.byActor[env.Principal] = &resReg{active: false,
		clock: env.LogicalClock, eid: eid}
}

func (s *State) applyResonancePalette(env *signal.Envelope, eid id.EventID) {
	pal, err := resonance.DecodePalette(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	// Authorized against the SIGNER: only the space controller tunes the
	// palette (immutable within the space's lifetime in M1).
	if s.Controller == nil || *s.Controller != env.Principal {
		s.Unsupported["resonance:unauthorized_palette"]++
		return
	}
	if s.resPalette != nil && !later(env.LogicalClock, eid, s.resPalette.clock, s.resPalette.eid) {
		return
	}
	s.resPalette = &resPaletteReg{palette: *pal, clock: env.LogicalClock, eid: eid}
}

// ActivePalette returns the space palette, falling back to the built-in
// default until the controller publishes one.
func (s *State) ActivePalette() (resonance.Palette, bool) {
	if s.resPalette != nil {
		return s.resPalette.palette, true
	}
	return resonance.DefaultPalette(), false
}

// resonanceHidden reports whether a target's aggregate must be hidden:
// tombstoned entries and archived publications hide their reactions (the
// registers survive — restore brings them back).
func (s *State) resonanceHidden(target id.EventID) bool {
	if rec, ok := s.entries[target]; ok {
		return rec.tomb
	}
	if docID, ok := s.pubTargets[target]; ok {
		if p, ok := s.publications[docID]; ok {
			return p.archived()
		}
	}
	return false
}

// ResonanceFor projects one target's aggregate. Unresolved or hidden
// targets project empty.
func (s *State) ResonanceFor(target id.EventID) ResonanceAggregate {
	agg := ResonanceAggregate{}
	rec, ok := s.resonance[target]
	if !ok || !s.ResonanceTargetStatus(target) || s.resonanceHidden(target) {
		return agg
	}
	byKey := map[string]*ResonanceGroup{}
	for actor, reg := range rec.byActor {
		if later(reg.clock, reg.eid, agg.RevClock, agg.RevEID) {
			agg.RevClock, agg.RevEID = reg.clock, reg.eid
		}
		if !reg.active {
			continue
		}
		gk := reg.reaction.GroupKey()
		g, ok := byKey[gk]
		if !ok {
			g = &ResonanceGroup{Reaction: reg.reaction}
			byKey[gk] = g
		}
		g.Count++
		g.Actors = append(g.Actors, actor)
		// The group's representative reaction (and wire fallback source) is
		// the register latest in (clock, eid) — deterministic.
		if later(reg.clock, reg.eid, g.WireClock, g.WireEID) {
			g.WireClock, g.WireEID = reg.clock, reg.eid
			g.Reaction = reg.reaction
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := byKey[k]
		sort.Slice(g.Actors, func(i, j int) bool {
			return string(g.Actors[i][:]) < string(g.Actors[j][:])
		})
		agg.Groups = append(agg.Groups, *g)
		agg.Total += g.Count
	}
	return agg
}

// OwnResonance reports one actor's active reaction (viewer-relative — API
// layer only, never part of any digest).
func (s *State) OwnResonance(target id.EventID, me id.PrincipalID) (resonance.Reaction, bool) {
	rec, ok := s.resonance[target]
	if !ok {
		return resonance.Reaction{}, false
	}
	reg, ok := rec.byActor[me]
	if !ok || !reg.active {
		return resonance.Reaction{}, false
	}
	return reg.reaction, true
}

// ResonanceDigest hashes the COMPLETE resonance state — registers (resolved
// and unresolved, active and cleared) and the palette register. It is a
// TEST oracle for order-independence (review correction 10), deliberately
// not part of the network Digest.
func (s *State) ResonanceDigest() [32]byte {
	h := sha256.New()
	targets := make([]id.EventID, 0, len(s.resonance))
	for t := range s.resonance {
		targets = append(targets, t)
	}
	sort.Slice(targets, func(i, j int) bool {
		return string(targets[i][:]) < string(targets[j][:])
	})
	for _, t := range targets {
		h.Write(t[:])
		rec := s.resonance[t]
		actors := make([]id.PrincipalID, 0, len(rec.byActor))
		for a := range rec.byActor {
			actors = append(actors, a)
		}
		sort.Slice(actors, func(i, j int) bool {
			return string(actors[i][:]) < string(actors[j][:])
		})
		for _, a := range actors {
			reg := rec.byActor[a]
			h.Write(a[:])
			if reg.active {
				h.Write([]byte{1})
				h.Write([]byte(reg.reaction.GroupKey()))
				h.Write([]byte(reg.reaction.Fallback))
			} else {
				h.Write([]byte{0})
			}
			h.Write(reg.eid[:])
		}
	}
	if s.resPalette != nil {
		h.Write([]byte(s.resPalette.palette.PaletteID))
		h.Write(s.resPalette.eid[:])
		for _, sl := range s.resPalette.palette.Slots {
			h.Write([]byte(sl.Key))
			h.Write([]byte(sl.Label))
			h.Write([]byte(sl.Fallback))
		}
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}
