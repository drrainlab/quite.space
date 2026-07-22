// Package reducers builds materialized state from applied events (plan
// §16.3): deterministic, side-effect free, reproducible from the log. Views
// are ordered by (logical_clock, event_id), so any two nodes holding the
// same events materialize identical state regardless of arrival order.
//
// Media wave: the feed is a list of typed Entries (text and block events),
// reactions are state-based LWW per (target, principal, emoji), and unknown
// block types stay visible through their universal fallback — an event is
// never dropped for being from the future.
package reducers

import (
	"crypto/sha256"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/appdef"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// EntryKind names the materialized entry type.
type EntryKind string

const (
	KindText    EntryKind = "text"
	KindVisual  EntryKind = "visual"
	KindVoice   EntryKind = "voice"
	KindAudio   EntryKind = "audio"
	KindFile    EntryKind = "file"
	KindLink    EntryKind = "link"
	KindSignal  EntryKind = "live_signal"
	KindUnknown EntryKind = "unknown"
)

// EntryContent is a tagged union: exactly one pointer is non-nil, matching
// Kind (no giant flat struct of nullable fields).
type EntryContent struct {
	Text    *TextContent
	Visual  *schemas.VisualBlock
	Voice   *schemas.VoiceBlock
	Audio   *schemas.AudioBlock
	File    *schemas.FileBlock
	Link    *schemas.LinkBlock
	Signal  *schemas.LiveSignalBlock
	Unknown *UnknownContent
}

// TextContent is a chat text entry (message.text.v1).
type TextContent struct {
	Text    string
	ReplyTo *id.EventID
	Revised bool
}

// UnknownContent keeps a future block visible and honest.
type UnknownContent struct {
	Schema   string
	Fallback string
}

type reactionState struct {
	active bool
	clock  uint64
	event  id.EventID
}

type reactionKey struct {
	principal id.PrincipalID
	emoji     string
}

// Entry is one materialized feed item.
type Entry struct {
	ID         id.EventID
	Author     id.PrincipalID
	Clock      uint64
	ProducedBy signal.Authorship
	Kind       EntryKind
	Content    EntryContent
	// Reactions: emoji → principals with active reactions (deterministic,
	// no "mine" here — that is a per-viewer projection, never reducer
	// state, and never part of the digest).
	Reactions map[string][]id.PrincipalID
}

type entryRec struct {
	entry     Entry
	tomb      bool
	revision  *revisionRec
	reactions map[reactionKey]reactionState
}

type revisionRec struct {
	text  string
	clock uint64
	eid   id.EventID
}

type pendingReaction struct {
	author id.PrincipalID
	emoji  string
	active bool
	clock  uint64
	eid    id.EventID
}

// State is the materialized state of one terminal.
type State struct {
	entries     map[id.EventID]*entryRec
	orphanTombs map[id.EventID]struct{}
	// pendingReactions arrived before their target entry (order tolerance).
	pendingReactions map[id.EventID][]pendingReaction
	cards            map[id.EventID]*Card
	observation      *Observation

	// publications: per-document projection (ADR-014, publications.go).
	publications map[[16]byte]*pubRec
	// pubTargets: stable reaction target → document id.
	pubTargets map[id.EventID][16]byte

	// apps (ADR-014, apps.go): definitions by revision event, instances by
	// id, and per-instance state partitions.
	appDefs      map[id.EventID]*AppDefinitionRec
	appInstances map[[16]byte]*AppInstanceRec
	appEvents    map[[16]byte][]AppStateEvent

	// Unsupported schemas are counted, never dropped silently (ADR-009).
	Unsupported map[string]int
}

// Card is one object-block entry (vision §6.3).
type Card struct {
	ID       id.EventID
	Title    string
	Status   string
	Assignee *id.PrincipalID
	Origin   *id.EventID
	Clock    uint64
}

// Observation is the latest telemetry value per space.
type Observation struct {
	Value      schemas.Observation
	Author     id.PrincipalID
	ObservedAt uint64
	Clock      uint64
}

// NewState creates empty materialized state.
func NewState() *State {
	return &State{
		entries:          map[id.EventID]*entryRec{},
		orphanTombs:      map[id.EventID]struct{}{},
		pendingReactions: map[id.EventID][]pendingReaction{},
		cards:            map[id.EventID]*Card{},
		Unsupported:      map[string]int{},
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

func (s *State) entryRecFor(eid id.EventID) *entryRec {
	rec, ok := s.entries[eid]
	if !ok {
		rec = &entryRec{reactions: map[reactionKey]reactionState{}}
		s.entries[eid] = rec
	}
	return rec
}

func (s *State) installEntry(eid id.EventID, env *signal.Envelope, kind EntryKind, content EntryContent) {
	rec := s.entryRecFor(eid)
	rec.entry = Entry{
		ID: eid, Author: env.Principal, Clock: env.LogicalClock,
		ProducedBy: env.ProducedBy, Kind: kind, Content: content,
	}
	if _, t := s.orphanTombs[eid]; t {
		rec.tomb = true
		delete(s.orphanTombs, eid)
	}
	// Drain reactions that arrived before their target.
	for _, p := range s.pendingReactions[eid] {
		s.applyReactionState(rec, p.author, p.emoji, p.active, p.clock, p.eid)
	}
	delete(s.pendingReactions, eid)
}

// applyReactionState is the LWW merge: the winner per (principal, emoji) is
// the event latest in (clock, event id) order; its `active` field IS the
// state (never a toggle — two devices both saying active=true converge to
// one visible reaction).
func (s *State) applyReactionState(rec *entryRec, author id.PrincipalID, emoji string,
	active bool, clock uint64, eid id.EventID) {

	key := reactionKey{principal: author, emoji: emoji}
	cur, exists := rec.reactions[key]
	if exists && !later(clock, eid, cur.clock, cur.event) {
		return
	}
	rec.reactions[key] = reactionState{active: active, clock: clock, event: eid}
}

// Apply folds one applied event into the state.
func (s *State) Apply(env *signal.Envelope, eid id.EventID) {
	switch env.Schema {
	case schemas.MessageText:
		m, err := schemas.DecodeTextMessage(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		s.installEntry(eid, env, KindText, EntryContent{Text: &TextContent{Text: m.Text, ReplyTo: m.ReplyTo}})
	case schemas.MessageRevised:
		m, err := schemas.DecodeTextMessage(env.Payload)
		if err != nil || m.ReplyTo == nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		rec := s.entryRecFor(*m.ReplyTo)
		if rec.revision == nil || later(env.LogicalClock, eid, rec.revision.clock, rec.revision.eid) {
			rec.revision = &revisionRec{text: m.Text, clock: env.LogicalClock, eid: eid}
		}
	case schemas.MessageTombstoned:
		tb, err := schemas.DecodeTombstone(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		if rec, ok := s.entries[tb.Target]; ok {
			rec.tomb = true
		} else {
			s.orphanTombs[tb.Target] = struct{}{}
		}
	case schemas.BlockVisual:
		if b, err := schemas.DecodeVisualBlock(env.Payload); err == nil {
			s.installEntry(eid, env, KindVisual, EntryContent{Visual: b})
		} else {
			s.installUnknown(eid, env)
		}
	case schemas.BlockVoice:
		if b, err := schemas.DecodeVoiceBlock(env.Payload); err == nil {
			s.installEntry(eid, env, KindVoice, EntryContent{Voice: b})
		} else {
			s.installUnknown(eid, env)
		}
	case schemas.BlockAudio:
		if b, err := schemas.DecodeAudioBlock(env.Payload); err == nil {
			s.installEntry(eid, env, KindAudio, EntryContent{Audio: b})
		} else {
			s.installUnknown(eid, env)
		}
	case schemas.BlockFile:
		if b, err := schemas.DecodeFileBlock(env.Payload); err == nil {
			s.installEntry(eid, env, KindFile, EntryContent{File: b})
		} else {
			s.installUnknown(eid, env)
		}
	case schemas.BlockLink:
		if b, err := schemas.DecodeLinkBlock(env.Payload); err == nil {
			s.installEntry(eid, env, KindLink, EntryContent{Link: b})
		} else {
			s.installUnknown(eid, env)
		}
	case schemas.BlockLiveSignal:
		if b, err := schemas.DecodeLiveSignalBlock(env.Payload); err == nil {
			s.installEntry(eid, env, KindSignal, EntryContent{Signal: b})
		} else {
			s.installUnknown(eid, env)
		}
	case schemas.BlockReaction:
		rb, err := schemas.DecodeReactionBlock(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		// A reaction target is either a feed entry's event id or a STABLE
		// publication target (ADR-014 invariant 7: never a revision event id).
		if docID, ok := s.pubTargets[rb.Target]; ok {
			s.applyPubReaction(docID, env.Principal, rb.Emoji, rb.Active, env.LogicalClock, eid)
		} else if rec, ok := s.entries[rb.Target]; ok && rec.entry.Kind != "" {
			s.applyReactionState(rec, env.Principal, rb.Emoji, rb.Active, env.LogicalClock, eid)
		} else {
			s.pendingReactions[rb.Target] = append(s.pendingReactions[rb.Target], pendingReaction{
				author: env.Principal, emoji: rb.Emoji, active: rb.Active,
				clock: env.LogicalClock, eid: eid,
			})
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
	case publication.SchemaPublished, publication.SchemaRevised:
		s.applyPublicationRevision(env, eid)
	case publication.SchemaArchived:
		s.applyPublicationLifecycle(env, eid, true)
	case publication.SchemaRestored:
		s.applyPublicationLifecycle(env, eid, false)
	case publication.SchemaComment:
		s.applyPublicationComment(env, eid)
	case appdef.SchemaDefinition:
		s.applyAppDefinition(env, eid)
	case appdef.SchemaInstance:
		s.applyAppInstance(env, eid)
	case appdef.SchemaPollVote, appdef.SchemaFormResponse:
		s.applyAppState(env, eid)
	case schemas.BlockAttached:
		// An asset carrier: indexed via the OnBlock hook, never a feed entry.
		return
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
		if schemas.IsBlockSchema(env.Schema) {
			// A block type from the future: keep it in the feed via the
			// universal fallback — never dropped, never invisible.
			s.installUnknown(eid, env)
			return
		}
		s.Unsupported[env.Schema]++
	}
}

func (s *State) installUnknown(eid id.EventID, env *signal.Envelope) {
	fb, err := schemas.DecodeBlockFallback(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	s.installEntry(eid, env, KindUnknown, EntryContent{
		Unknown: &UnknownContent{Schema: env.Schema, Fallback: fb},
	})
}

// Entries returns the live feed in total order, with reactions projected
// deterministically (sorted principals per emoji).
func (s *State) Entries() []Entry {
	out := make([]Entry, 0, len(s.entries))
	for _, rec := range s.entries {
		if rec.tomb || rec.entry.Kind == "" {
			continue
		}
		e := rec.entry
		if e.Kind == KindText && rec.revision != nil {
			t := *e.Content.Text
			t.Text = rec.revision.text
			t.Revised = true
			e.Content.Text = &t
		}
		e.Reactions = projectReactions(rec.reactions)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return !later(out[i].Clock, out[i].ID, out[j].Clock, out[j].ID)
	})
	return out
}

func projectReactions(states map[reactionKey]reactionState) map[string][]id.PrincipalID {
	if len(states) == 0 {
		return nil
	}
	out := map[string][]id.PrincipalID{}
	for key, st := range states {
		if st.active {
			out[key.emoji] = append(out[key.emoji], key.principal)
		}
	}
	for emoji := range out {
		ps := out[emoji]
		sort.Slice(ps, func(i, j int) bool { return string(ps[i][:]) < string(ps[j][:]) })
		out[emoji] = ps
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Message is the pre-media text view (compat for older tests and the
// messages API).
type Message struct {
	ID         id.EventID
	Author     id.PrincipalID
	Text       string
	ReplyTo    *id.EventID
	Clock      uint64
	ProducedBy signal.Authorship
	Revised    bool
}

func (s *State) Messages() []Message {
	var out []Message
	for _, e := range s.Entries() {
		if e.Kind != KindText {
			continue
		}
		out = append(out, Message{
			ID: e.ID, Author: e.Author, Text: e.Content.Text.Text,
			ReplyTo: e.Content.Text.ReplyTo, Clock: e.Clock,
			ProducedBy: e.ProducedBy, Revised: e.Content.Text.Revised,
		})
	}
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
// must produce identical digests (M0.6 acceptance). Reaction state enters
// by (emoji, sorted principals) — viewer-relative data like "mine" never
// exists at this layer.
func (s *State) Digest() [32]byte {
	h := sha256.New()
	for _, e := range s.Entries() {
		h.Write(e.ID[:])
		h.Write([]byte(e.Kind))
		h.Write([]byte{byte(e.ProducedBy)})
		switch {
		case e.Content.Text != nil:
			h.Write([]byte(e.Content.Text.Text))
			if e.Content.Text.Revised {
				h.Write([]byte{1})
			}
		case e.Content.Unknown != nil:
			h.Write([]byte(e.Content.Unknown.Schema))
			h.Write([]byte(e.Content.Unknown.Fallback))
		case e.Content.Visual != nil:
			h.Write([]byte(e.Content.Visual.Alt))
			h.Write(e.Content.Visual.Original.AssetID[:])
		case e.Content.Voice != nil:
			h.Write(e.Content.Voice.Original.AssetID[:])
		case e.Content.Audio != nil:
			h.Write([]byte(e.Content.Audio.Title))
			h.Write(e.Content.Audio.Original.AssetID[:])
		case e.Content.File != nil:
			h.Write([]byte(e.Content.File.Filename))
		case e.Content.Link != nil:
			h.Write([]byte(e.Content.Link.URL))
		case e.Content.Signal != nil:
			h.Write([]byte(e.Content.Signal.Preset))
		}
		emojis := make([]string, 0, len(e.Reactions))
		for em := range e.Reactions {
			emojis = append(emojis, em)
		}
		sort.Strings(emojis)
		for _, em := range emojis {
			h.Write([]byte(em))
			for _, p := range e.Reactions[em] {
				h.Write(p[:])
			}
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
