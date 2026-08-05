// AR-1b.1 — the notification candidate contract.
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
// So the sink is ARMED, and arming is a separate act from wiring. Until a host
// arms it, absorbing produces nothing at all. `node.Open` finishes its replays
// before anybody can arm, which makes history unable to notify by construction
// — there is no set of ids to get right and no frontier to forget to persist.
// The durable presentation frontier and the id set still belong to the
// coordinator (AR-1b.6); they defend against reconnect and process restart,
// which are different hazards from replay.
package node

import (
	"sync"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// NotificationCandidate is the narrow fact a host may render. It carries no
// frames, no keys and no protocol: everything here has already been verified.
type NotificationCandidate struct {
	EventID   id.EventID
	SpaceID   id.TerminalID
	Device    id.DeviceID
	Schema    string
	CreatedAt uint64
	// AuthoredLocally marks our own event. A person is not told about the
	// thing they just did, and the host does not have to work out who they
	// are to know that.
	AuthoredLocally bool
}

// notifySink is the runtime's armed notification plane.
type notifySink struct {
	mu   sync.RWMutex
	fn   func(NotificationCandidate)
	live bool
}

func (s *notifySink) arm(fn func(NotificationCandidate)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn, s.live = fn, fn != nil
}

func (s *notifySink) emit(c NotificationCandidate) {
	s.mu.RLock()
	fn, live := s.fn, s.live
	s.mu.RUnlock()
	if !live || fn == nil {
		return
	}
	fn(c)
}

// ArmNotifications installs the host's notification plane. Events absorbed
// from this moment produce candidates; everything already in the log does not,
// and cannot — it was absorbed before there was anywhere to send it.
//
// Passing nil disarms, which is what a host does when the person refuses the
// notification permission: a refusal is an ordinary state, and the core should
// stop producing candidates nobody can render rather than have the host drop
// them silently.
func (r *Runtime) ArmNotifications(fn func(NotificationCandidate)) {
	r.notify.arm(fn)
}

// notifyAbsorbed is called from the absorb funnel, for every space.
func (r *Runtime) notifyAbsorbed(tid id.TerminalID, a eventlog.Applied) {
	if a.Env == nil {
		return
	}
	r.notify.emit(NotificationCandidate{
		EventID:         a.ID,
		SpaceID:         tid,
		Device:          a.Env.Device,
		Schema:          a.Env.Schema,
		CreatedAt:       a.Env.CreatedAt,
		AuthoredLocally: a.Env.Device == r.Device.ID,
	})
}
