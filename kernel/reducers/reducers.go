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
	"github.com/drrainlab/quiet_places/protocol/objects"
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
	// KindObservation is a human observation (SP-1): a quiet feed row that
	// is simultaneously a timeline note on its object (objects.go).
	KindObservation EntryKind = "observation"
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
	Signal      *schemas.LiveSignalBlock
	Observation *ObservationNoteContent
	Unknown     *UnknownContent
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
	// ObjectRefs: the author's signed claim of which domain objects this
	// message is about (SP-2.1) — carried like Mentions, resolved to
	// names at the API layer.
	ObjectRefs [][16]byte
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
	// valueObs is the instrument plane's materialized view (QI-1): one
	// LWW slot per (instrument, channel). Latest-value state, never feed
	// entries — the reducer twin of the presence map.
	valueObs map[ValueObsKey]*ValueObservation

	// resonance (RP-1, resonance.go): target → per-actor single-slot LWW
	// registers (unresolved targets stay unprojected, never evicted), plus
	// the controller-authored palette register.
	resonance  map[id.EventID]*resRec
	resPalette *resPaletteReg

	// publications: per-document projection (ADR-014, publications.go).
	publications map[[16]byte]*pubRec
	// pubTargets: stable reaction target → document id.
	pubTargets map[id.EventID][16]byte

	// objects (SP-1, objects.go): per-object projection + stable targets +
	// the space journal of object-less observations.
	objects    map[[16]byte]*objRec
	objTargets map[id.EventID][16]byte
	journalObs []ObservationNote
	// ObservationEvicted counts timeline notes deliberately aged out of
	// bounded timelines. A separate counter, NOT Unsupported — these were
	// understood, then evicted by the projection's own law.
	ObservationEvicted int

	// annots (SP-2, edges.go): per-asset bounded annotation timelines,
	// same eviction law as observations; AnnotationEvicted is its honest
	// counter.
	annots            map[string][]AnnotationNote
	AnnotationEvicted int

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

// Card is one task card (vision §6.3, SP-1). Status is domain display
// state — carried and shown, never interpreted by the kernel.
type Card struct {
	ID       id.EventID
	Title    string
	Status   string
	Assignee *id.PrincipalID
	Origin   *id.EventID
	ObjectID *[16]byte // the domain object this task belongs to (SP-1)
	Clock    uint64
}

// Observation is the latest telemetry value per space.
type Observation struct {
	Value      schemas.Observation
	Author     id.PrincipalID
	ObservedAt uint64
	Clock      uint64
}

// ValueObsKey names one instrument channel. The instrument id is the
// SourceTerminal of the emitting participant — the stable identity the
// owner's plan insists on; never a device key, never called a terminal
// in any new API.
type ValueObsKey struct {
	Instrument id.TerminalID
	Channel    string
}

// ValueObservation is the latest reading of one channel.
type ValueObservation struct {
	Value      schemas.ValueObservation
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
			External: m.External, ObjectRefs: m.ObjectRefs}})
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
			Assignee: c.Assignee, Origin: c.Origin, ObjectID: c.ObjectID, Clock: env.LogicalClock}
	case schemas.CardUpdated:
		c, err := schemas.DecodeCard(env.Payload)
		if err != nil || c.Card == nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		existing, ok := s.cards[*c.Card]
		if !ok {
			s.cards[*c.Card] = &Card{ID: *c.Card, Title: c.Title,
				Status: c.Status, Assignee: c.Assignee, ObjectID: c.ObjectID, Clock: env.LogicalClock}
			return
		}
		if later(env.LogicalClock, eid, existing.Clock, existing.ID) {
			existing.Title = c.Title
			existing.Status = c.Status
			existing.Assignee = c.Assignee
			existing.ObjectID = c.ObjectID
			existing.Clock = env.LogicalClock
		}
	case publication.SchemaPublished, publication.SchemaRevised:
		s.applyPublicationRevision(env, eid)
	case publication.SchemaArchived:
		s.applyPublicationLifecycle(env, eid, true)
	case publication.SchemaRestored:
		s.applyPublicationLifecycle(env, eid, false)
	case objects.SchemaCreated, objects.SchemaRevised:
		s.applyObjectRevision(env, eid)
	case objects.SchemaArchived:
		s.applyObjectLifecycle(env, eid, true)
	case objects.SchemaRestored:
		s.applyObjectLifecycle(env, eid, false)
	case objects.SchemaAttached:
		s.applyObjectAttached(env, eid)
	case schemas.ObservationNoted:
		s.applyObservationNoted(env, eid)
	case schemas.AssetAnnotated:
		s.applyAssetAnnotated(env, eid)
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
	case schemas.ObservationValue:
		vo, err := schemas.DecodeValueObservation(env.Payload)
		if err != nil {
			s.Unsupported["malformed:"+env.Schema]++
			return
		}
		if env.SourceTerminal == nil {
			return // a reading with no instrument behind it names nothing
		}
		key := ValueObsKey{Instrument: *env.SourceTerminal, Channel: vo.Channel}
		if s.valueObs == nil {
			s.valueObs = map[ValueObsKey]*ValueObservation{}
		}
		cur := s.valueObs[key]
		if cur == nil || later(env.LogicalClock, eid, cur.Clock, id.EventID{}) {
			s.valueObs[key] = &ValueObservation{Value: *vo, Author: env.Principal,
				ObservedAt: vo.ObservedAt, Clock: env.LogicalClock}
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
	// External is foreign provenance (TR-0). Carried here so a renderer
	// can name the sender — and gated on imported authorship at the point
	// of display, because the payload can lie and the envelope cannot.
	External *schemas.ExternalOrigin
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
			External: e.Content.Text.External,
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

// ValueObservations returns the instrument plane's latest readings,
// keyed by (instrument, channel). The map is a copy: state stays private.
func (s *State) ValueObservations() map[ValueObsKey]ValueObservation {
	if len(s.valueObs) == 0 {
		return nil
	}
	out := make(map[ValueObsKey]ValueObservation, len(s.valueObs))
	for k, v := range s.valueObs {
		out[k] = *v
	}
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
		case e.Content.Observation != nil:
			h.Write([]byte(e.Content.Observation.Text))
			if e.Content.Observation.ObjectID != nil {
				h.Write(e.Content.Observation.ObjectID[:])
			}
		}
	}
	for _, c := range s.Cards() {
		h.Write(c.ID[:])
		h.Write([]byte(c.Title))
		h.Write([]byte(c.Status))
		if c.ObjectID != nil {
			h.Write(c.ObjectID[:])
		}
	}
	// Objects (SP-1): record + lifecycle + bounded timelines, in sorted
	// object-id order. Empty state writes nothing, so pre-SP-1 fixed
	// digests stand.
	for _, o := range s.digestObjects() {
		h.Write(o.ObjectID[:])
		h.Write([]byte(o.Name))
		h.Write(o.RevisionEventID[:])
		if o.Archived {
			h.Write([]byte{1})
		}
		for _, n := range o.Observations {
			h.Write(n.EventID[:])
		}
		// Asset edges (SP-2): pure LWW registers, deterministic in any
		// arrival order, so they belong in the digest. Sorted by asset
		// hex; the winner event id covers every field of the register.
		for _, e := range s.digestEdges(o.ObjectID) {
			h.Write([]byte(e.Asset))
			h.Write(e.EventID[:])
			if e.Detached {
				h.Write([]byte{1})
			}
			h.Write([]byte(e.Supersedes))
			h.Write([]byte(e.Role))
		}
		if rec := s.objects[o.ObjectID]; rec != nil && rec.cand != nil {
			h.Write([]byte(rec.cand.asset))
			h.Write(rec.cand.eid[:])
		}
	}
	for _, n := range s.journalObs {
		h.Write(n.EventID[:])
	}
	// Annotations (SP-2): bounded timelines under the deterministic
	// observation eviction law — digest-safe. Sorted by asset hex;
	// empty state writes nothing, so pre-SP-2 digests stand.
	if len(s.annots) > 0 {
		assets := make([]string, 0, len(s.annots))
		for a := range s.annots {
			assets = append(assets, a)
		}
		sort.Strings(assets)
		for _, a := range assets {
			h.Write([]byte(a))
			for _, n := range s.annots[a] {
				h.Write(n.EventID[:])
			}
		}
	}
	if o, ok := s.LatestObservation(); ok {
		h.Write([]byte{byte(o.Value.CentiValue)})
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}
