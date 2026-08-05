// AR-1b.5.3a/5.3b — what survives a process that dies mid-notification.
//
// THE WINDOW THIS CLOSES. Until now the plane was live-only: an event applied
// in Go produced a candidate, the candidate crossed into the host, and if the
// process died anywhere in between the notification was gone for good. The
// runtime cursor cannot help — it is scoped to one runtime, deliberately, and
// a reopened node starts again at zero with every prior event already replayed
// past a detached sink. So a crash between "applied" and "the host has it
// durably" silently ate a message.
//
// THE LOG IS THE OUTBOX, and this is the one design decision worth arguing
// about. A second durable queue — append a record beside every applied event,
// then delete it on acknowledgement — is the obvious shape, and it is what the
// review asked for. It was not built, for three reasons, and the guarantee is
// the same:
//
//  1. It would not close the window it exists for. The append happens AFTER
//     apply and cannot share its commit, so a crash in between loses the
//     record exactly as before. Only the log's own durability closes that, and
//     the log already has it.
//  2. It would be a second source of truth about what happened, which is the
//     thing this codebase spends its correctness budget avoiding.
//  3. It would put previews and names in a plaintext file. The candidate's
//     labels are resolved from state that is already in memory; a durable
//     queue would have to write them down, and a person's messages would then
//     rest in cleartext beside a log that is sealed.
//
// So what is persisted is not the events but the WATERMARK: per space, per
// device, the sequence up to which the host has confirmed it holds things
// durably. Everything the log holds beyond that watermark is, by definition,
// an unacknowledged candidate — recomputed at attach rather than remembered.
//
// ACTIVATION IS A DIFFERENT FACT FROM ATTACHING, and conflating them is how a
// restart re-announces a year of history. The first time a person turns
// notifications on, the current frontier becomes the watermark and everything
// behind it is history, silently. Every later attach — a new process, a new
// runtime epoch, a permission granted again — finds the marker already there
// and resumes from what was actually acknowledged.
//
// THE ASYMMETRY IS DELIBERATE. Acknowledgements are written back debounced, so
// a crash can lose one; a lost acknowledgement costs a REDELIVERY, which the
// host's own event-id dedup absorbs into nothing. A lost event costs a
// notification, which nothing absorbs. One of those is recoverable and the
// other is not, so the cheap write is the one allowed to be late.
package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// notifyLedgerFile is plain JSON beside relays.json and quicklinks.json, and
// carries the same class of thing they do: local bookkeeping, no secrets, no
// content. Space ids and sequence numbers only — never a name, never a
// preview, never a payload.
const notifyLedgerFile = "notifications.json"

// ackFlushEvery bounds how often the watermark reaches disk. Acknowledgements
// arrive one per delivered candidate, which during a catch-up is as often as
// events; writing each one would put a file rewrite on the sync path to buy
// something a redelivery already provides for free.
const ackFlushEvery = 2 * time.Second

type notifyLedgerState struct {
	// Activated records that a person has, at some point, turned notifications
	// on. Its absence is what makes a first run silent; its presence is what
	// stops a restart being mistaken for one.
	Activated bool `json:"activated"`

	// Confirmed is space -> device -> the last sequence the host has
	// acknowledged holding durably. Anything past it in the log is a candidate
	// nobody has confirmed.
	Confirmed map[string]map[string]uint64 `json:"confirmed"`
}

type notifyLedger struct {
	mu    sync.Mutex
	path  string
	state notifyLedgerState

	// pending maps an event id to where it sits in a chain, so an
	// acknowledgement — which names an event — can advance a watermark, which
	// counts sequences. Bounded by what is unacknowledged, which is bounded by
	// how far behind the host is.
	pending map[id.EventID]notifyPosition

	// above holds acknowledged sequences that are not yet contiguous with the
	// watermark. A host may acknowledge out of order; the watermark may only
	// move over a gap that has been filled.
	above map[string]map[string]map[uint64]bool

	dirty     bool
	lastFlush time.Time
}

type notifyPosition struct {
	space  id.TerminalID
	device id.DeviceID
	seq    uint64
}

func newNotifyLedger(dataDir string) *notifyLedger {
	l := &notifyLedger{
		path:    filepath.Join(dataDir, notifyLedgerFile),
		pending: map[id.EventID]notifyPosition{},
		above:   map[string]map[string]map[uint64]bool{},
	}
	l.state.Confirmed = map[string]map[string]uint64{}
	l.load()
	return l
}

func (l *notifyLedger) load() {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return // absent is the ordinary first-run state, not a failure
	}
	var st notifyLedgerState
	if err := json.Unmarshal(b, &st); err != nil {
		// A corrupt ledger is recoverable in the safe direction: treat it as
		// never activated, so the worst case is silence until the person turns
		// notifications on again — never a re-announcement of everything.
		return
	}
	if st.Confirmed == nil {
		st.Confirmed = map[string]map[string]uint64{}
	}
	l.state = st
}

// saveLocked writes the watermark. Callers hold l.mu.
func (l *notifyLedger) saveLocked() {
	b, err := json.MarshalIndent(l.state, "", "  ")
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp)
		return
	}
	l.dirty = false
	l.lastFlush = time.Now()
}

func (l *notifyLedger) activated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state.Activated
}

// activate marks the plane as switched on and takes the frontier as the
// baseline. Idempotent: a second call changes nothing, which is what makes a
// restart resume rather than re-announce.
func (l *notifyLedger) activate(frontiers map[id.TerminalID][]eventlog.ChainState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.Activated {
		return
	}
	l.state.Activated = true
	for tid, chains := range frontiers {
		m := map[string]uint64{}
		for _, c := range chains {
			m[c.Device.Hex()] = c.ContiguousUntil
		}
		l.state.Confirmed[tid.Hex()] = m
	}
	l.saveLocked()
}

// confirmedSeq is the last sequence of one chain the host has acknowledged.
func (l *notifyLedger) confirmedSeq(tid id.TerminalID, dev id.DeviceID) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state.Confirmed[tid.Hex()][dev.Hex()]
}

// note remembers where an emitted candidate sits, so its acknowledgement can
// be turned into a watermark.
func (l *notifyLedger) note(eid id.EventID, p notifyPosition) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending[eid] = p
}

// ack records that the host holds this event durably and advances the
// watermark as far as the acknowledgements are contiguous.
func (l *notifyLedger) ack(eid id.EventID) {
	l.mu.Lock()
	defer l.mu.Unlock()

	p, ok := l.pending[eid]
	if !ok {
		return // an ack for something we never emitted, or already advanced past
	}
	delete(l.pending, eid)

	sp, dv := p.space.Hex(), p.device.Hex()
	if l.above[sp] == nil {
		l.above[sp] = map[string]map[uint64]bool{}
	}
	if l.above[sp][dv] == nil {
		l.above[sp][dv] = map[uint64]bool{}
	}
	l.above[sp][dv][p.seq] = true

	if l.state.Confirmed[sp] == nil {
		l.state.Confirmed[sp] = map[string]uint64{}
	}
	// Contiguity, not a maximum: acknowledging event 40 while 38 is still
	// unconfirmed must not move the watermark past 38, or a crash would lose
	// the one in the middle.
	for {
		next := l.state.Confirmed[sp][dv] + 1
		if !l.above[sp][dv][next] {
			break
		}
		delete(l.above[sp][dv], next)
		l.state.Confirmed[sp][dv] = next
	}

	l.dirty = true
	if time.Since(l.lastFlush) >= ackFlushEvery {
		l.saveLocked()
	}
}

// flush writes anything outstanding. Called on Close, where the cost of a
// write no longer competes with the sync path.
func (l *notifyLedger) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dirty {
		l.saveLocked()
	}
}

// AckNotification tells the core that the host holds this candidate durably.
// Until it does, the candidate is redelivered on every attach — which is the
// whole mechanism: no acknowledgement, no forgetting.
//
// Acknowledging is NOT the same as showing. A host acknowledges a candidate it
// decided to suppress exactly as readily as one it posted: the question the
// watermark answers is "has this been dealt with", not "was somebody woken".
func (r *Runtime) AckNotification(eid id.EventID) {
	if r.notifyLedger == nil {
		return
	}
	r.notifyLedger.ack(eid)
}
