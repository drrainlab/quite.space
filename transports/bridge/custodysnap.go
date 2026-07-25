// Durability for the acknowledgement side (RB-0B hardening). The custody
// itself has been durable since TN-1 — the queue fsyncs before Enqueue
// returns. What was not durable was everything the bridge knew ABOUT that
// custody: which receipts still owed the sender an answer, and which frames
// had already been answered for.
//
// Two crash windows made that matter:
//
//   - Crash after the frame is fsynced but BEFORE the ACK goes out. The
//     sender retransmits; the bridge must answer with the same acceptance
//     time, not a fresh promise for an old frame. Solved by reading
//     EnqueuedAt back out of the queue, which never needed a second copy.
//   - Crash after the ACK goes out but before the send is recorded. The
//     receipt is simply sent again, and the node applies it idempotently.
//     This is at-least-once by choice: recording the send first would turn
//     a crash into a promise nobody ever hears.
package bridge

import (
	"os"
	"time"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

const (
	ackKeyTerminal = 1
	ackKeyFrames   = 2
	ackKeyKind     = 3
	ackKeyAt       = 4
	ackKeyExpires  = 5
	ackKeyNextTry  = 6
	ackKeyTries    = 7

	accKeyEvent = 1
	accKeyAt    = 2
)

func (b *Bridge) acksPath() string { return b.cfg.DataDir + "/custody-acks.snap" }

// saveAcks writes pending receipts and the acceptance memory.
func (b *Bridge) saveAcks() error {
	b.mu.Lock()
	pending := append([]pendingAck(nil), b.pendingAcks...)
	accepted := make(map[id.EventID]time.Time, len(b.accepted))
	for k, v := range b.accepted {
		accepted[k] = v
	}
	b.mu.Unlock()

	buf := codec.AppendArray(nil, len(pending))
	for _, p := range pending {
		buf = codec.AppendMap(buf, 7)
		buf = codec.AppendUint(buf, ackKeyTerminal)
		buf = codec.AppendBytes(buf, p.terminal[:])
		buf = codec.AppendUint(buf, ackKeyFrames)
		buf = codec.AppendArray(buf, len(p.frames))
		for _, f := range p.frames {
			buf = codec.AppendBytes(buf, f[:])
		}
		buf = codec.AppendUint(buf, ackKeyKind)
		buf = codec.AppendUint(buf, uint64(p.kind))
		buf = codec.AppendUint(buf, ackKeyAt)
		buf = codec.AppendUint(buf, uint64(max(p.at.Unix(), 0)))
		buf = codec.AppendUint(buf, ackKeyExpires)
		buf = codec.AppendUint(buf, p.expires)
		buf = codec.AppendUint(buf, ackKeyNextTry)
		var next int64
		if !p.nextTry.IsZero() {
			next = p.nextTry.Unix()
		}
		buf = codec.AppendUint(buf, uint64(max(next, 0)))
		buf = codec.AppendUint(buf, ackKeyTries)
		buf = codec.AppendUint(buf, uint64(p.tries))
	}

	// The acceptance memory follows as a second array in the same file.
	buf = codec.AppendArray(buf, len(accepted))
	for eid, at := range accepted {
		buf = codec.AppendMap(buf, 2)
		buf = codec.AppendUint(buf, accKeyEvent)
		buf = codec.AppendBytes(buf, eid[:])
		buf = codec.AppendUint(buf, accKeyAt)
		buf = codec.AppendUint(buf, uint64(max(at.Unix(), 0)))
	}

	tmp := b.acksPath() + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.acksPath())
}

// loadAcks restores pending receipts and the acceptance memory. A snapshot
// that cannot be read is dropped rather than fatal: the cost is a repeated
// or missing ACK, both of which the protocol already tolerates.
func (b *Bridge) loadAcks() {
	data, err := os.ReadFile(b.acksPath())
	if err != nil {
		return
	}
	d := codec.NewDecoder(data)
	n, err := d.ReadArray()
	if err != nil {
		return
	}
	var pending []pendingAck
	for range n {
		m, err := d.ReadMapHeader()
		if err != nil {
			return
		}
		var p pendingAck
		for {
			k, ok, err := m.Next()
			if err != nil || !ok {
				break
			}
			switch k {
			case ackKeyTerminal:
				raw, err := d.ReadBytes()
				if err != nil || len(raw) != id.Size {
					return
				}
				copy(p.terminal[:], raw)
			case ackKeyFrames:
				fn, err := d.ReadArray()
				if err != nil {
					return
				}
				for range fn {
					raw, err := d.ReadBytes()
					if err != nil || len(raw) != id.Size {
						return
					}
					var eid id.EventID
					copy(eid[:], raw)
					p.frames = append(p.frames, eid)
				}
			case ackKeyKind:
				v, err := d.ReadUint()
				if err != nil {
					return
				}
				p.kind = ackKind(v)
			case ackKeyAt:
				v, err := d.ReadUint()
				if err != nil {
					return
				}
				p.at = time.Unix(int64(v), 0)
			case ackKeyExpires:
				p.expires, err = d.ReadUint()
				if err != nil {
					return
				}
			case ackKeyNextTry:
				v, err := d.ReadUint()
				if err != nil {
					return
				}
				if v > 0 {
					p.nextTry = time.Unix(int64(v), 0)
				}
			case ackKeyTries:
				v, err := d.ReadUint()
				if err != nil {
					return
				}
				p.tries = int(v)
			default:
				if err := d.SkipItem(); err != nil {
					return
				}
			}
		}
		if len(p.frames) > 0 {
			pending = append(pending, p)
		}
	}

	accepted := map[id.EventID]time.Time{}
	if an, err := d.ReadArray(); err == nil {
		for range an {
			m, err := d.ReadMapHeader()
			if err != nil {
				break
			}
			var eid id.EventID
			var at time.Time
			for {
				k, ok, err := m.Next()
				if err != nil || !ok {
					break
				}
				switch k {
				case accKeyEvent:
					raw, err := d.ReadBytes()
					if err != nil || len(raw) != id.Size {
						return
					}
					copy(eid[:], raw)
				case accKeyAt:
					v, err := d.ReadUint()
					if err != nil {
						return
					}
					at = time.Unix(int64(v), 0)
				default:
					if err := d.SkipItem(); err != nil {
						return
					}
				}
			}
			if !at.IsZero() {
				accepted[eid] = at
			}
		}
	}

	b.mu.Lock()
	b.pendingAcks = pending
	for k, v := range accepted {
		if _, exists := b.accepted[k]; !exists {
			b.accepted[k] = v
		}
	}
	b.mu.Unlock()
}
