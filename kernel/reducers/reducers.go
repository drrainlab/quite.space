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
	"github.com/drrainlab/quiet_places/protocol/keep"
	"github.com/drrainlab/quiet_places/protocol/listening"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/resonance"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// EntryKind names the materialized entry type.
type EntryKind string

const (
	KindText    EntryKind = "text"
	KindVisual  EntryKind = "visual"
	KindVideo   EntryKind = "video"
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
	Video   *schemas.VideoBlock
	Voice   *schemas.VoiceBlock
	Audio   *schemas.AudioBlock
	File    *schemas.FileBlock
	Link    *schemas.LinkBlock
	Signal  *schemas.LiveSignalBlock
	Unknown *UnknownContent
}

// TextContent is a chat text entry (message.text.v1). Mentions are the
// author's SIGNED claim of who is addressed — anyone may mention anyone, but
// the claim itself is authenticated, so readers never have to guess from the
// text. ReplyTo here is a genuine reply pointer; note that
// message.revised.v1 reuses the same wire field as its revision target, so
// only MessageText entries carry a reply edge.
type TextContent struct {
	Text     string
	ReplyTo  *id.EventID
	Mentions []id.PrincipalID
	Revised  bool
	// Origin is the quotation's provenance when this message is a share
	// (SHARE-1). Every field in it is the SENDER's claim: the original was
	// sealed under another space's epoch and cannot be verified here.
	Origin *schemas.ShareOrigin
	// Card is the post card when this message forwards a publication (PS).
	// Sender-authored like Origin, and exactly as unverifiable.
	Card *schemas.SharedPublication
	// Model is the signer's claim about what produced the text, carried
	// only for machine-authored messages (AI-0). Without it, a log of
	// answers from three models is unattributable — the member card cannot
	// help, because a v0 manifest declares no model and honestly says so.
	Model string
	// External is the foreign provenance of an imported message (TR-0,
	// key 7). Sender-authored like Origin and exactly as unverifiable —
	// and renderers must show it ONLY when the envelope's authorship is
	// imported, or any member could dress its own words as somebody's
	// email.
	External *schemas.ExternalOrigin
}

// UnknownContent keeps a future block visible and honest.
type UnknownContent struct {
	Schema   string
	Fallback string
}

// Entry is one materialized feed item. Reactions live in the resonance
// projection (resonance.go), keyed by target — not on the entry.
type Entry struct {
	ID         id.EventID
	Author     id.PrincipalID
	Clock      uint64
	CreatedAt  uint64 // author wall-clock (advisory) — display order
	ProducedBy signal.Authorship
	Kind       EntryKind
	Content    EntryContent
}

type entryRec struct {
	entry    Entry
	tomb     bool
	revision *revisionRec
}

type revisionRec struct {
	text  string
	clock uint64
	eid   id.EventID
}

// State is the materialized state of one terminal.
type State struct {
	entries     map[id.EventID]*entryRec
	orphanTombs map[id.EventID]struct{}
	cards       map[id.EventID]*Card
	observation *Observation

	// resonance (RP-1, resonance.go): target → per-actor single-slot LWW
	// registers (unresolved targets stay unprojected, never evicted), plus
	// the controller-authored palette register.
	resonance  map[id.EventID]*resRec
	resPalette *resPaletteReg

	// publications: per-document projection (ADR-014, publications.go).
	publications map[[16]byte]*pubRec
	// pubTargets: stable reaction target → document id.
	pubTargets map[id.EventID][16]byte

	// apps (ADR-014, apps.go): definitions by revision event, instances by
	// id, and per-instance state partitions.
	appDefs      map[id.EventID]*AppDefinitionRec
	appInstances map[[16]byte]*AppInstanceRec
	appEvents    map[[16]byte][]AppStateEvent
	// appInstanceEvents: instance creation event id → instance id (keep
	// targets address app instances by their creation event).
	appInstanceEvents map[id.EventID][16]byte

	// keeps (LR-1, keep.go): target → per-author LWW keep registers, plus a
	// bounded FIFO of targets folded before their object was seen.
	keeps       map[id.EventID]*keepRec
	keepPending []id.EventID
	// Controller is the space controller from the manifest — static space
	// metadata (never event-derived), used only to authorize keep moderation.
	Controller *id.PrincipalID

	// Authorized is the curated-space publish filter (PA-0): nil means no
	// policy (everything materializes). When set, events from principals
	// outside the set are not materialized — defense in depth behind the
	// log admission gate, for frames that reached this replica by paths
	// that predate or bypass admission.
	Authorized map[id.PrincipalID]bool

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
		entries:     map[id.EventID]*entryRec{},
		orphanTombs: map[id.EventID]struct{}{},
		cards:       map[id.EventID]*Card{},
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

func (s *State) entryRecFor(eid id.EventID) *entryRec {
	rec, ok := s.entries[eid]
	if !ok {
		rec = &entryRec{}
		s.entries[eid] = rec
	}
	return rec
}

func (s *State) installEntry(eid id.EventID, env *signal.Envelope, kind EntryKind, content EntryContent) {
	rec := s.entryRecFor(eid)
	rec.entry = Entry{
		ID: eid, Author: env.Principal, Clock: env.LogicalClock, CreatedAt: env.CreatedAt,
		ProducedBy: env.ProducedBy, Kind: kind, Content: content,
	}
	if _, t := s.orphanTombs[eid]; t {
		rec.tomb = true
		delete(s.orphanTombs, eid)
	}
	// A keep folded before this entry now resolves against the allowlist.
	// Resonance registers need no drain: unresolved registers simply start
	// projecting once ResonanceTargetStatus resolves.
	s.resolveKeepTarget(eid)
}

// Apply folds one applied event into the state.
func (s *State) Apply(env *signal.Envelope, eid id.EventID) {
	// Curated-space publish policy (PA-0, defense in depth): the primary
	// gate is log admission; anything that slipped past it still never
	// materializes here.
	if s.Authorized != nil && !s.Authorized[env.Principal] {
		return
	}
	switch env.Schema {
	case schemas.MessageText:
		m, err := schemas.DecodeTextMessage(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		s.installEntry(eid, env, KindText, EntryContent{Text: &TextContent{
			Text: m.Text, ReplyTo: m.ReplyTo, Mentions: m.Mentions,
			Model: m.ProducedModel, Origin: m.Origin, Card: m.Card,
			// The imported-authorship gate lives at the RENDERER (it has
			// the envelope); the reducer carries what was said.
			External: m.External}})
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
	case schemas.BlockVideo:
		if b, err := schemas.DecodeVideoBlock(env.Payload); err == nil {
			s.installEntry(eid, env, KindVideo, EntryContent{Video: b})
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
		// Legacy pre-Resonance reactions (clean replacement, RP wave): the
		// explicit arm is MANDATORY — without it IsBlockSchema would route
		// these into installUnknown and they would surface as unknown feed
		// entries. Counted, never rendered.
		s.Unsupported["legacy:block.reaction.v1"]++
		return
	case resonance.SchemaSet:
		s.applyResonanceSet(env, eid)
	case resonance.SchemaClear:
		s.applyResonanceClear(env, eid)
	case resonance.SchemaPalette:
		s.applyResonancePalette(env, eid)
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
	case appdef.SchemaPollVote, appdef.SchemaFormResponse, listening.SchemaCommand:
		s.applyAppState(env, eid)
	case keep.SchemaKept:
		s.applyKept(env, eid)
	case keep.SchemaUnkept:
		s.applyUnkept(env, eid)
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

// Entries returns the live feed in total order. Reactions are a separate
// projection — ResonanceFor(entry id).
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
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return displayBefore(out[i], out[j])
	})
	return out
}

// displayBefore orders the feed for humans: wall-clock (CreatedAt) first, then
// the deterministic (LogicalClock, EventID) tiebreak. Logical clock remains
// the causal authority (ADR-004) — this only reorders equal-time concurrent
// items so a later message never renders above an earlier one just because
// the authors' own log lengths differ. Every replica still agrees exactly.
func displayBefore(a, b Entry) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	if a.Clock != b.Clock {
		return a.Clock < b.Clock
	}
	return string(a.ID[:]) < string(b.ID[:])
}

// EntryByID projects one live entry (Shelf enrichment). Tombstoned entries
// return false — the Shelf renders those as placeholders, not content.
func (s *State) EntryByID(eid id.EventID) (Entry, bool) {
	rec, ok := s.entries[eid]
	if !ok || rec.tomb || rec.entry.Kind == "" {
		return Entry{}, false
	}
	e := rec.entry
	if e.Kind == KindText && rec.revision != nil {
		t := *e.Content.Text
		t.Text = rec.revision.text
		t.Revised = true
		e.Content.Text = &t
	}
	return e, true
}

// Message is the pre-media text view (compat for older tests and the
// messages API).
type Message struct {
	ID         id.EventID
	Author     id.PrincipalID
	Text       string
	ReplyTo    *id.EventID
	Mentions   []id.PrincipalID
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
			ReplyTo: e.Content.Text.ReplyTo, Mentions: e.Content.Text.Mentions,
			Clock: e.Clock, ProducedBy: e.ProducedBy, Revised: e.Content.Text.Revised,
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
// must produce identical digests (M0.6 acceptance). Resonance state stays
// out (precedent: keep/pubs/apps) — ResonanceDigest() is the test oracle
// for its order-independence.
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
