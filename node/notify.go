// AR-1b.1 + AR-1b.5.1/5.2 — the notification candidate contract.
//
// A candidate is produced BELOW the interface, where an event has already been
// decrypted, signature-checked and applied to the log. Nothing above this line
// re-parses the journal or interprets the protocol: the host renders what it
// is handed and nothing else.
//
// THE CENTRAL INVARIANT, and it is structural rather than a filter:
//
//	Opening an existing journal publishes NOT ONE historical notification,
//	and the next new event publishes exactly one.
//
// The dangerous seam is Space.OnAbsorb, which fires for EVERY absorbed event —
// local emits, synced frames, AND every event replayed while the log is
// attached at open. A plane wired there naively turns a 16 000-event journal
// into 16 000 "new messages" on first run.
//
// So the sink is ATTACHED, and attaching is a separate act from wiring. Until
// a host attaches, absorbing produces nothing at all. `node.Open` finishes its
// replays before anybody can attach, which makes history unable to notify by
// construction — there is no set of ids to get right and no frontier to forget
// to persist.
//
// ATTACHING IS ONE OPERATION, NOT TWO, and that is AR-1b.5.2. The obvious
// shape — ask for the current cursor, remember it as a baseline, then
// subscribe — has a window between the reading and the subscription. An event
// applied inside that window lands after the baseline and before the sink
// exists: it is neither announced nor recoverable, and nothing anywhere
// reports it missing. So the cursor is read and the sink installed under one
// lock, and the baseline a host is handed is true by construction.
//
// UNITS LIVE IN FIELD NAMES HERE. `created_at` cost a live run once already:
// the protocol counts seconds, Android counts milliseconds, and the shade
// rendered every notification as "56y" ago. A number crossing a language
// boundary carries its unit in its name or it eventually carries the wrong
// one.
package node

import (
	"sync"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
)

// maxPreviewRunes bounds what leaves the core as a preview. A notification
// shows a line, and a host that receives a whole post has been handed more of
// a person's content than it can ever display.
const maxPreviewRunes = 120

// NotificationCandidate is the narrow fact a host may render. It carries no
// frames, no keys and no protocol: everything here has already been verified.
type NotificationCandidate struct {
	EventID id.EventID
	SpaceID id.TerminalID
	Device  id.DeviceID
	Schema  string

	// SourceSequence is this event's place in its author's chain — the same
	// number the watermark counts. A host needs it to compact its own
	// tombstones: "is this event behind the oldest position the core may
	// replay" is a question about sequences, and an event id cannot answer it.
	SourceSequence uint64

	// OccurredAtUnixMs is the AUTHOR's clock, in milliseconds, unverified.
	// For ordering what a person is shown — never for deciding what is new:
	// that is what PresentationCursor is for, and a device with a skewed
	// clock must not be able to silence or resurrect notifications on
	// somebody else's phone.
	OccurredAtUnixMs uint64

	// PresentationCursor is assigned by the core AFTER verify and apply, and
	// is monotonic in this runtime's stream of applied events. It is not a
	// protocol frontier and carries no causal meaning: two nodes' cursors are
	// not comparable, and neither are two runs of the same node. Its one job
	// is to let a host say "everything up to here has been dealt with"
	// without inventing an order of its own.
	//
	// SCOPED TO ONE RUNTIME. Nothing here survives Close: a reopened node
	// starts again at zero, and by then every prior event is history that has
	// already been replayed past a detached sink. A host that stores a cursor
	// must store the runtime epoch beside it and treat a change of epoch as
	// "there is nothing to resume".
	PresentationCursor uint64

	// AuthoredLocally marks our own event. A person is not told about the
	// thing they just did, and the host does not have to work out who they
	// are to know that.
	AuthoredLocally bool

	// The presentation snapshot: what a host may SHOW, as opposed to what it
	// may act on. Every field is optional and an empty one is ordinary — a
	// space with no title has none, a manifest may not have arrived, a schema
	// may have no text. None of the machinery above depends on them, so a
	// missing label can never cost a notification or break a dedup.
	//
	// They are resolved from state the core already holds in memory. No
	// journal is re-read, no request leaves the process, and nothing here is
	// an identity: a label is what a name CLAIMED to be at the moment the
	// event was applied.
	SpaceLabel  string
	SenderLabel string
	PreviewText string
}

// notifySink is the runtime's attached notification plane.
//
// Its own lock, never r.mu. The lock covers both the sink and the cursor,
// which is what makes attaching atomic with respect to emission: while a host
// is being handed its baseline, no event can be applied past it.
type notifySink struct {
	mu     sync.Mutex
	fn     func(NotificationCandidate)
	cursor uint64
	// forgotten is told when a space is deleted from this device (SD-0).
	//
	// SEPARATE FROM THE CANDIDATE PATH BUT SENT THROUGH THE SAME HOST QUEUE,
	// so it cannot overtake a candidate for the space it is retiring. A host
	// that cancelled a notification and then received a queued candidate for
	// the same space would put the conversation straight back on a screen
	// somebody had just cleared it from.
	forgotten func(id.TerminalID)
}

// attach installs the sink and returns the cursor as it stood at that instant.
// One critical section, deliberately: see the package comment.
func (s *notifySink) attach(fn func(NotificationCandidate)) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
	return s.cursor
}

// attachForgotten installs the deletion callback, under the same lock as the
// sink so a host cannot be half-attached.
func (s *notifySink) attachForgotten(fn func(id.TerminalID)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = fn
}

func (s *notifySink) forgetHook() func(id.TerminalID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forgotten
}

// next advances the cursor and returns the sink to call, or nil when nothing
// is attached. The cursor advances for EVERY applied event, attached or not,
// so that a gap in what a host saw is visible as a gap rather than silently
// closed up.
func (s *notifySink) next() (func(NotificationCandidate), uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor++
	return s.fn, s.cursor
}

// AttachNotifications installs the host's notification plane and returns the
// presentation cursor as it stood at that moment. Events absorbed from here
// produce candidates; everything already in the log does not, and cannot — it
// was absorbed before there was anywhere to send it.
//
// Passing nil detaches, which is what a host does when the person refuses the
// notification permission: a refusal is an ordinary state, and the core should
// stop producing candidates nobody can render rather than have the host drop
// them silently.
//
// NOTHING IS RETAINED WHILE DETACHED, and that is the design rather than a
// limitation. A queue that held candidates for a host that might come back is
// the same queue that turns a first run over a long journal into one
// notification per message ever written; the difference between the two cases
// is a judgement no buffer can make. What a detached host missed is caught up
// in the log — where it belongs — and the returned cursor is how it learns
// that a gap exists at all.
func (r *Runtime) AttachNotifications(fn func(NotificationCandidate)) uint64 {
	base := r.notify.attach(fn)
	if fn == nil {
		return base
	}
	// ACTIVATION IS NOT ATTACHING (AR-1b.5.3a). The first time a person ever
	// turns notifications on, the frontier becomes the baseline and everything
	// behind it is history — silently, once. Every later attach finds the
	// marker already there and resumes from what the host actually
	// acknowledged, which is why a restart does not re-announce a year.
	// DAMAGED IS ITS OWN ANSWER. With no trustworthy watermark, neither
	// redelivering nor taking a fresh baseline is safe: the first would
	// announce an unknown amount of history, the second would silently decide
	// that an unacknowledged event never happened. So the plane serves LIVE
	// events — silence would be a second loss on top of the first — remembers
	// nothing across a restart, and says so through NotificationPlaneState
	// until somebody resets it deliberately.
	if r.notifyLedger.isDamaged() {
		return base
	}
	if !r.notifyLedger.activated() {
		r.notifyLedger.activate(r.frontiers())
		return base
	}
	// Whatever the log holds past the acknowledged watermark is, by
	// definition, a candidate nobody confirmed — including anything applied
	// while the process was dying. Redelivered here rather than remembered in
	// a queue: the log is the durable record, and a second one would be a
	// second story about what happened.
	r.redeliverUnacknowledged(fn)
	return base
}

// frontiers snapshots every open space's chain state, which is what activation
// freezes as "this is history".
func (r *Runtime) frontiers() map[id.TerminalID][]eventlog.ChainState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[id.TerminalID][]eventlog.ChainState, len(r.spaces))
	for tid, st := range r.spaces {
		out[tid] = st.space.Log.Summary()
	}
	return out
}

// redeliverUnacknowledged walks each space in memory and re-emits what the
// host never confirmed. It runs on the caller's goroutine — the host's, not a
// sync path — so a slow host delays only itself.
func (r *Runtime) redeliverUnacknowledged(fn func(NotificationCandidate)) {
	type pending struct {
		cand NotificationCandidate
		pos  notifyPosition
	}
	var out []pending

	// EVERYTHING IS BUILT UNDER r.mu AND NOTHING IS DELIVERED UNDER IT. A
	// candidate is decorated from the space's live state and from this node's
	// own manifest, which are exactly the things another goroutine may be
	// rewriting; the host, meanwhile, must never be called with the runtime
	// held, or a sink that reaches back in deadlocks against a lock it cannot
	// see. Two loops, and the split is the reason for both.
	r.mu.Lock()
	for tid, st := range r.spaces {
		confirmed := map[id.DeviceID]uint64{}
		for _, ch := range st.space.Log.Summary() {
			confirmed[ch.Device] = r.notifyLedger.confirmedSeq(tid, ch.Device)
		}
		_ = st.space.Log.Replay(func(a eventlog.Applied) error {
			if a.Env == nil || a.Env.Sequence <= confirmed[a.Env.Device] {
				return nil
			}
			out = append(out, pending{
				// Cursor 0: a redelivery is not a position in THIS runtime's
				// stream, and pretending otherwise would hand a host two
				// numbers that look comparable and are not.
				cand: r.candidateOf(tid, st.space, a, 0),
				pos: notifyPosition{
					space: tid, device: a.Env.Device, seq: a.Env.Sequence,
				},
			})
			return nil
		})
	}
	r.mu.Unlock()

	for _, p := range out {
		r.notifyLedger.note(p.cand.EventID, p.pos)
		fn(p.cand)
	}
}

// ArmNotifications is AttachNotifications without the cursor, kept because a
// caller that only wants "start telling me" should not have to ignore a return
// value to say so.
func (r *Runtime) ArmNotifications(fn func(NotificationCandidate)) {
	r.notify.attach(fn)
}

// notifyAbsorbed is called from the absorb funnel, for every space, with r.mu
// held. Everything it reads is in-memory state of the space it was handed —
// no lock is taken and no second journal pass happens.
func (r *Runtime) notifyAbsorbed(tid id.TerminalID, s *terminals.Space, a eventlog.Applied) {
	if a.Env == nil {
		return
	}
	// MACHINERY DOES NOT NOTIFY. A device certificate and a revocation are
	// how the network establishes who may speak (MD-0, ADR-002) — nobody
	// said anything, and a person joining a room should no more be told
	// "a certificate arrived" than "an epoch rotated". Caught by
	// TestJoiningASpaceWithHistoryDoesNotNotifyForThatHistory the moment
	// certificates began to travel.
	switch a.Env.Schema {
	case schemas.DeviceCertified, schemas.DeviceRevoked:
		return
	}
	fn, cursor := r.notify.next()
	if fn == nil {
		return
	}
	c := r.candidateOf(tid, s, a, cursor)
	// Recorded BEFORE the host is told, so a candidate cannot be delivered
	// without a place for its acknowledgement to land.
	r.notifyLedger.note(c.EventID, notifyPosition{
		space: tid, device: a.Env.Device, seq: a.Env.Sequence,
	})
	fn(c)
}

// candidateOf builds what a host may render. Called with r.mu held on the live
// path and without it on the redelivery path, so it touches only the space it
// was handed.
func (r *Runtime) candidateOf(tid id.TerminalID, s *terminals.Space,
	a eventlog.Applied, cursor uint64) NotificationCandidate {

	c := NotificationCandidate{
		EventID:            a.ID,
		SpaceID:            tid,
		Device:             a.Env.Device,
		Schema:             a.Env.Schema,
		SourceSequence:     a.Env.Sequence,
		OccurredAtUnixMs:   a.Env.CreatedAt * 1000, // the protocol counts seconds
		PresentationCursor: cursor,
		AuthoredLocally:    a.Env.Device == r.Device.ID,
	}
	r.decorateLocked(s, a, &c)
	return c
}

// decorateLocked fills the presentation snapshot from what the space already
// holds. Called with r.mu held, on the absorb path, so it does exactly two
// map-and-slice reads and no more: this runs for every applied event of every
// space, and a notification must never become a reason the log is slow.
func (r *Runtime) decorateLocked(s *terminals.Space, a eventlog.Applied, c *NotificationCandidate) {
	if s == nil {
		return
	}
	c.SpaceLabel = manifestTitle(s.ManifestFrame)

	entry, ok := s.State.EntryByID(a.ID)
	if !ok {
		// Ordinary: a schema the reducer does not project has no entry, and a
		// candidate without a preview is still a perfectly good candidate.
		return
	}
	// TEXT ONLY, and never a fallback caption. A voice note, a photo or a file
	// has no line to quote, and inventing one ("sent a photo") here would put
	// a product sentence in the core and an untranslatable English string on
	// every phone. What kind of thing arrived is already in Schema; how to say
	// it belongs to whoever is rendering.
	if entry.Content.Text != nil {
		c.PreviewText = clipRunes(entry.Content.Text.Text, maxPreviewRunes)
	}

	if entry.Author == r.PrincipalID {
		c.SenderLabel = r.displayNameLocked()
		return
	}
	for _, card := range s.MemberCards(0) {
		// Machine cards are skipped for the same reason a quotation skips
		// them: the assistant shares its controller's principal, and a
		// person's words must not arrive under its name.
		if card.Agency == "ai_agent" || card.Kind == "agent" || card.Kind == "bot" {
			continue
		}
		if card.Principal == entry.Author && card.Name != "" {
			c.SenderLabel = card.Name
			return
		}
	}
}

func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// NotificationDeliveredThrough is the last sequence, per space and per device,
// that the host has confirmed holding. Everything at or below it has safely
// reached Android.
//
// Named for what it IS rather than for what somebody might do with it. The
// earlier name — a "retention floor" — is the shape an off-by-one lives in:
// one reader takes 37 to mean "37 must be kept", another takes it to mean
// "everything up to 37 may go", and the disagreement shows up as a lost
// notification months later. Use NotificationRetainFrom for the other
// question; it answers in the units the question is asked in.
func (r *Runtime) NotificationDeliveredThrough() map[id.TerminalID]map[id.DeviceID]uint64 {
	return r.notifyWatermarks(false)
}

// NotificationRetainFrom is the OLDEST sequence anything may still need: the
// first that must survive, per space and per device. A compactor may collapse
// everything strictly before it, and nothing at or after it.
//
// It is deliberately the minimum across BOTH checkpoint generations, not the
// current one. The checkpoint is written as current + previous precisely so
// that damage falls back rather than guesses — and a fallback resumes from the
// OLDER watermark, replaying events the newer one had already confirmed. A
// consumer that trimmed to the current generation would, on that rollback,
// find the core replaying events it had thrown away: the log could not produce
// them, and a host that had already deleted its tombstones would treat them as
// new and resurrect notifications a person dismissed a week ago.
//
// So the floor is what the OLDEST recoverable generation still needs.
func (r *Runtime) NotificationRetainFrom() map[id.TerminalID]map[id.DeviceID]uint64 {
	out := r.notifyWatermarks(true)
	for _, m := range out {
		for dev, seq := range m {
			m[dev] = seq + 1 // the first that must survive, not the last that may go
		}
	}
	return out
}

func (r *Runtime) notifyWatermarks(acrossGenerations bool) map[id.TerminalID]map[id.DeviceID]uint64 {
	out := map[id.TerminalID]map[id.DeviceID]uint64{}
	if r.notifyLedger == nil {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for tid, st := range r.spaces {
		m := map[id.DeviceID]uint64{}
		for _, ch := range st.space.Log.Summary() {
			if acrossGenerations {
				m[ch.Device] = r.notifyLedger.recoverableSeq(tid, ch.Device)
			} else {
				m[ch.Device] = r.notifyLedger.confirmedSeq(tid, ch.Device)
			}
		}
		out[tid] = m
	}
	return out
}

// AttachSpaceForgotten installs the callback that says a space was deleted
// from this device, so the host can take down what it is showing for it
// instead of waiting for the next reconciliation.
//
// WITHOUT IT, THE TWO HALVES DISAGREE IN THE WORST DIRECTION: a person says
// "forget this space" and the shade goes on showing the conversation as a
// live thing. The person is right and the notification is a leftover, but the
// notification is the part they can see.
func (r *Runtime) AttachSpaceForgotten(fn func(id.TerminalID)) {
	r.notify.attachForgotten(fn)
}
