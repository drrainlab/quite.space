// Responsibility state machine (RB-1 step 2b). What a custody receipt is
// allowed to change about a message this device is still responsible for.
//
// The rule everything else follows from: a receipt may move responsibility
// only when it names the hand-off that is CURRENT. Anything older is
// recorded and ignored. Without that, an acknowledgement from an attempt
// that was abandoned minutes ago completes the attempt that replaced it,
// and the message is dropped by both sides — the sender believes a gateway
// has it, and the gateway that has it never promised anything.
//
//	retryable, attempt=A
//	  └─ Accepted(A,L1) ──────────→ in custody, attempt=A, lease=L1
//
//	in custody, attempt=A, lease=L1
//	  ├─ Lapsed(A,L1) / Expired(A,L1) ─→ retryable → next send mints B
//	  └─ ExpiresAt passes ─────────────→ retryable → next send mints B
//
//	attempt=B
//	  ├─ Accepted(A,L1)  late ──→ audit only
//	  ├─ Lapsed(A,L1)    late ──→ audit only
//	  └─ Accepted(B,L2) ────────→ in custody under L2
package node

import (
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
)

// ReceiptOutcome is what a receipt did to local responsibility. It exists
// so tests and diagnostics can assert on the DECISION rather than infer it
// from side effects.
type ReceiptOutcome uint8

const (
	// ReceiptIgnored: not about anything this device is tracking.
	ReceiptIgnored ReceiptOutcome = iota
	// ReceiptAudited: a real, verified receipt that names a hand-off which
	// is no longer current. Recorded; responsibility untouched.
	ReceiptAudited
	// ReceiptTookCustody: responsibility suspended until the horizon.
	ReceiptTookCustody
	// ReceiptRepeated: the same claim again for the current lease. Custody
	// was already recorded; nothing changes.
	ReceiptRepeated
	// ReceiptReturned: custody ended, responsibility is ours again.
	ReceiptReturned
	// ReceiptConflicted: a second gateway claims custody of a hand-off that
	// another lease already holds. The first promise stands.
	ReceiptConflicted
	// ReceiptUnbound: no attempt token. Decoded and logged, but it names no
	// hand-off, so it can neither suspend nor release responsibility.
	ReceiptUnbound
)

func (o ReceiptOutcome) String() string {
	switch o {
	case ReceiptAudited:
		return "audited"
	case ReceiptTookCustody:
		return "took_custody"
	case ReceiptRepeated:
		return "repeated"
	case ReceiptReturned:
		return "returned"
	case ReceiptConflicted:
		return "conflicted"
	case ReceiptUnbound:
		return "unbound"
	}
	return "ignored"
}

// applyReceiptToLedger runs the machine for one event. Caller holds r.mu.
//
// It deliberately takes a decoded, ALREADY SIGNATURE-CHECKED and
// pin-checked receipt: authenticity is a separate question from currency,
// and mixing them produced the bug where "the signature is good" was
// treated as "this is about the attempt I am running".
func (r *Runtime) applyReceiptToLedger(eid id.EventID, rec *bridge.CustodyReceipt,
	now time.Time) ReceiptOutcome {

	if r.ledger == nil {
		return ReceiptIgnored
	}
	in, ok := r.ledger.Get(eid)
	if !ok {
		return ReceiptIgnored
	}

	// A receipt that names no hand-off cannot be about the current one.
	// It may be older software, it may be a replay from a segment where
	// tokens were never used; either way it is not evidence about THIS
	// attempt, and wire compatibility must not be paid for with
	// correctness. It is decoded and recorded, and that is all.
	if len(rec.Attempt) == 0 {
		r.noteReceiptAudit(eid, rec, now, ReceiptUnbound)
		return ReceiptUnbound
	}
	if in.Attempt.Zero() || string(in.Attempt[:]) != string(rec.Attempt) {
		r.noteReceiptAudit(eid, rec, now, ReceiptAudited)
		return ReceiptAudited
	}

	if rec.Kind.Lapsed() {
		// Ending custody requires naming the lease as well. The attempt
		// alone is not enough: one attempt can be answered by more than one
		// gateway, and a withdrawal from a gateway we are not relying on
		// must not hand us back responsibility we already placed elsewhere.
		if in.Lease == "" || in.Lease != rec.Lease {
			r.noteReceiptAudit(eid, rec, now, ReceiptAudited)
			return ReceiptAudited
		}
		if in.State != IntentCustody {
			return ReceiptRepeated
		}
		_, _, err := r.ledger.Update(eid, now, func(cur *DeliveryIntent) bool {
			cur.State = IntentRetryable
			cur.Lease = ""
			cur.LeaseExpires = 0
			// The next send mints a new attempt: responsibility came back,
			// and that is precisely what starts a new epoch.
			cur.Attempt = AttemptID{}
			cur.NextAttemptAt = 0
			return true
		})
		if err != nil {
			return ReceiptIgnored
		}
		r.noteCustodyLapsed(eid, in.Space, rec.Instance)
		return ReceiptReturned
	}

	// Accepted.
	switch {
	case in.State == IntentCustody && in.Lease == rec.Lease:
		return ReceiptRepeated // idempotent
	case in.State == IntentCustody && in.Lease != rec.Lease:
		// Another gateway answering the same hand-off. In the beta exactly
		// one downlink gateway is active per segment, so this is either a
		// misconfiguration or a second custodian that also heard the frame.
		// Keeping the first promise is the conservative choice: switching
		// would abandon a lease we are already relying on, on the word of a
		// receipt we did not ask for.
		r.noteReceiptAudit(eid, rec, now, ReceiptConflicted)
		return ReceiptConflicted
	}
	_, _, err := r.ledger.Update(eid, now, func(cur *DeliveryIntent) bool {
		cur.State = IntentCustody
		cur.Lease = rec.Lease
		cur.LeaseExpires = int64(rec.ExpiresAt)
		if cur.Proof < claims.DeliveryAcceptedByRelay {
			cur.Proof = claims.DeliveryAcceptedByRelay
		}
		return true
	})
	if err != nil {
		return ReceiptIgnored
	}
	return ReceiptTookCustody
}

// ReceiptAudit is a verified receipt that did not move responsibility.
// Kept device-local for diagnostics — never emitted, never a claim.
type ReceiptAudit struct {
	Space    id.TerminalID
	Instance string
	Attempt  string
	Lease    string
	Kind     bridge.ReceiptKind
	Outcome  ReceiptOutcome
	At       time.Time
}

const maxReceiptAudits = 512

// noteReceiptAudit records a receipt that was real but not current. Caller
// holds r.mu. This is what makes a stale acknowledgement debuggable
// instead of invisible: silently dropping it is correct for the ledger and
// useless for anyone asking why a message is still pending.
func (r *Runtime) noteReceiptAudit(eid id.EventID, rec *bridge.CustodyReceipt,
	now time.Time, outcome ReceiptOutcome) {

	if r.receiptAudits == nil {
		r.receiptAudits = map[id.EventID][]ReceiptAudit{}
	}
	if len(r.receiptAudits) >= maxReceiptAudits {
		for k := range r.receiptAudits {
			delete(r.receiptAudits, k)
			break
		}
	}
	var space id.TerminalID
	if in, ok := r.ledger.Get(eid); ok {
		space = in.Space
	}
	entry := ReceiptAudit{
		Space:    space,
		Instance: rec.Instance,
		Attempt:  hexOf(rec.Attempt),
		Lease:    rec.Lease,
		Kind:     rec.Kind,
		Outcome:  outcome,
		At:       now,
	}
	list := r.receiptAudits[eid]
	if len(list) >= 8 {
		list = list[1:]
	}
	r.receiptAudits[eid] = append(list, entry)
}

// ReceiptAudits returns the stale receipts recorded for an event.
func (r *Runtime) ReceiptAudits(eid id.EventID) []ReceiptAudit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReceiptAudit(nil), r.receiptAudits[eid]...)
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// openAttempt returns the responsibility token that new sends for this
// space must carry, minting one if the space has no attempt in flight.
// Caller holds r.mu.
//
// The token is per RESPONSIBILITY EPOCH, not per packet and not per pass.
// It survives fragmentation and ordinary retries — losing a transport
// acknowledgement is not evidence that anything changed, so it must not
// start a new epoch. A new token is minted only when responsibility has
// actually come back: custody withdrawn, custody expired, or nothing handed
// over yet.
//
// The granularity is per-space rather than strictly per-event, and that is
// a deliberate limit worth naming. One frames message carries one token and
// a gateway answers a batch with one receipt, so a batch is the finest unit
// a hand-off can be identified at. Intents in one space are sent and
// acknowledged together, so they share an epoch; an intent already in
// custody is not due and is not re-sent, so it keeps the token it went out
// under until that custody ends.
//
// It is written and FSYNCED before the send that carries it. A crash in
// between must not leave the node minting a second token for the same
// epoch, because an acknowledgement already in flight would then name a
// hand-off the node no longer recognises.
func (r *Runtime) openAttempt(tid id.TerminalID, now time.Time) (AttemptID, bool) {
	if r.ledger == nil {
		return AttemptID{}, false
	}
	due := r.ledger.Due(now, 0)
	var token AttemptID
	var needs []id.EventID
	for _, in := range due {
		if in.Space != tid {
			continue
		}
		if !in.Attempt.Zero() {
			if token.Zero() {
				// An epoch is already open for this space: reuse it, so a
				// retry of an unacknowledged attempt keeps its identity.
				token = in.Attempt
			}
			continue
		}
		needs = append(needs, in.EventID)
	}
	if token.Zero() {
		if len(needs) == 0 {
			return AttemptID{}, false // nothing due for this space
		}
		fresh, err := NewAttemptID()
		if err != nil {
			return AttemptID{}, false
		}
		token = fresh
	}
	// Durable BEFORE the caller sends anything under it.
	for _, eid := range needs {
		if _, _, err := r.ledger.Update(eid, now, func(cur *DeliveryIntent) bool {
			if !cur.Attempt.Zero() {
				return false
			}
			cur.Attempt = token
			cur.AttemptNo++
			return true
		}); err != nil {
			return AttemptID{}, false
		}
	}
	return token, true
}

// markHandedToTransport records that these events left the machine under
// the space's open attempt. Bytes in an adapter are not custody and never
// terminal — the proof stops at handed_to_transport until a gateway signs
// for them. Caller holds r.mu.
func (r *Runtime) markHandedToTransport(ids []id.EventID, transport string,
	now time.Time) {

	if r.ledger == nil {
		return
	}
	for _, eid := range ids {
		if _, ok := r.ledger.Get(eid); !ok {
			continue
		}
		_, _, _ = r.ledger.Update(eid, now, func(cur *DeliveryIntent) bool {
			if cur.State == IntentCustody || cur.State == IntentSettled {
				return false // already covered by someone else's promise
			}
			cur.State = IntentInFlight
			cur.Transport = transport
			if cur.Proof < claims.DeliveryHandedToTransport {
				cur.Proof = claims.DeliveryHandedToTransport
			}
			return true
		})
	}
}

// trackOutbound starts tracking responsibility for an event this device
// authored. Idempotent, so replaying the log after a restart cannot
// multiply it or reset an attempt still in flight. Caller holds r.mu.
func (r *Runtime) trackOutbound(eid id.EventID, tid id.TerminalID, size int,
	now time.Time) {
	if r.ledger == nil {
		return
	}
	// A local-only space has no delivery to track: nothing carries its
	// events anywhere, so an intent here would sit unsettled forever and
	// the projection would offer a route the node will never take. Found
	// live — a copy shared into the assistant's space reported
	// "carrying it over lan" (AI-0, SHARE-2).
	if r.ks.Spaces[tid].LocalOnly {
		return
	}
	_, _ = r.ledger.Enqueue(eid, tid, size, now)
}
