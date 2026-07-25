// Custody acknowledgement on the carrier (RB-0B). TN-B built the receipt
// format, the signing key, and the node-side pin check — and then nothing
// ever sent one. `AcceptUplink` had no callers outside its own test and
// `EncodeCustodyMessage` had none anywhere, so a node handing frames to a
// gateway got silence back and had no way to tell "carried" from "lost".
//
// Three rules make the ACK worth trusting:
//
//   - It is sent only after the frame is durably in custody. Enqueue
//     fsyncs before returning, so the promise cannot outlive a power cut.
//   - It is idempotent. A repeat of an already-accepted frame is answered
//     with the SAME acceptance time rather than treated as new: otherwise
//     one lost ACK would mean a node retransmitting until it gave up.
//   - It rides ahead of the data queue. A backed-up bridge must still be
//     able to say "I have it" — the whole point of the ACK is to let a
//     sender stop worrying, and a promise that arrives after the queue
//     drains would arrive too late to mean anything.
package bridge

import (
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
)

const (
	// acceptedMemory bounds how long a frame is remembered as accepted for
	// the purpose of repeating an ACK.
	acceptedMemory = 6 * time.Hour
	// acceptedCap bounds that memory in entries (a Pi, not a server).
	acceptedCap = 4096
	// pendingAckCap bounds unsent acknowledgements. Overflow means the
	// carrier has been unusable for a long time; the oldest are dropped and
	// the sender learns the truth by not hearing anything.
	pendingAckCap = 256
)

// ackKind distinguishes a custody claim from its withdrawal.
type ackKind uint8

const (
	ackHeld    ackKind = 0
	ackLapsed  ackKind = 1
)

// pendingAck is one acknowledgement waiting for airtime.
type pendingAck struct {
	terminal id.TerminalID
	frames   []id.EventID
	kind     ackKind
	at       time.Time
}

// rememberAccepted records that custody of a frame was taken at a moment,
// so a repeat can be answered identically.
func (b *Bridge) rememberAccepted(eid id.EventID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.accepted == nil {
		b.accepted = map[id.EventID]time.Time{}
	}
	if len(b.accepted) >= acceptedCap {
		for k, at := range b.accepted {
			if now.Sub(at) > acceptedMemory {
				delete(b.accepted, k)
			}
		}
		if len(b.accepted) >= acceptedCap {
			return // still full: forget the new one rather than an old promise
		}
	}
	b.accepted[eid] = now
}

// wasAccepted reports whether this frame was already taken into custody
// recently enough to answer for.
func (b *Bridge) wasAccepted(eid id.EventID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.accepted[eid]
	return ok
}

// queueAck schedules a custody acknowledgement for the next control pass.
func (b *Bridge) queueAck(term id.TerminalID, frames []id.EventID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pendingAcks) >= pendingAckCap {
		b.pendingAcks = b.pendingAcks[1:]
	}
	b.pendingAcks = append(b.pendingAcks, pendingAck{
		terminal: term, frames: frames, kind: ackHeld, at: now,
	})
}

// queueLapse schedules the withdrawal of a custody claim: the bridge said
// it held these frames and can no longer keep them to the promised time.
// Sending nothing would be worse than sending bad news — the sender would
// go on believing a gateway still had its message.
func (b *Bridge) queueLapse(term id.TerminalID, frames []id.EventID, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pendingAcks) >= pendingAckCap {
		b.pendingAcks = b.pendingAcks[1:]
	}
	b.pendingAcks = append(b.pendingAcks, pendingAck{
		terminal: term, frames: frames, kind: ackLapsed, at: now,
	})
}

// PushAcks puts pending custody acknowledgements on the carrier. Call it
// BEFORE PushRadio: the data queue must never be the reason a sender is
// left wondering whether its message was taken.
func (b *Bridge) PushAcks(now time.Time) int {
	if b.cfg.Radio == nil {
		return 0
	}
	b.mu.Lock()
	pending := b.pendingAcks
	b.pendingAcks = nil
	b.mu.Unlock()

	sent := 0
	var deferred []pendingAck
	for i, p := range pending {
		receipt := b.signReceipt(p)
		msg := kernelsync.EncodeCustodyMessage(p.terminal, receipt)
		if !b.airtime.Take(len(msg), now) {
			deferred = append(deferred, pending[i:]...)
			break
		}
		if err := b.sendOnRadio(msg); err != nil {
			deferred = append(deferred, pending[i:]...)
			break
		}
		b.mu.Lock()
		b.stats.CustodyAcks++
		b.mu.Unlock()
		sent++
	}
	if len(deferred) > 0 {
		b.mu.Lock()
		b.pendingAcks = append(deferred, b.pendingAcks...)
		if len(b.pendingAcks) > pendingAckCap {
			b.pendingAcks = b.pendingAcks[len(b.pendingAcks)-pendingAckCap:]
		}
		b.mu.Unlock()
	}
	return sent
}

// signReceipt builds the signed receipt for one pending acknowledgement.
// AcceptedAt comes from the remembered acceptance, so a repeat is byte-wise
// the same claim rather than a fresh promise with a later clock.
func (b *Bridge) signReceipt(p pendingAck) []byte {
	b.mu.Lock()
	acceptedAt := p.at
	for _, eid := range p.frames {
		if at, ok := b.accepted[eid]; ok && at.Before(acceptedAt) {
			acceptedAt = at
		}
	}
	b.mu.Unlock()
	r := &CustodyReceipt{
		FrameIDs:   p.frames,
		StoreID:    b.cfg.DataDir,
		AcceptedAt: uint64(acceptedAt.Unix()),
		Instance:   b.cfg.Instance,
	}
	if p.kind == ackLapsed {
		// A withdrawal: expiry in the past says plainly that custody is
		// over. A node must not read this as "held until then".
		r.Lapsed = true
		r.ExpiresAt = uint64(p.at.Unix())
	} else {
		r.ExpiresAt = uint64(acceptedAt.Add(b.cfg.QueueCaps.OperatorTTL).Unix())
	}
	return r.Sign(b.custodian)
}

// sendOnRadio fragments and writes one message to the carrier.
func (b *Bridge) sendOnRadio(msg []byte) error {
	b.mu.Lock()
	b.nextStream++
	stream := b.nextStream
	b.mu.Unlock()
	frags, err := kernelsync.FragmentStream(stream, msg,
		b.cfg.Radio.Capabilities().MaxPayload)
	if err != nil {
		return err
	}
	for _, f := range frags {
		if err := b.cfg.Radio.Send(f); err != nil {
			return err
		}
	}
	return nil
}

// releaseUnairable drops a record that can never cross this carrier —
// too large to broadcast politely, or too many fragments to survive a lossy
// link. Holding it would be pretending: it would sit in custody until it
// expired and nobody would ever be told. If its custody was acknowledged,
// the sender hears about the withdrawal.
func (b *Bridge) releaseUnairable(rec *routing.CustodyRecord, now time.Time) {
	if rec.Guaranteed {
		b.noteLapsed([]*routing.CustodyRecord{rec}, now)
	}
	b.queue.Ack(rec.ID)
	b.mu.Lock()
	b.stats.Unairable++
	b.mu.Unlock()
}

// noteLapsed turns swept guaranteed records into withdrawals.
func (b *Bridge) noteLapsed(lapsed []*routing.CustodyRecord, now time.Time) {
	byTerm := map[id.TerminalID][]id.EventID{}
	for _, rec := range lapsed {
		byTerm[rec.Destination] = append(byTerm[rec.Destination], id.EventIDOf(rec.Frame))
	}
	for term, ids := range byTerm {
		b.queueLapse(term, ids, now)
		b.mu.Lock()
		b.stats.CustodyLapsed += len(ids)
		b.mu.Unlock()
	}
}
