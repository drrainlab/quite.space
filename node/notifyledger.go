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
// then delete it on acknowledgement — is the obvious shape. It was not built,
// for three reasons, and the guarantee is the same:
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
// THREE STATES, NOT TWO, AND THIS IS THE CORRECTION THAT MATTERS. An earlier
// version read a damaged file as "never activated", which sounds cautious and
// is the one behaviour that loses a message silently:
//
//	the plane is active · an event is applied · the host has not confirmed it
//	· the file is damaged · the process restarts · "never activated" · the
//	frontier becomes a fresh baseline · the event is history now · nobody is
//	ever told
//
// So damage is its own answer. `never` takes a baseline; `active` resumes;
// `damaged` does NEITHER — it does not announce history, does not invent a
// baseline, does not write anything at all, and says so, until somebody
// deliberately resets it. Live events still reach the host, because silence
// would be a second loss on top of the first.
//
// The file is written as two generations with a checksum, so ordinary damage —
// a half-written file, a truncated one — is survived by falling back rather
// than by guessing. Only losing BOTH generations reaches `damaged`.
//
// THE ASYMMETRY IS DELIBERATE. Acknowledgements are written back debounced, so
// a crash can lose one; a lost acknowledgement costs a REDELIVERY, which the
// host's own event-id dedup absorbs into nothing. A lost event costs a
// notification, which nothing absorbs. One of those is recoverable and the
// other is not, so the cheap write is the one allowed to be late.
//
// A CONSTRAINT FOR WHOEVER ADDS LOG COMPACTION. There is none today — no
// snapshot, no checkpoint, nothing collapses a segment — and when it arrives
// it must treat this watermark as a retention pin. An event dropped from the
// log before the host acknowledged it cannot be redelivered by anybody, and
// journal-as-outbox stops being an outbox on the quietest possible failure.
package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// The checkpoint, in two generations. Plain JSON beside relays.json and
// quicklinks.json, carrying the same class of thing they do: local
// bookkeeping, no secrets, no content. Space ids and sequence numbers only —
// never a name, never a preview, never a payload.
const (
	notifyLedgerFile     = "notifications.json"
	notifyLedgerPrevFile = "notifications.prev.json"
	notifyLedgerSchema   = 1
)

// ackFlushEvery bounds how often the watermark reaches disk. Acknowledgements
// arrive one per delivered candidate, which during a catch-up is as often as
// events; writing each one would put a file rewrite on the sync path to buy
// something a redelivery already provides for free.
const ackFlushEvery = 2 * time.Second

// The plane's durable state, as three answers rather than a boolean.
const (
	// NotifyPlaneNever — nobody has ever turned notifications on here. The
	// next activation takes the frontier as its baseline, once.
	NotifyPlaneNever = "never_activated"
	// NotifyPlaneActive — activated, with a watermark to resume from.
	NotifyPlaneActive = "active"
	// NotifyPlaneDamaged — the checkpoint could not be read or cannot be
	// written. Neither a baseline nor a redelivery is safe, so neither
	// happens, and nothing is persisted until somebody resets deliberately.
	NotifyPlaneDamaged = "metadata_corrupt"
)

type notifyLedgerState struct {
	Schema     int    `json:"schema_version"`
	Generation uint64 `json:"generation"`

	// Activated records that a person has, at some point, turned notifications
	// on. Its absence is what makes a first run silent; its presence is what
	// stops a restart being mistaken for one.
	Activated bool `json:"activated"`

	// Confirmed is space -> device -> the last sequence the host has
	// acknowledged holding durably. Anything past it in the log is a candidate
	// nobody has confirmed.
	Confirmed map[string]map[string]uint64 `json:"confirmed"`

	// Checksum covers everything above. Not a security property — the file is
	// ours and anybody who can rewrite it can recompute this — but a
	// half-written file is exactly what it catches, and a half-written file is
	// what a phone losing power produces.
	Checksum string `json:"checksum"`
}

func (s notifyLedgerState) sum() string {
	c := s
	c.Checksum = ""
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type notifyLedger struct {
	mu       sync.Mutex
	path     string
	prevPath string
	state    notifyLedgerState

	// damaged is set when neither generation could be read, or when a write
	// failed. It is deliberately NOT the same as "not activated": see the
	// package comment.
	damaged bool

	// pending maps an event id to where it sits in a chain, so an
	// acknowledgement — which names an event — can advance a watermark, which
	// counts sequences. Bounded by what is unacknowledged, which is bounded by
	// how far behind the host is.
	pending map[id.EventID]notifyPosition

	// above holds acknowledged sequences that are not yet contiguous with the
	// watermark. A host may acknowledge out of order; the watermark may only
	// move over a gap that has been filled. Memory-only on purpose — losing it
	// costs a redelivery, which the host deduplicates away.
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
		path:     filepath.Join(dataDir, notifyLedgerFile),
		prevPath: filepath.Join(dataDir, notifyLedgerPrevFile),
		pending:  map[id.EventID]notifyPosition{},
		above:    map[string]map[string]map[uint64]bool{},
	}
	l.state.Schema = notifyLedgerSchema
	l.state.Confirmed = map[string]map[string]uint64{}
	l.load()
	return l
}

// load reads the current generation, falls back to the previous one, and
// reaches "damaged" only when neither can be trusted.
func (l *notifyLedger) load() {
	cur, curOK := readNotifyLedgerFile(l.path)
	if curOK {
		l.state = cur
		return
	}
	prev, prevOK := readNotifyLedgerFile(l.prevPath)
	if prevOK {
		// The previous generation is BEHIND the lost one, so resuming from it
		// redelivers what was acknowledged in between. That is the safe
		// direction: a duplicate candidate is deduplicated by the host, and a
		// missing one is a message nobody hears about.
		l.state = prev
		return
	}
	if !fileMissing(l.path) || !fileMissing(l.prevPath) {
		// Something is there and neither generation parsed. Not a first run.
		l.damaged = true
	}
}

func readNotifyLedgerFile(path string) (notifyLedgerState, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return notifyLedgerState{}, false
	}
	var st notifyLedgerState
	if err := json.Unmarshal(b, &st); err != nil {
		return notifyLedgerState{}, false
	}
	if st.Schema != notifyLedgerSchema {
		return notifyLedgerState{}, false
	}
	if st.Confirmed == nil {
		st.Confirmed = map[string]map[string]uint64{}
	}
	if st.Checksum == "" || st.Checksum != st.sum() {
		return notifyLedgerState{}, false
	}
	return st, true
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// saveLocked writes the next generation. Callers hold l.mu.
//
// The order is what makes a torn write survivable: the new bytes are fully on
// disk before the current generation becomes the previous one, so at every
// instant at least one complete generation exists.
func (l *notifyLedger) saveLocked() bool {
	if l.damaged {
		// Nothing is written while damaged. A watermark invented on top of an
		// unknown one is exactly the silent rebaseline this state exists to
		// prevent.
		return false
	}
	l.state.Schema = notifyLedgerSchema
	l.state.Generation++
	l.state.Checksum = l.state.sum()

	b, err := json.MarshalIndent(l.state, "", "  ")
	if err != nil {
		l.damaged = true
		return false
	}
	tmp := l.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		l.damaged = true
		return false
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		l.damaged = true
		return false
	}
	if err := f.Sync(); err != nil { // on disk BEFORE it is called current
		f.Close()
		os.Remove(tmp)
		l.damaged = true
		return false
	}
	f.Close()

	// The current generation steps back before the new one steps forward. A
	// crash between the two renames leaves the previous generation intact,
	// which load() knows how to use.
	_ = os.Rename(l.path, l.prevPath)
	if err := os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp)
		l.damaged = true
		return false
	}
	if d, err := os.Open(filepath.Dir(l.path)); err == nil {
		_ = d.Sync() // the renames themselves, not just the bytes
		d.Close()
	}
	l.dirty = false
	l.lastFlush = time.Now()
	return true
}

// planeState is the three-valued answer.
func (l *notifyLedger) planeState() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.damaged:
		return NotifyPlaneDamaged
	case l.state.Activated:
		return NotifyPlaneActive
	default:
		return NotifyPlaneNever
	}
}

func (l *notifyLedger) activated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state.Activated
}

func (l *notifyLedger) isDamaged() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.damaged
}

// activate marks the plane as switched on and takes the frontier as the
// baseline. Idempotent: a second call changes nothing, which is what makes a
// restart resume rather than re-announce.
//
// THE MARKER IS DURABLE BEFORE THE PLANE IS ACTIVE. If the write fails, this
// reports failure and the state stays "not activated" — the alternative is a
// plane that considers itself active in memory and, after a crash, activates
// for the "first" time all over again with a later baseline.
func (l *notifyLedger) activate(frontiers map[id.TerminalID][]eventlog.ChainState) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.damaged || l.state.Activated {
		return false
	}
	before := l.state
	l.state.Activated = true
	for tid, chains := range frontiers {
		m := map[string]uint64{}
		for _, c := range chains {
			m[c.Device.Hex()] = c.ContiguousUntil
		}
		l.state.Confirmed[tid.Hex()] = m
	}
	if !l.saveLocked() {
		l.state = before // never active in memory but absent on disk
		return false
	}
	return true
}

// reset is the deliberate operation that leaves the damaged state: it throws
// the unreadable checkpoint away and starts again from the current frontier.
// Everything not yet acknowledged becomes history, which is why nothing does
// this on a person's behalf.
func (l *notifyLedger) reset(frontiers map[id.TerminalID][]eventlog.ChainState) bool {
	l.mu.Lock()
	l.damaged = false
	l.state = notifyLedgerState{
		Schema:    notifyLedgerSchema,
		Confirmed: map[string]map[string]uint64{},
	}
	l.above = map[string]map[string]map[uint64]bool{}
	l.pending = map[id.EventID]notifyPosition{}
	l.mu.Unlock()
	return l.activate(frontiers)
}

// baselineIfUnknown gives a space its first watermark: whatever it already
// holds at the moment the plane first sees it is history.
//
// THE DOOR THIS CLOSES. Activation freezes the frontier of every space open at
// the time — but a space JOINED LATER has no entry at all, and confirmedSeq
// answers zero for anything it has never heard of. The live path is safe,
// because join history is installed before the absorb funnel exists; the
// REDELIVERY path is not, because it walks the log from the watermark, and a
// watermark of zero makes an imported history look entirely unacknowledged.
// The first restart after joining a long-running room would have handed the
// host every message ever written in it.
//
// A space with no entry has never had a candidate delivered for it, so there
// is nothing owed: taking its current frontier as the baseline cannot hide a
// message that was already on its way to somebody.
func (l *notifyLedger) baselineIfUnknown(tid id.TerminalID, chains []eventlog.ChainState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.damaged || !l.state.Activated {
		return
	}
	if _, known := l.state.Confirmed[tid.Hex()]; known {
		return
	}
	m := map[string]uint64{}
	for _, c := range chains {
		m[c.Device.Hex()] = c.ContiguousUntil
	}
	l.state.Confirmed[tid.Hex()] = m
	l.saveLocked()
}

// confirmedSeq is the last sequence of one chain the host has acknowledged.
func (l *notifyLedger) confirmedSeq(tid id.TerminalID, dev id.DeviceID) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state.Confirmed[tid.Hex()][dev.Hex()]
}

// recoverableSeq is the watermark of the OLDEST generation that could still be
// loaded — the minimum of current and previous.
//
// If the current checkpoint is damaged, load() falls back to the previous one
// and resumes from ITS watermark, replaying what the newer generation had
// already confirmed. Anything a consumer trims must therefore be behind that
// older line, not behind the current one: the difference between the two is
// exactly the window in which a rollback resurrects notifications a person had
// already dealt with.
func (l *notifyLedger) recoverableSeq(tid id.TerminalID, dev id.DeviceID) uint64 {
	l.mu.Lock()
	cur := l.state.Confirmed[tid.Hex()][dev.Hex()]
	l.mu.Unlock()

	prev, ok := readNotifyLedgerFile(l.prevPath)
	if !ok {
		return cur
	}
	if p := prev.Confirmed[tid.Hex()][dev.Hex()]; p < cur {
		return p
	}
	return cur
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
//
// EVERY delivered candidate is acknowledged, including one the host decided
// not to show: the question this answers is "has it been dealt with", not "was
// somebody woken". Otherwise an ordinary membership event at sequence 38 would
// pin the watermark there forever while 39 and 40 sat acknowledged behind it.
func (l *notifyLedger) ack(eid id.EventID) {
	l.mu.Lock()
	defer l.mu.Unlock()

	p, ok := l.pending[eid]
	if !ok {
		return // never emitted, or already advanced past
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
// decided to suppress exactly as readily as one it posted.
func (r *Runtime) AckNotification(eid id.EventID) {
	if r.notifyLedger == nil {
		return
	}
	r.notifyLedger.ack(eid)
}

// NotificationPlaneState is what a host displays when notifications are not
// behaving: never_activated, active, or metadata_corrupt. The third is the one
// worth surfacing — it means live notifications still work and nothing is
// being remembered across a restart.
func (r *Runtime) NotificationPlaneState() string {
	if r.notifyLedger == nil {
		return NotifyPlaneNever
	}
	return r.notifyLedger.planeState()
}

// ResetNotificationPlane throws away an unreadable checkpoint and starts again
// from the current frontier. Everything not yet acknowledged becomes history,
// so this is a deliberate act with a person behind it — never a recovery step
// something takes on its own.
func (r *Runtime) ResetNotificationPlane() bool {
	if r.notifyLedger == nil {
		return false
	}
	return r.notifyLedger.reset(r.frontiers())
}
