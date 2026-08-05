// AR-1b.2 — the seam a candidate crosses to reach Android.
//
// node/notify.go produces candidates BELOW the interface, after verification
// and application. This file is the only place they cross into Java, and it
// has exactly three jobs: keep the absorb path free, keep arming honest, and
// never let a host's failure become the core's.
//
// THE ABSORB PATH MUST NOT BLOCK, and that is not tidiness. Space.OnAbsorb
// runs inside the runtime's own critical section — the comment at
// node/node.go's Runtime.notify says as much — so a sink called INLINE would
// hold a sync in flight for as long as a Java method takes to return, and a
// host that reached back into the core from inside it would deadlock against
// a lock it cannot see. So the emit path does one non-blocking send into a
// bounded queue and returns; a pump goroutine calls Java.
//
// THE QUEUE IS BOUNDED, AND OVERFLOW IS COUNTED RATHER THAN HIDDEN. An
// unbounded queue would make a stalled host into a memory leak in a process
// Android is already looking for reasons to kill. A dropped candidate costs a
// notification, never an event: the log is the truth and it is untouched. The
// count is in Status so "notifications went quiet" is answerable rather than
// mysterious.
//
// WHAT THE ARMING WINDOW DOES AND DOES NOT COVER — recorded rather than
// quietly relied on. Start arms only after node.Open has returned, which is
// what makes history unable to notify by construction. The cost is the window
// between Open finishing its replays and the arm call: an event that arrives
// over the relay inside those milliseconds is applied and caught up, but is
// never announced. Closing that honestly needs an applied_frontier in the
// candidate DTO (AR-1b.1 shipped without one) plus the durable presentation
// frontier of AR-1b.5 — it is NOT closable on the Java side alone, and the
// coordinator's id set cannot recover a candidate that was never produced.
package quietcore

import (
	"sync"
	"sync/atomic"

	"github.com/drrainlab/quiet_places/node"
)

// notifyQueueDepth bounds what one slow host may hold in memory. Sized for a
// burst — a sync catching up a busy space delivers tens of events in a tick —
// not for an outage, because a host that has been unable to render for 256
// events is not going to be helped by the 257th.
const notifyQueueDepth = 256

// Candidate is the Java-visible shape of node.NotificationCandidate. Ids are
// hex strings rather than byte arrays: gomobile carries strings across the
// boundary intact, and a host that must not interpret the protocol has no use
// for the bytes anyway — it needs a stable key and a routing target.
type Candidate struct {
	EventID string
	SpaceID string
	Device  string
	Schema  string

	// OccurredAtUnixMs is the AUTHOR's clock, in MILLISECONDS, unverified and
	// not ours. The unit is in the name because the version without it cost a
	// live run: the protocol counts seconds, Android counts milliseconds, and
	// the shade rendered every notification as "56y" ago.
	//
	// For ordering what a person is shown, never for deciding what is new —
	// that is PresentationCursor's job, and a device with a skewed clock must
	// not be able to silence or resurrect notifications on somebody else's
	// phone.
	OccurredAtUnixMs int64

	// PresentationCursor is monotonic in this runtime's stream of applied
	// events, assigned after verify and apply. Not a protocol frontier and not
	// comparable across nodes OR across runs: it is scoped to one runtime, so
	// a host that stores it must store the runtime epoch beside it and treat a
	// change of epoch as "there is nothing to resume".
	PresentationCursor int64

	// AuthoredLocally marks our own event. A person is not told about the
	// thing they just did, and the host does not have to work out who they are
	// to know that.
	AuthoredLocally bool

	// The presentation snapshot: what may be SHOWN, as opposed to what may be
	// acted on. Every field is optional and an empty one is ordinary. Nothing
	// in the dedup or the cursor depends on them, so a missing label can never
	// cost a notification.
	SpaceLabel  string
	SenderLabel string
	PreviewText string
}

// NotificationSink is implemented in JAVA. Two obligations, stated because
// nothing enforces them across the boundary:
//
//	it is called on a Go goroutine, not the main thread — be thread-safe
//	it must return promptly and must not call back into the core
type NotificationSink interface {
	OnCandidate(c *Candidate)
}

var (
	// notifyMu guards the sink and the queue only. It is deliberately NOT
	// stateMu: a host arming or disarming must never queue behind an open,
	// and Status must never queue behind a host.
	notifyMu   sync.Mutex
	notifySink NotificationSink
	notifyQ    chan *Candidate

	notifyDelivered atomic.Int64
	notifyDropped   atomic.Int64

	// notifyBaseline is the cursor the core reported when the plane was last
	// attached. Reported in Status so a host can tell "nothing has happened
	// since I attached" from "I have not been listening" — the two look
	// identical from the shade.
	notifyBaseline atomic.Int64
)

// ArmNotifications installs the host's sink. Safe to call before the core is
// open — the usual case, since a host installs it at Application scope — and
// safe to call while it is running, which arms the live runtime immediately.
//
// Passing nil disarms. A refused notification permission is an ordinary state,
// and a core that keeps producing candidates nobody can render is worse than
// one that stops: it burns the queue and hides the refusal.
func ArmNotifications(sink NotificationSink) {
	notifyMu.Lock()
	notifySink = sink
	if sink != nil && notifyQ == nil {
		notifyQ = make(chan *Candidate, notifyQueueDepth)
		go notifyPump(notifyQ)
	}
	notifyMu.Unlock()

	// The lock is released first on purpose: armRuntime touches stateMu, and
	// taking two locks in one call is how an ordering rule gets invented by
	// accident and violated three commits later.
	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	if r != nil {
		armRuntime(r)
	}
}

// DisarmNotifications is ArmNotifications(nil) under a name a Java caller can
// read. gomobile turns a nil interface argument into a null check the caller
// has to get right; a second method removes the question.
func DisarmNotifications() { ArmNotifications(nil) }

// armRuntime points the runtime's plane at this file's queue, or takes it
// down. Called after Open has returned and whenever the sink changes.
func armRuntime(r *node.Runtime) {
	notifyMu.Lock()
	live := notifySink != nil
	notifyMu.Unlock()

	if !live {
		r.ArmNotifications(nil)
		return
	}
	// AttachNotifications, not ArmNotifications: the baseline cursor is read
	// and the sink installed in ONE critical section, so no event can be
	// applied between a host learning where it stands and being able to hear.
	notifyBaseline.Store(int64(r.AttachNotifications(func(c node.NotificationCandidate) {
		notifyMu.Lock()
		q := notifyQ
		notifyMu.Unlock()
		if q == nil {
			return
		}
		select {
		case q <- &Candidate{
			EventID:            c.EventID.Hex(),
			SpaceID:            c.SpaceID.Hex(),
			Device:             c.Device.Hex(),
			Schema:             c.Schema,
			OccurredAtUnixMs:   int64(c.OccurredAtUnixMs),
			PresentationCursor: int64(c.PresentationCursor),
			AuthoredLocally:    c.AuthoredLocally,
			SpaceLabel:         c.SpaceLabel,
			SenderLabel:        c.SenderLabel,
			PreviewText:        c.PreviewText,
		}:
		default:
			// Drop, count, and never block: this runs inside the absorb path,
			// where waiting on a host would stall sync for everybody.
			notifyDropped.Add(1)
		}
	})))
}

// notifyPump is the one goroutine that calls Java. It outlives Stop and Start
// so a restarted core reuses it rather than leaking a pump per open.
func notifyPump(q chan *Candidate) {
	for c := range q {
		notifyMu.Lock()
		s := notifySink
		notifyMu.Unlock()
		if s == nil {
			continue // disarmed while queued — the candidate simply expires
		}
		deliver(s, c)
	}
}

// deliver isolates the host's failure from the core's liveness. A Java
// exception crosses gomobile as a panic; unrecovered it would kill the pump
// and every notification after it, silently, for the life of the process.
func deliver(s NotificationSink, c *Candidate) {
	defer func() {
		if recover() != nil {
			notifyDropped.Add(1)
		}
	}()
	s.OnCandidate(c)
	notifyDelivered.Add(1)
}

// notifyStatus is folded into Status(). Three numbers, because "no
// notifications appeared" has three different causes and they need different
// answers: nothing was armed, nothing was produced, or the host could not keep
// up.
func notifyStatus() map[string]any {
	notifyMu.Lock()
	armed := notifySink != nil
	notifyMu.Unlock()
	return map[string]any{
		"notify_armed":     armed,
		"notify_delivered": notifyDelivered.Load(),
		"notify_dropped":   notifyDropped.Load(),
		"notify_baseline":  notifyBaseline.Load(),
	}
}
