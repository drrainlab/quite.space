package reducers

// SP-3.2 (ADR-034): the completion facts of recording sessions.
//
// THE OBJECT NEVER OWNS ITS SWEEPS — the TasksForObject law, word for
// word. sweep.completed.v1 is the canonical completion fact; the Sweep
// Object's status slug is a render cache, and where the two disagree
// the event wins. So the projection here is a flat bounded list plus a
// DERIVED filter, never a field on the object record: one truth, one
// place to look for it.

import (
	"sort"

	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// maxSweepsPerSpace bounds the list under the deterministic observation
// eviction law (evict the oldest, count honestly).
const maxSweepsPerSpace = 200

// SweepFact is one folded sweep.completed.v1.
type SweepFact struct {
	EventID    id.EventID
	Author     id.PrincipalID
	ObjectID   [16]byte // the Sweep Object
	StartedAt  uint64
	EndedAt    uint64
	DistanceM  uint64
	Result     string
	BBoxMin    geo.Point
	BBoxMax    geo.Point
	TrackAsset [32]byte
	Fallback   string
	CreatedAt  uint64
	Clock      uint64
}

func (s *State) applySweepCompleted(env *signal.Envelope, eid id.EventID) {
	c, err := schemas.DecodeCompletedSweep(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	s.sweeps = s.insertSweep(s.sweeps, SweepFact{
		EventID: eid, Author: env.Principal, ObjectID: c.ObjectID,
		StartedAt: c.StartedAt, EndedAt: c.EndedAt, DistanceM: c.DistanceM,
		Result: c.Result, BBoxMin: c.BBoxMin, BBoxMax: c.BBoxMax,
		TrackAsset: c.TrackAsset, Fallback: c.Fallback,
		CreatedAt: env.CreatedAt, Clock: env.LogicalClock,
	})
}

// insertSweep keeps the ascending (CreatedAt, Clock, EventID) total
// order with EventID dedupe and oldest-first eviction — the marker
// insertion law, fourth verse.
func (s *State) insertSweep(list []SweepFact, f SweepFact) []SweepFact {
	for _, e := range list {
		if e.EventID == f.EventID {
			return list // replayed duplicate: idempotent
		}
	}
	pos := sort.Search(len(list), func(i int) bool { return sweepBefore(f, list[i]) })
	list = append(list, SweepFact{})
	copy(list[pos+1:], list[pos:])
	list[pos] = f
	if len(list) > maxSweepsPerSpace {
		list = append(list[:0], list[1:]...) // evict the OLDEST
		s.SweepEvicted++
	}
	return list
}

func sweepBefore(a, b SweepFact) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	if a.Clock != b.Clock {
		return a.Clock < b.Clock
	}
	return string(a.EventID[:]) < string(b.EventID[:])
}

// Sweeps returns every folded completion, oldest first.
func (s *State) Sweeps() []SweepFact { return s.sweeps }

// SweepsForObject DERIVES the object→sweep relation from the fact's own
// ObjectID — the event is the single truth, the object embeds nothing.
// "Sweeps in this sector" is a further hop via the object's Parent, and
// deliberately stays a caller-side walk.
func (s *State) SweepsForObject(objectID [16]byte) []SweepFact {
	var out []SweepFact
	for _, f := range s.sweeps {
		if f.ObjectID == objectID {
			out = append(out, f)
		}
	}
	return out
}
