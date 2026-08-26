// Domain objects projection (SP-1): folds object.* events into per-object
// state, structurally the publications reducer (ADR-014 discipline): LWW by
// (LogicalClock, EventID), archive is a tombstone only an explicit later
// restore referencing that archive can lift, CreatedAt frozen at the first
// accepted revision.
//
// NAMING: the domain object here is objects.Record — NOT composition.Object,
// which is a placed visual reference (SC-0) and never owns state.
//
// Two owner invariants live here:
//   - Status is domain-local display state. This file (and the kernel at
//     large) never branches on its VALUE — it is carried, sorted around,
//     hashed, and shown, never interpreted.
//   - An object never owns its tasks. The object→task edge is DERIVED from
//     Card.ObjectID at projection time (TasksForObject); objRec embeds no
//     task state, so a task update never has to touch an object record.
package reducers

import (
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// maxObservationsPerTimeline bounds each per-object timeline AND the space
// journal. An observation is a feed-class event (prunable); the object's
// structural record must not grow with every note taken across a decade.
const maxObservationsPerTimeline = 200

type objRec struct {
	recRaw   []byte
	name     string
	author   id.PrincipalID
	revID    id.EventID
	revClock uint64
	prevRev  *id.EventID
	// createdAt is the wall clock of the FIRST accepted revision; later
	// revisions never move it (the publications rule).
	createdAt uint64

	// Archive and restore are two LWW REGISTERS and "archived" is a pure
	// function of both — NOT the publications-style mutable flag. The flag
	// form has an ordering hole this projection's convergence test caught:
	// a restore folded BEFORE its archive is dropped ("not archived yet")
	// and never reconsidered, so nodes that saw restore-first diverge
	// forever from nodes that saw archive-first.
	hasArchive   bool
	archiveEvent id.EventID
	archiveClock uint64
	restoreRef   *id.EventID // which archive event the restore undoes
	restoreEID   id.EventID
	restoreClock uint64

	// observations: bounded timeline, ascending (CreatedAt, Clock, EventID).
	// See insertObservation for the eviction law.
	observations []ObservationNote
}

// archived derives the lifecycle state: archived unless the latest restore
// explicitly references the latest archive and is later than it.
func (r *objRec) archived() bool {
	if !r.hasArchive {
		return false
	}
	if r.restoreRef == nil || *r.restoreRef != r.archiveEvent {
		return true
	}
	return !later(r.restoreClock, r.restoreEID, r.archiveClock, r.archiveEvent)
}

// ObservationNote is one human observation on a timeline (object or space
// journal). The same event is ALSO a feed entry — one event, two
// projections, no duplicate on the wire.
type ObservationNote struct {
	EventID    id.EventID
	Author     id.PrincipalID
	Text       string
	ObjectID   *[16]byte
	ObservedAt uint64 // author's claim; 0 = CreatedAt
	CreatedAt  uint64
	Clock      uint64
}

// ObservationNoteContent is the feed-entry projection of the same event.
type ObservationNoteContent struct {
	Text       string
	ObjectID   *[16]byte
	ObservedAt uint64
}

// Object is the projection of one domain object.
type Object struct {
	ObjectID        [16]byte
	Record          *objects.Record
	Raw             []byte
	Name            string
	Author          id.PrincipalID
	RevisionEventID id.EventID
	PrevRevision    *id.EventID
	Clock           uint64
	CreatedAt       uint64
	Archived        bool
	Observations    []ObservationNote
}

func (s *State) objRecFor(objectID [16]byte) *objRec {
	if s.objects == nil {
		s.objects = map[[16]byte]*objRec{}
	}
	rec, ok := s.objects[objectID]
	if !ok {
		rec = &objRec{}
		s.objects[objectID] = rec
		// Register the stable target. Resonance registers need no drain —
		// unresolved registers project as soon as the target resolves.
		if s.objTargets == nil {
			s.objTargets = map[id.EventID][16]byte{}
		}
		target := objects.Target(objectID)
		s.objTargets[target] = objectID
		// A keep of this object may have folded before the object.
		s.resolveKeepTarget(target)
	}
	return rec
}

// ObjectByTarget resolves a stable keep/reaction target to its object id.
func (s *State) ObjectByTarget(target id.EventID) ([16]byte, bool) {
	o, ok := s.objTargets[target]
	return o, ok
}

func (s *State) applyObjectRevision(env *signal.Envelope, eid id.EventID) {
	p, err := objects.DecodeRevisionPayload(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	r, err := objects.Decode(p.Record)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.objRecFor(r.ObjectID)
	if rec.recRaw != nil && !later(env.LogicalClock, eid, rec.revClock, rec.revID) {
		return // stale revision
	}
	if rec.recRaw == nil {
		rec.createdAt = env.CreatedAt
	}
	rec.recRaw = append([]byte(nil), p.Record...)
	rec.name = r.Name
	rec.author = env.Principal
	rec.revID = eid
	rec.revClock = env.LogicalClock
	rec.prevRev = p.PrevRevision
	// NOTE: a later revision does NOT clear an archive — only restore does.
}

func (s *State) applyObjectLifecycle(env *signal.Envelope, eid id.EventID, archive bool) {
	p, err := objects.DecodeLifecyclePayload(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.objRecFor(p.ObjectID)
	if archive {
		if rec.hasArchive && !later(env.LogicalClock, eid, rec.archiveClock, rec.archiveEvent) {
			return
		}
		rec.hasArchive = true
		rec.archiveEvent = eid
		rec.archiveClock = env.LogicalClock
		return
	}
	// Restore register: latest restore wins, ARRIVAL ORDER IRRELEVANT — a
	// restore folded before its archive still counts once the archive
	// lands, because archived() re-derives from both registers. It lifts
	// the archive only when it explicitly references that archive event
	// and is later than it (the publications invariant, kept).
	if rec.restoreRef != nil && !later(env.LogicalClock, eid, rec.restoreClock, rec.restoreEID) {
		return
	}
	rec.restoreRef = p.ArchivedRevision
	rec.restoreEID = eid
	rec.restoreClock = env.LogicalClock
}

// applyObservationNoted is the "one event, two projections" arm: the note
// becomes a quiet feed entry AND lands on a bounded timeline (the object's
// when ObjectID is set, the space journal otherwise).
func (s *State) applyObservationNoted(env *signal.Envelope, eid id.EventID) {
	o, err := schemas.DecodeNotedObservation(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	s.installEntry(eid, env, KindObservation, EntryContent{
		Observation: &ObservationNoteContent{Text: o.Text, ObjectID: o.ObjectID, ObservedAt: o.ObservedAt},
	})
	note := ObservationNote{
		EventID: eid, Author: env.Principal, Text: o.Text, ObjectID: o.ObjectID,
		ObservedAt: o.ObservedAt, CreatedAt: env.CreatedAt, Clock: env.LogicalClock,
	}
	if o.ObjectID != nil {
		rec := s.objRecFor(*o.ObjectID)
		rec.observations = s.insertObservation(rec.observations, note)
	} else {
		s.journalObs = s.insertObservation(s.journalObs, note)
	}
}

// insertObservation keeps a timeline sorted ascending by (CreatedAt, Clock,
// EventID) and bounded. THE EVICTION LAW IS A CONVERGENCE INVARIANT: the
// surviving set is exactly the greatest maxObservationsPerTimeline notes in
// that total order, so any arrival order converges to the same timeline.
// Evictions count into State.ObservationEvicted — a SEPARATE counter, not
// Unsupported: Unsupported means "did not understand the data", and these
// were understood and deliberately aged out. An operator a year in must not
// read 1834 evictions as 1834 mysteries.
func (s *State) insertObservation(list []ObservationNote, n ObservationNote) []ObservationNote {
	for _, e := range list {
		if e.EventID == n.EventID {
			return list // replayed duplicate: idempotent
		}
	}
	pos := sort.Search(len(list), func(i int) bool { return noteBefore(n, list[i]) })
	list = append(list, ObservationNote{})
	copy(list[pos+1:], list[pos:])
	list[pos] = n
	if len(list) > maxObservationsPerTimeline {
		list = append(list[:0], list[1:]...) // evict the OLDEST
		s.ObservationEvicted++
	}
	return list
}

func noteBefore(a, b ObservationNote) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	if a.Clock != b.Clock {
		return a.Clock < b.Clock
	}
	return string(a.EventID[:]) < string(b.EventID[:])
}

// Objects lists live objects, newest revision first.
func (s *State) Objects() []Object {
	return s.projectObjects(false)
}

// ArchivedObjects lists archived objects (for a restore UI).
func (s *State) ArchivedObjects() []Object {
	return s.projectObjects(true)
}

func (s *State) projectObjects(archived bool) []Object {
	out := make([]Object, 0, len(s.objects))
	for oid, rec := range s.objects {
		if rec.recRaw == nil || rec.archived() != archived {
			continue
		}
		r, err := objects.Decode(rec.recRaw)
		if err != nil {
			continue
		}
		out = append(out, Object{
			ObjectID: oid, Record: r, Raw: rec.recRaw,
			Name: rec.name, Author: rec.author,
			RevisionEventID: rec.revID, PrevRevision: rec.prevRev,
			Clock: rec.revClock, CreatedAt: rec.createdAt, Archived: rec.archived(),
			Observations: append([]ObservationNote(nil), rec.observations...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Clock != out[j].Clock {
			return out[i].Clock > out[j].Clock
		}
		return string(out[i].RevisionEventID[:]) > string(out[j].RevisionEventID[:])
	})
	return out
}

// ObjectByID returns one object's projection (live or archived).
func (s *State) ObjectByID(objectID [16]byte) (Object, bool) {
	rec, ok := s.objects[objectID]
	if !ok || rec.recRaw == nil {
		return Object{}, false
	}
	for _, o := range s.projectObjects(rec.archived()) {
		if o.ObjectID == objectID {
			return o, true
		}
	}
	return Object{}, false
}

// digestObjects enumerates every materialized record — live AND archived —
// in sorted object-id order, for the state digest only.
func (s *State) digestObjects() []Object {
	ids := make([][16]byte, 0, len(s.objects))
	for oid, rec := range s.objects {
		if rec.recRaw == nil {
			continue
		}
		ids = append(ids, oid)
	}
	sort.Slice(ids, func(i, j int) bool { return string(ids[i][:]) < string(ids[j][:]) })
	out := make([]Object, 0, len(ids))
	for _, oid := range ids {
		rec := s.objects[oid]
		out = append(out, Object{
			ObjectID: oid, Name: rec.name, RevisionEventID: rec.revID,
			Archived: rec.archived(), Observations: rec.observations,
		})
	}
	return out
}

// JournalObservations lists space-journal notes (no object), ascending.
func (s *State) JournalObservations() []ObservationNote {
	return append([]ObservationNote(nil), s.journalObs...)
}

// TasksForObject DERIVES the object→task edge from Card.ObjectID — the
// card reducer is the single truth; the object record embeds nothing.
func (s *State) TasksForObject(objectID [16]byte) []Card {
	var out []Card
	for _, c := range s.Cards() {
		if c.ObjectID != nil && *c.ObjectID == objectID {
			out = append(out, c)
		}
	}
	return out
}
