// Package eventlog implements the append-only Event Store for one terminal
// (M0.4, ADR-004/ADR-006): per-device hash chains, dedup, gap buffering for
// out-of-order arrival, fork quarantine, and replay-based rebuild.
package eventlog

import (
	"errors"
	"fmt"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// AdmitFunc decides whether a structurally valid, signature-verified event
// is admitted (device certification, revocation — kernel/identity). Nil
// means admit all signed events (tests, bootstrap).
type AdmitFunc func(env *signal.Envelope) error

// Applied is one event accepted into the log.
type Applied struct {
	Env   *signal.Envelope
	ID    id.EventID
	Frame []byte
}

// Errors surfaced to callers. Forks and quarantine are loud by design
// (invariant §2.7).
var (
	ErrWrongTerminal = errors.New("eventlog: frame addressed to another terminal")
	ErrChainForked   = errors.New("eventlog: chain fork detected — device quarantined")
	ErrPendingFull   = errors.New("eventlog: too many out-of-order events pending for device")
)

// maxPendingPerChain bounds the reorder buffer (storage-exhaustion guard,
// plan §26 adversarial tests).
const maxPendingPerChain = 1024

type chain struct {
	next    uint64 // next expected sequence (contiguous_until + 1)
	tip     id.EventID
	pending map[uint64][]byte
	forked  bool
}

// Log is the event store for one terminal.
type Log struct {
	Terminal id.TerminalID

	store *SegmentStore // nil = memory only
	admit AdmitFunc

	frames  map[id.EventID][]byte
	order   []id.EventID // append order for replay
	chains  map[id.DeviceID]*chain
	quarant map[id.DeviceID][][]byte // frames set aside after a fork
}

// New creates an in-memory log.
func New(terminal id.TerminalID, admit AdmitFunc) *Log {
	return &Log{
		Terminal: terminal,
		admit:    admit,
		frames:   map[id.EventID][]byte{},
		chains:   map[id.DeviceID]*chain{},
		quarant:  map[id.DeviceID][][]byte{},
	}
}

// Open creates a log backed by segment files and replays existing frames
// through the same validation path (M0.4: rebuild reproduces state).
func Open(terminal id.TerminalID, dir string, admit AdmitFunc) (*Log, []Applied, error) {
	store, err := OpenSegments(dir)
	if err != nil {
		return nil, nil, err
	}
	l := New(terminal, admit)
	var replayed []Applied
	err = store.Scan(func(frame []byte) error {
		applied, err := l.ingest(frame, false)
		if err != nil {
			return fmt.Errorf("eventlog: stored frame rejected on replay: %w", err)
		}
		replayed = append(replayed, applied...)
		return nil
	})
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	l.store = store
	return l, replayed, nil
}

// Close releases storage.
func (l *Log) Close() error {
	if l.store != nil {
		return l.store.Close()
	}
	return nil
}

// Ingest validates and applies one frame. Duplicates are silent no-ops
// (idempotency, plan §17.3). Out-of-order frames are buffered until their
// predecessor arrives; everything applied as a result is returned.
func (l *Log) Ingest(frame []byte) ([]Applied, error) {
	return l.ingest(frame, true)
}

func (l *Log) ingest(frame []byte, persist bool) ([]Applied, error) {
	env, err := signal.Decode(frame)
	if err != nil {
		return nil, err
	}
	if env.Terminal != l.Terminal {
		return nil, ErrWrongTerminal
	}
	if err := signal.VerifyFrame(frame, env); err != nil {
		return nil, err
	}
	eid := id.EventIDOf(frame)
	if _, dup := l.frames[eid]; dup {
		return nil, nil // replay/duplicate: no-op
	}
	if l.admit != nil {
		if err := l.admit(env); err != nil {
			return nil, err
		}
	}
	c, ok := l.chains[env.Device]
	if !ok {
		c = &chain{next: 1, pending: map[uint64][]byte{}}
		l.chains[env.Device] = c
	}
	if c.forked {
		l.quarant[env.Device] = append(l.quarant[env.Device], frame)
		return nil, ErrChainForked
	}

	switch {
	case env.Sequence < c.next:
		// Slot already filled by a different event (dedup would have caught
		// the identical one): this is a fork (ADR-004).
		return nil, l.forkChain(env.Device, frame)
	case env.Sequence > c.next:
		if len(c.pending) >= maxPendingPerChain {
			return nil, ErrPendingFull
		}
		if held, exists := c.pending[env.Sequence]; exists {
			if id.EventIDOf(held) != eid {
				return nil, l.forkChain(env.Device, frame)
			}
			return nil, nil
		}
		c.pending[env.Sequence] = frame
		return nil, nil
	}

	// env.Sequence == c.next: verify hash link, then apply and drain.
	if env.Sequence > 1 && (env.Previous == nil || *env.Previous != c.tip) {
		return nil, l.forkChain(env.Device, frame)
	}
	var applied []Applied
	a, err := l.apply(env, eid, frame, persist)
	if err != nil {
		return nil, err
	}
	applied = append(applied, a)

	for {
		nextFrame, ok := c.pending[c.next]
		if !ok {
			break
		}
		delete(c.pending, c.next)
		nextEnv, err := signal.Decode(nextFrame)
		if err != nil {
			return applied, err
		}
		if nextEnv.Previous == nil || *nextEnv.Previous != c.tip {
			return applied, l.forkChain(nextEnv.Device, nextFrame)
		}
		nid := id.EventIDOf(nextFrame)
		a, err := l.apply(nextEnv, nid, nextFrame, persist)
		if err != nil {
			return applied, err
		}
		applied = append(applied, a)
	}
	return applied, nil
}

func (l *Log) apply(env *signal.Envelope, eid id.EventID, frame []byte, persist bool) (Applied, error) {
	if persist && l.store != nil {
		if err := l.store.Append(frame); err != nil {
			return Applied{}, err
		}
	}
	c := l.chains[env.Device]
	c.next = env.Sequence + 1
	c.tip = eid
	l.frames[eid] = frame
	l.order = append(l.order, eid)
	return Applied{Env: env, ID: eid, Frame: frame}, nil
}

func (l *Log) forkChain(dev id.DeviceID, frame []byte) error {
	c := l.chains[dev]
	c.forked = true
	l.quarant[dev] = append(l.quarant[dev], frame)
	return ErrChainForked
}

// Get returns a stored frame by event id.
func (l *Log) Get(eid id.EventID) ([]byte, bool) {
	f, ok := l.frames[eid]
	return f, ok
}

// Len returns the number of applied events.
func (l *Log) Len() int { return len(l.order) }

// Replay iterates applied events in append order.
func (l *Log) Replay(fn func(Applied) error) error {
	for _, eid := range l.order {
		frame := l.frames[eid]
		env, err := signal.Decode(frame)
		if err != nil {
			return err
		}
		if err := fn(Applied{Env: env, ID: eid, Frame: frame}); err != nil {
			return err
		}
	}
	return nil
}

// Forked reports whether a device chain is quarantined.
func (l *Log) Forked(dev id.DeviceID) bool {
	c, ok := l.chains[dev]
	return ok && c.forked
}

// ---- Sync support (plan §17.2) ----

// ChainState is one device's summary entry.
type ChainState struct {
	Device          id.DeviceID
	ContiguousUntil uint64
	Forked          bool
}

// Summary lists all known chains, sorted by device id for determinism.
func (l *Log) Summary() []ChainState {
	out := make([]ChainState, 0, len(l.chains))
	for dev, c := range l.chains {
		out = append(out, ChainState{Device: dev, ContiguousUntil: c.next - 1, Forked: c.forked})
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].Device[:]) < string(out[j].Device[:])
	})
	return out
}

// FramesInRange returns the frames of one device chain for sequences
// [from, to], in order. Missing sequences end the range early.
func (l *Log) FramesInRange(dev id.DeviceID, from, to uint64) [][]byte {
	var out [][]byte
	// Walk the applied order; M0-scale terminals make a linear pass fine.
	for _, eid := range l.order {
		frame := l.frames[eid]
		env, err := signal.Decode(frame)
		if err != nil {
			continue
		}
		if env.Device == dev && env.Sequence >= from && env.Sequence <= to {
			out = append(out, frame)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := signal.Decode(out[i])
		b, _ := signal.Decode(out[j])
		return a.Sequence < b.Sequence
	})
	return out
}
