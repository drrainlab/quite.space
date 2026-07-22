// Package reducers builds materialized state from applied events (plan
// §16.3): deterministic, side-effect free, reproducible from the log. Views
// are ordered by (logical_clock, event_id), so any two nodes holding the
// same events materialize identical state regardless of arrival order.
package reducers

import (
	"crypto/sha256"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// Message is one chat entry in the materialized view.
type Message struct {
	ID      id.EventID
	Author  id.PrincipalID
	Text    string
	ReplyTo *id.EventID
	Clock   uint64
	// Authorship travels with the message so no view can launder AI output
	// as human (plan §10.2).
	ProducedBy signal.Authorship
	Revised    bool
}

// Card is one object-block entry (plan §6.3 of the vision MVP).
type Card struct {
	ID       id.EventID // the card.created event
	Title    string
	Status   string
	Assignee *id.PrincipalID
	Origin   *id.EventID
	Clock    uint64 // clock of the latest applied update
}

// Observation is the latest telemetry value per sensor terminal.
type Observation struct {
	Value      schemas.Observation
	Author     id.PrincipalID
	ObservedAt uint64
	Clock      uint64
}

type messageRec struct {
	msg      Message
	tomb     bool
	revision *revisionRec // latest accepted revision
}

type revisionRec struct {
	text  string
	clock uint64
	eid   id.EventID
}

// State is the materialized state of one terminal.
type State struct {
	messages map[id.EventID]*messageRec
	cards    map[id.EventID]*Card
	// tombstones seen before their target (arrival order independence)
	orphanTombs map[id.EventID]struct{}
	observation *Observation

	// Unsupported schemas are counted, never dropped silently (ADR-009).
	Unsupported map[string]int
}

// NewState creates empty materialized state.
func NewState() *State {
	return &State{
		messages:    map[id.EventID]*messageRec{},
		cards:       map[id.EventID]*Card{},
		orphanTombs: map[id.EventID]struct{}{},
		Unsupported: map[string]int{},
	}
}

// later reports whether (clockA, idA) supersedes (clockB, idB) in the total
// order: Lamport clock first, event id bytes as the tiebreaker (ADR-004).
func later(clockA uint64, idA id.EventID, clockB uint64, idB id.EventID) bool {
	if clockA != clockB {
		return clockA > clockB
	}
	return string(idA[:]) > string(idB[:])
}

// Apply folds one applied event into the state. Unknown schemas are counted
// as unsupported; malformed payloads of known schemas are counted and
// skipped (the log already accepted the signature; reduction stays honest
// about what it could not interpret).
func (s *State) Apply(env *signal.Envelope, eid id.EventID) {
	switch env.Schema {
	case schemas.MessageText:
		m, err := schemas.DecodeTextMessage(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		rec, exists := s.messages[eid]
		if !exists {
			rec = &messageRec{}
			s.messages[eid] = rec
		}
		rec.msg = Message{
			ID: eid, Author: env.Principal, Text: m.Text, ReplyTo: m.ReplyTo,
			Clock: env.LogicalClock, ProducedBy: env.ProducedBy,
		}
		if _, t := s.orphanTombs[eid]; t {
			rec.tomb = true
			delete(s.orphanTombs, eid)
		}
	case schemas.MessageRevised:
		m, err := schemas.DecodeTextMessage(env.Payload)
		if err != nil || m.ReplyTo == nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		target := *m.ReplyTo
		rec, exists := s.messages[target]
		if !exists {
			rec = &messageRec{}
			s.messages[target] = rec
		}
		if rec.revision == nil || later(env.LogicalClock, eid, rec.revision.clock, rec.revision.eid) {
			rec.revision = &revisionRec{text: m.Text, clock: env.LogicalClock, eid: eid}
		}
	case schemas.MessageTombstoned:
		tb, err := schemas.DecodeTombstone(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		if rec, ok := s.messages[tb.Target]; ok {
			rec.tomb = true
		} else {
			s.orphanTombs[tb.Target] = struct{}{}
		}
	case schemas.CardCreated:
		c, err := schemas.DecodeCard(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		if existing, ok := s.cards[eid]; ok && later(existing.Clock, existing.ID, env.LogicalClock, eid) {
			return
		}
		s.cards[eid] = &Card{ID: eid, Title: c.Title, Status: c.Status,
			Assignee: c.Assignee, Origin: c.Origin, Clock: env.LogicalClock}
	case schemas.CardUpdated:
		c, err := schemas.DecodeCard(env.Payload)
		if err != nil || c.Card == nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		existing, ok := s.cards[*c.Card]
		if !ok {
			// Update before create: materialize a placeholder that the
			// create will fill; last-writer-wins on clock.
			s.cards[*c.Card] = &Card{ID: *c.Card, Title: c.Title,
				Status: c.Status, Assignee: c.Assignee, Clock: env.LogicalClock}
			return
		}
		if later(env.LogicalClock, eid, existing.Clock, existing.ID) {
			existing.Title = c.Title
			existing.Status = c.Status
			existing.Assignee = c.Assignee
			existing.Clock = env.LogicalClock
		}
	case schemas.ObservationTemp:
		o, err := schemas.DecodeObservation(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		if s.observation == nil || later(env.LogicalClock, eid, s.observation.Clock, id.EventID{}) {
			s.observation = &Observation{Value: *o, Author: env.Principal,
				ObservedAt: o.ObservedAt, Clock: env.LogicalClock}
		}
	default:
		s.Unsupported[env.Schema]++
	}
}

// Messages returns live (non-tombstoned) messages in total order.
func (s *State) Messages() []Message {
	out := make([]Message, 0, len(s.messages))
	for eid, rec := range s.messages {
		if rec.tomb || rec.msg.Text == "" {
			continue // tombstoned, or revision/tombstone arrived before create
		}
		m := rec.msg
		if rec.revision != nil {
			m.Text = rec.revision.text
			m.Revised = true
		}
		_ = eid
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return !later(out[i].Clock, out[i].ID, out[j].Clock, out[j].ID)
	})
	return out
}

// Cards returns cards in total order.
func (s *State) Cards() []Card {
	out := make([]Card, 0, len(s.cards))
	for _, c := range s.cards {
		if c.Title == "" {
			continue
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		return !later(out[i].Clock, out[i].ID, out[j].Clock, out[j].ID)
	})
	return out
}

// LatestObservation returns the newest telemetry value, if any.
func (s *State) LatestObservation() (Observation, bool) {
	if s.observation == nil {
		return Observation{}, false
	}
	return *s.observation, true
}

// Digest hashes the materialized views. Two nodes holding the same events
// must produce identical digests (M0.6 acceptance).
func (s *State) Digest() [32]byte {
	h := sha256.New()
	for _, m := range s.Messages() {
		h.Write(m.ID[:])
		h.Write([]byte(m.Text))
		h.Write([]byte{byte(m.ProducedBy)})
		if m.Revised {
			h.Write([]byte{1})
		}
	}
	for _, c := range s.Cards() {
		h.Write(c.ID[:])
		h.Write([]byte(c.Title))
		h.Write([]byte(c.Status))
	}
	if o, ok := s.LatestObservation(); ok {
		h.Write([]byte{byte(o.Value.CentiValue)})
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}
