// Field projections (SP-3, ADR-031): markers and check-ins. Positions
// deliberately live in the trust engine (the presence twin) — they are
// expiring per-terminal claims, not space state; the reducer holds what
// converges deterministically and belongs in the digest.
//
// Markers fold as a map by marker_id — an LWW register in shape (immutable
// in v1, so first-and-only; the shape is ready for a v2 move/tombstone) —
// plus a bounded ascending list under the observation eviction law: the
// survivors are exactly the newest maxMarkersPerSpace in the total order,
// in any arrival order. Check-ins fold twice from one event: the latest
// per source terminal (an LWW register), and a bounded space history —
// and the note is ALSO a quiet feed row (one event, two projections, the
// observation.noted precedent).
package reducers

import (
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

const (
	maxMarkersPerSpace  = 500
	maxCheckinsPerSpace = 200
)

// FieldMarker is one placed marker.
type FieldMarker struct {
	MarkerID  [16]byte
	EventID   id.EventID
	Author    id.PrincipalID
	Text      string
	Kind      string
	LatE7U    uint64
	LonE7U    uint64
	ObjectID  *[16]byte
	ExpiresAt uint64 // 0 = timeless; display honesty only, never custody
	CreatedAt uint64
	Clock     uint64
}

// CheckinRecord is one contact fact.
type CheckinRecord struct {
	EventID    id.EventID
	Author     id.PrincipalID
	Source     *id.TerminalID
	Text       string
	LatE7U     uint64
	LonE7U     uint64
	HasPoint   bool
	BatteryPct uint64
	HasBattery bool
	SOS        bool
	CreatedAt  uint64
	Clock      uint64
}

// CheckinContent is the feed-row projection of the same event.
type CheckinContent struct {
	Text string
	SOS  bool
	// The claim's coordinates, carried into the row so the feed can SAY
	// them (owner's ask): "I'm OK" without a place is half the sentence.
	// Display-side only — the wire already carries the point; nothing is
	// ever parsed back out of prose (ADR-031).
	LatE7U   uint64
	LonE7U   uint64
	HasPoint bool
}

func (s *State) applyMarkerPlaced(env *signal.Envelope, eid id.EventID) {
	m, err := schemas.DecodePlacedMarker(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	// Immutable v1: among events claiming one marker_id, the FIRST in
	// the total order wins on every replica regardless of arrival —
	// insertMarker enforces it inside the sorted list itself.
	fm := FieldMarker{
		MarkerID: m.MarkerID, EventID: eid, Author: env.Principal,
		Text: m.Text, Kind: m.Kind,
		LatE7U: m.Point.LatE7U, LonE7U: m.Point.LonE7U,
		ObjectID: m.ObjectID, ExpiresAt: m.ExpiresAt,
		CreatedAt: env.CreatedAt, Clock: env.LogicalClock,
	}
	s.markers = s.insertMarker(s.markers, fm)
}

// insertMarker keeps the marker list sorted ascending by (CreatedAt,
// Clock, EventID) and bounded — the observation eviction law verbatim,
// with its own honest counter. marker_id uniqueness is enforced INSIDE
// the total order: among events claiming the same id, the FIRST in the
// total order wins on every replica regardless of arrival.
func (s *State) insertMarker(list []FieldMarker, m FieldMarker) []FieldMarker {
	for _, e := range list {
		if e.EventID == m.EventID {
			return list // replayed duplicate: idempotent
		}
	}
	pos := sort.Search(len(list), func(i int) bool { return markerBefore(m, list[i]) })
	// A later event claiming an existing marker_id loses to the earlier
	// one; an EARLIER event arriving late evicts the later claimant —
	// both directions converge on "first in total order wins".
	for i, e := range list {
		if e.MarkerID == m.MarkerID {
			if i < pos {
				return list // existing claim is earlier — it stands
			}
			// existing sorts at/after the newcomer → the newcomer is
			// earlier in the total order and takes the id; removal is at
			// i >= pos, so pos needs no shift.
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	list = append(list, FieldMarker{})
	copy(list[pos+1:], list[pos:])
	list[pos] = m
	if len(list) > maxMarkersPerSpace {
		list = append(list[:0], list[1:]...) // evict the OLDEST
		s.MarkerEvicted++
	}
	return list
}

func markerBefore(a, b FieldMarker) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	if a.Clock != b.Clock {
		return a.Clock < b.Clock
	}
	return string(a.EventID[:]) < string(b.EventID[:])
}

// Markers lists the space's markers, ascending.
func (s *State) Markers() []FieldMarker {
	return append([]FieldMarker(nil), s.markers...)
}

func (s *State) applyCheckin(env *signal.Envelope, eid id.EventID) {
	c, err := schemas.DecodeCheckin(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := CheckinRecord{
		EventID: eid, Author: env.Principal, Source: env.SourceTerminal,
		Text: c.Text, SOS: c.SOS,
		BatteryPct: c.BatteryPct, HasBattery: c.HasBattery,
		CreatedAt: env.CreatedAt, Clock: env.LogicalClock,
	}
	if c.Point != nil {
		rec.LatE7U, rec.LonE7U, rec.HasPoint = c.Point.LatE7U, c.Point.LonE7U, true
	}
	// Projection one: the quiet feed row — a check-in is a sentence the
	// room should see, especially when it screams.
	s.installEntry(eid, env, KindCheckin, EntryContent{
		Checkin: &CheckinContent{Text: c.Text, SOS: c.SOS,
			LatE7U: rec.LatE7U, LonE7U: rec.LonE7U, HasPoint: rec.HasPoint},
	})
	// Projection two: latest per author principal, an LWW register by
	// (CreatedAt, Clock, EventID). Keyed by PRINCIPAL, not terminal: the
	// question the overdue arithmetic answers is "when did KATYA last
	// check in", and a person may carry two devices.
	if s.checkinLatest == nil {
		s.checkinLatest = map[id.PrincipalID]*CheckinRecord{}
	}
	cur := s.checkinLatest[env.Principal]
	if cur == nil || checkinBefore(*cur, rec) {
		r := rec
		s.checkinLatest[env.Principal] = &r
	}
	// Projection three: the bounded space history, observation law.
	s.checkins = s.insertCheckin(s.checkins, rec)
}

func (s *State) insertCheckin(list []CheckinRecord, c CheckinRecord) []CheckinRecord {
	for _, e := range list {
		if e.EventID == c.EventID {
			return list
		}
	}
	pos := sort.Search(len(list), func(i int) bool { return checkinBefore(c, list[i]) })
	list = append(list, CheckinRecord{})
	copy(list[pos+1:], list[pos:])
	list[pos] = c
	if len(list) > maxCheckinsPerSpace {
		list = append(list[:0], list[1:]...)
		s.CheckinEvicted++
	}
	return list
}

func checkinBefore(a, b CheckinRecord) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	if a.Clock != b.Clock {
		return a.Clock < b.Clock
	}
	return string(a.EventID[:]) < string(b.EventID[:])
}

// Checkins lists the space's check-in history, ascending.
func (s *State) Checkins() []CheckinRecord {
	return append([]CheckinRecord(nil), s.checkins...)
}

// LatestCheckin returns a member's most recent contact fact.
func (s *State) LatestCheckin(p id.PrincipalID) (CheckinRecord, bool) {
	r, ok := s.checkinLatest[p]
	if !ok {
		return CheckinRecord{}, false
	}
	return *r, true
}

// LatestCheckins returns the freshest check-in per member.
func (s *State) LatestCheckins() map[id.PrincipalID]CheckinRecord {
	if len(s.checkinLatest) == 0 {
		return nil
	}
	out := make(map[id.PrincipalID]CheckinRecord, len(s.checkinLatest))
	for k, v := range s.checkinLatest {
		out[k] = *v
	}
	return out
}
