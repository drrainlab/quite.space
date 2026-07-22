// Keep in Space projection (LR-1): the Shelf. Semantics — OR across people,
// LWW within one person:
//
//	Shelf object = OR of every member's keep state for a target
//	User keep    = LWW register of (author, target): kept | unkept
//
// An object stays on the Shelf while at least one member's register says
// kept. A regular unkeep must be signed by the keep author; removing someone
// else's keep is reserved for the space controller (moderation). Both checks
// run against the envelope signer here, independent of what the API let in.
//
// Keep state is deliberately NOT part of Digest (like publications and
// apps): under the pending-eviction cap the projection is deterministic, and
// at the cap eviction depends on arrival order — bounded honesty beats
// unbounded memory.
package reducers

import (
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/keep"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// Pending bounds (plan LR-1): keeps whose target has not been seen yet are
// held, bounded by count AND by note memory, evicting oldest first. The Shelf
// never initiates fetching unknown targets.
const (
	maxPendingKeepTargets = 256
	maxPendingKeepBytes   = 64 << 10
)

type keepReg struct {
	kept  bool
	note  string
	clock uint64
	eid   id.EventID
}

type keepRec struct {
	byAuthor map[id.PrincipalID]*keepReg
}

// ShelfKeeper is one member's active keep of a target.
type ShelfKeeper struct {
	Author  id.PrincipalID
	Note    string
	Clock   uint64
	EventID id.EventID
}

// ShelfItem is one Shelf object: a target plus everyone keeping it.
type ShelfItem struct {
	Target  id.EventID
	Kind    string // text|visual|video|voice|audio|file|link|publication|app
	Removed bool   // target tombstoned → placeholder, the keep never vanishes silently
	Keepers []ShelfKeeper
	// Latest keep (clock, event id) — the deterministic sort key.
	LatestClock uint64
	LatestEID   id.EventID
}

// kindSchema maps a feed entry kind to its schema id for the canonical
// keepable allowlist (protocol/keep).
func kindSchema(k EntryKind) string {
	switch k {
	case KindText:
		return schemas.MessageText
	case KindVisual:
		return schemas.BlockVisual
	case KindVideo:
		return schemas.BlockVideo
	case KindVoice:
		return schemas.BlockVoice
	case KindAudio:
		return schemas.BlockAudio
	case KindFile:
		return schemas.BlockFile
	case KindLink:
		return schemas.BlockLink
	}
	return ""
}

// KeepTargetStatus classifies a keep target against current state:
// kind ("" when unresolved), whether the target is known, and whether it is
// keepable. The node API uses the same answer at emit time.
func (s *State) KeepTargetStatus(target id.EventID) (kind string, resolved, keepable bool) {
	if rec, ok := s.entries[target]; ok && rec.entry.Kind != "" {
		sch := kindSchema(rec.entry.Kind)
		return string(rec.entry.Kind), true, sch != "" && keep.KeepableSchema(sch)
	}
	if _, ok := s.pubTargets[target]; ok {
		return "publication", true, true
	}
	if _, ok := s.appInstanceEvents[target]; ok {
		return "app", true, true
	}
	return "", false, false
}

func (s *State) keepRecFor(target id.EventID) *keepRec {
	if s.keeps == nil {
		s.keeps = map[id.EventID]*keepRec{}
	}
	rec, ok := s.keeps[target]
	if !ok {
		rec = &keepRec{byAuthor: map[id.PrincipalID]*keepReg{}}
		s.keeps[target] = rec
		if _, resolved, _ := s.KeepTargetStatus(target); !resolved {
			s.keepPending = append(s.keepPending, target)
			s.enforceKeepPending()
		}
	}
	return rec
}

// enforceKeepPending drops resolved ids from the queue lazily, then evicts
// the OLDEST unresolved targets while over the count or memory budget.
func (s *State) enforceKeepPending() {
	live := s.keepPending[:0]
	total := 0
	for _, t := range s.keepPending {
		rec, ok := s.keeps[t]
		if !ok {
			continue
		}
		if _, resolved, _ := s.KeepTargetStatus(t); resolved {
			continue
		}
		live = append(live, t)
		for _, reg := range rec.byAuthor {
			total += len(reg.note) + 64
		}
	}
	s.keepPending = live
	for len(s.keepPending) > 0 &&
		(len(s.keepPending) > maxPendingKeepTargets || total > maxPendingKeepBytes) {
		oldest := s.keepPending[0]
		s.keepPending = s.keepPending[1:]
		if rec, ok := s.keeps[oldest]; ok {
			for _, reg := range rec.byAuthor {
				total -= len(reg.note) + 64
			}
			delete(s.keeps, oldest)
			s.Unsupported["keep:pending_evicted"]++
		}
	}
}

// resolveKeepTarget runs when a target object materializes: a keep folded
// before its target now either sticks (keepable) or is discarded (allowlist
// holds at fold regardless of arrival order).
func (s *State) resolveKeepTarget(target id.EventID) {
	if s.keeps == nil {
		return
	}
	if _, ok := s.keeps[target]; !ok {
		return
	}
	if _, resolved, keepable := s.KeepTargetStatus(target); resolved && !keepable {
		delete(s.keeps, target)
		s.Unsupported["keep:not_keepable"]++
	}
}

func (s *State) applyKept(env *signal.Envelope, eid id.EventID) {
	k, err := keep.DecodeKept(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	if _, resolved, keepable := s.KeepTargetStatus(k.Target); resolved && !keepable {
		s.Unsupported["keep:not_keepable"]++
		return
	}
	rec := s.keepRecFor(k.Target)
	cur := rec.byAuthor[env.Principal]
	if cur != nil && !later(env.LogicalClock, eid, cur.clock, cur.eid) {
		return
	}
	rec.byAuthor[env.Principal] = &keepReg{kept: true, note: k.Note,
		clock: env.LogicalClock, eid: eid}
}

func (s *State) applyUnkept(env *signal.Envelope, eid id.EventID) {
	u, err := keep.DecodeUnkept(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	// Authorization against the SIGNER: self-unkeep, or controller moderation.
	if env.Principal != u.KeepAuthor &&
		(s.Controller == nil || *s.Controller != env.Principal) {
		s.Unsupported["keep:unauthorized_unkeep"]++
		return
	}
	// The register must exist even when the unkeep arrives first — an
	// older keep folding later must LOSE to this unkeep.
	rec := s.keepRecFor(u.Target)
	cur := rec.byAuthor[u.KeepAuthor]
	if cur != nil && !later(env.LogicalClock, eid, cur.clock, cur.eid) {
		return
	}
	rec.byAuthor[u.KeepAuthor] = &keepReg{kept: false,
		clock: env.LogicalClock, eid: eid}
}

// Shelf returns the space's kept objects: every RESOLVED target with at
// least one active keep, newest keep first. Unresolved targets stay
// invisible (bounded pending); tombstoned targets appear with Removed=true.
func (s *State) Shelf() []ShelfItem {
	out := make([]ShelfItem, 0, len(s.keeps))
	for target, rec := range s.keeps {
		kind, resolved, keepable := s.KeepTargetStatus(target)
		if !resolved || !keepable {
			continue
		}
		item := ShelfItem{Target: target, Kind: kind}
		if er, ok := s.entries[target]; ok && er.tomb {
			item.Removed = true
		}
		for author, reg := range rec.byAuthor {
			if !reg.kept {
				continue
			}
			item.Keepers = append(item.Keepers, ShelfKeeper{
				Author: author, Note: reg.note, Clock: reg.clock, EventID: reg.eid,
			})
			if later(reg.clock, reg.eid, item.LatestClock, item.LatestEID) {
				item.LatestClock, item.LatestEID = reg.clock, reg.eid
			}
		}
		if len(item.Keepers) == 0 {
			continue // OR: no active keeps → not on the Shelf
		}
		sort.Slice(item.Keepers, func(i, j int) bool {
			return !later(item.Keepers[i].Clock, item.Keepers[i].EventID,
				item.Keepers[j].Clock, item.Keepers[j].EventID)
		})
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return later(out[i].LatestClock, out[i].LatestEID,
			out[j].LatestClock, out[j].LatestEID)
	})
	return out
}

// KeepCount reports how many members actively keep a target.
func (s *State) KeepCount(target id.EventID) int {
	rec, ok := s.keeps[target]
	if !ok {
		return 0
	}
	n := 0
	for _, reg := range rec.byAuthor {
		if reg.kept {
			n++
		}
	}
	return n
}

// KeepState reports one member's current register for a target (for the UI's
// "kept by me" affordance).
func (s *State) KeepState(target id.EventID, author id.PrincipalID) (kept bool, note string) {
	rec, ok := s.keeps[target]
	if !ok {
		return false, ""
	}
	reg, ok := rec.byAuthor[author]
	if !ok || !reg.kept {
		return false, ""
	}
	return true, reg.note
}
