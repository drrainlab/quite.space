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

	"github.com/drrainlab/quiet_places/kernel/routing"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
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
	ackExpired ackKind = 2
)

// receiptKindOf maps the internal kind onto the signed one. They are
// separate types so the wire enum can gain values without every internal
// switch silently accepting them.
func receiptKindOf(k ackKind) ReceiptKind {
	switch k {
	case ackLapsed:
		return ReceiptLapsed
	case ackExpired:
		return ReceiptExpired
	}
	return ReceiptAccepted
}

// pendingAck is one acknowledgement waiting for airtime.
type pendingAck struct {
	terminal id.TerminalID
	frames   []id.EventID
	kind     ackKind
	at       time.Time
	// expires is the custody horizon this receipt reports. For a
	// withdrawal it is the expiry the ORIGINAL claim promised, not the
	// moment of withdrawal: the two mechanisms are layered, and reporting
	// "now" would collapse them into one.
	expires uint64
	// attempt echoes the SENDER's responsibility token, read off the frames
	// message and stored with the custody record. Every receipt about this
	// custody carries it, including a withdrawal issued much later, because
	// only that token tells the sender which of its hand-offs is being
	// answered.
	attempt []byte
	// lease names the custody record inside this gateway.
	lease string
	// nextTry / tries drive repetition of a withdrawal.
	nextTry time.Time
	tries   int
}

// lapseRetry is how long a withdrawal waits before it is repeated, and
// lapseMaxTries how often. There is no acknowledgement of an
// acknowledgement — inventing one would be a whole new protocol
// conversation — so repetition is bounded by attempts alone.
//
// It is deliberately NOT bounded by the custody horizon. Almost every
// withdrawal is raised when custody expires, so a rule of "repeat only
// while the horizon is still ahead" would silence the message in exactly
// the case it exists for. And the horizon is the weaker signal of the two:
// evaluating ExpiresAt means trusting a clock, and the sender here is a
// node that has been off-grid on a radio segment, which is precisely the
// device whose clock cannot be trusted (the same reason RB-2 measures
// beacon freshness with a monotonic counter). The withdrawal is the
// clock-independent half; the expiry is the backstop for when it is lost.
const (
	lapseRetry    = 5 * time.Minute
	lapseMaxTries = 4
)

// acceptedAt answers "when did I take custody of this frame". The queue is
// asked first: EnqueuedAt was written and fsynced with the record itself,
// so it is the one answer that survives a crash between taking custody and
// acknowledging it. The in-memory map is only a fallback for frames already
// forwarded and released, and is persisted with the pending receipts.
func (b *Bridge) acceptedAt(eid id.EventID) (time.Time, bool) {
	if rec, ok := b.queue.Held(eid); ok {
		return time.Unix(rec.EnqueuedAt, 0), true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	at, ok := b.accepted[eid]
	return at, ok
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
	_, ok := b.acceptedAt(eid)
	return ok
}

// queueAck schedules a custody acknowledgement for the next control pass.
// Frames already waiting to be acknowledged are dropped: after a restart
// the obligation is restored from disk AND the sender retransmits, and
// putting the identical receipt on the air twice in one pass would spend
// airtime to say the same thing.
func (b *Bridge) queueAck(term id.TerminalID, frames []id.EventID,
	attempt []byte, lease string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	queued := map[id.EventID]bool{}
	for _, p := range b.pendingAcks {
		if p.kind != ackHeld || p.terminal != term {
			continue
		}
		for _, f := range p.frames {
			queued[f] = true
		}
	}
	fresh := frames[:0:0]
	for _, f := range frames {
		if !queued[f] {
			fresh = append(fresh, f)
		}
	}
	if len(fresh) == 0 {
		return
	}
	if len(b.pendingAcks) >= pendingAckCap {
		b.pendingAcks = b.pendingAcks[1:]
	}
	b.pendingAcks = append(b.pendingAcks, pendingAck{
		terminal: term, frames: fresh, kind: ackHeld, at: now,
		expires: uint64(now.Add(b.cfg.QueueCaps.OperatorTTL).Unix()),
		attempt: append([]byte(nil), attempt...), lease: lease,
	})
}

// queueLapse schedules the withdrawal of a custody claim: the bridge said
// it held these frames and can no longer keep them to the promised time.
// Sending nothing would be worse than sending bad news — the sender would
// go on believing a gateway still had its message.
func (b *Bridge) queueLapse(term id.TerminalID, frames []id.EventID,
	attempt []byte, lease string, expires uint64, kind ackKind, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pendingAcks) >= pendingAckCap {
		b.pendingAcks = b.pendingAcks[1:]
	}
	b.pendingAcks = append(b.pendingAcks, pendingAck{
		terminal: term, frames: frames, kind: kind, at: now,
		expires: expires,
		attempt: append([]byte(nil), attempt...), lease: lease,
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
	var keep []pendingAck
	for i, p := range pending {
		if !p.nextTry.IsZero() && now.Before(p.nextTry) {
			keep = append(keep, p)
			continue
		}
		receipt := b.signReceipt(p)
		msg := kernelsync.EncodeCustodyMessage(p.terminal, receipt)
		if !b.airtime.Take(len(msg), now) {
			keep = append(keep, pending[i:]...)
			break
		}
		if err := b.sendOnRadio(msg); err != nil {
			keep = append(keep, pending[i:]...)
			break
		}
		b.mu.Lock()
		b.stats.CustodyAcks++
		b.mu.Unlock()
		sent++

		// Every message that ENDS custody is repeated — withdrawn early or
		// run to its horizon, both leave a sender believing something false
		// if they are lost. Testing for ackLapsed alone silenced the expiry
		// case, which is the overwhelmingly common one.
		if p.kind != ackHeld {
			p.tries++
			p.nextTry = now.Add(lapseRetry)
			if p.tries < lapseMaxTries {
				keep = append(keep, p)
			}
		}
	}
	b.mu.Lock()
	b.pendingAcks = append(keep, b.pendingAcks...)
	if len(b.pendingAcks) > pendingAckCap {
		b.pendingAcks = b.pendingAcks[len(b.pendingAcks)-pendingAckCap:]
	}
	b.mu.Unlock()
	if sent > 0 {
		_ = b.saveAcks()
	}
	return sent
}

// signReceipt builds the signed receipt for one pending acknowledgement.
// AcceptedAt comes from the remembered acceptance, so a repeat is byte-wise
// the same claim rather than a fresh promise with a later clock.
func (b *Bridge) signReceipt(p pendingAck) []byte {
	acceptedAt := p.at
	for _, eid := range p.frames {
		if at, ok := b.acceptedAt(eid); ok && at.Before(acceptedAt) {
			acceptedAt = at
		}
	}
	// ExpiresAt is carried on EVERY receipt, withdrawal included, and always
	// reports the horizon the ORIGINAL claim promised. The two mechanisms
	// are layered on purpose: the withdrawal makes a sender retry SOONER,
	// while the expiry guarantees responsibility cannot hang forever even if
	// every withdrawal is lost. Encoding a withdrawal as "expiry in the
	// past" would collapse the layers and make "expired" and "withdrawn
	// early" indistinguishable — which is what the explicit flag is for.
	r := &CustodyReceipt{
		FrameIDs:   p.frames,
		StoreID:    b.cfg.DataDir,
		AcceptedAt: uint64(acceptedAt.Unix()),
		ExpiresAt:  p.expires,
		Instance:   b.cfg.Instance,
		Kind:       receiptKindOf(p.kind),
		Attempt:    p.attempt,
		Lease:      p.lease,
		// Where the frames came in. A node pins custodians per link domain,
		// so a receipt that did not say which boundary it speaks about
		// could not be checked against the right pin.
		IngressLink: string(b.cfg.RadioLink),
		LoopDomain:  string(b.cfg.RadioDomain),
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
		// Given up early, not run out of time.
		b.noteLapsed([]*routing.CustodyRecord{rec}, ackLapsed, now)
	}
	b.queue.Ack(rec.ID)
	b.mu.Lock()
	b.stats.Unairable++
	b.mu.Unlock()
}

// noteLapsed turns ended custody into signed messages the sender can act
// on. kind distinguishes the two ways custody ends: ackExpired when the
// promised horizon simply arrived, ackLapsed when the gateway gave up
// early. A sender treats both as "mine again", but they are different
// stories to tell a person and different inputs to a retry policy.
//
// Grouping is by (destination, ATTEMPT), never by destination alone. Two
// records for one terminal can belong to different hand-offs, and a
// withdrawal that named the wrong attempt would be ignored by the sender —
// correctly, and silently, which is the worst way to lose a message.
func (b *Bridge) noteLapsed(lapsed []*routing.CustodyRecord, kind ackKind, now time.Time) {
	type key struct {
		term    id.TerminalID
		attempt string
	}
	type group struct {
		ids     []id.EventID
		attempt []byte
		expires uint64
	}
	groups := map[key]*group{}
	for _, rec := range lapsed {
		k := key{rec.Destination, string(rec.Attempt)}
		g := groups[k]
		if g == nil {
			g = &group{attempt: rec.Attempt}
			groups[k] = g
		}
		g.ids = append(g.ids, id.EventIDOf(rec.Frame))
		// Report the horizon that was actually promised. A record with no
		// author expiry was held under the operator TTL from the moment it
		// was enqueued.
		exp := rec.ExpiresAt
		if exp == 0 {
			exp = uint64(time.Unix(rec.EnqueuedAt, 0).Add(b.cfg.QueueCaps.OperatorTTL).Unix())
		}
		if exp > g.expires {
			g.expires = exp
		}
	}
	for k, g := range groups {
		b.queueLapse(k.term, g.ids, g.attempt, leaseOf(g.ids), g.expires, kind, now)
		b.mu.Lock()
		b.stats.CustodyLapsed += len(g.ids)
		b.mu.Unlock()
	}
	if len(groups) > 0 {
		_ = b.saveAcks()
	}
}

// leaseOf names the custody this receipt is about, so repeats are
// recognisable as repeats and a diagnostic can point at one row in one
// store. It refines the attempt token; it never substitutes for it.
func leaseOf(ids []id.EventID) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0].Hex()[:16]
}
