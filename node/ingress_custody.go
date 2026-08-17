// The custody chain for destructively collected ingress (MD-0b).
//
//	transport custody  →  LOCAL DURABLE CUSTODY  →  journal custody
//
// Two phases, and the split between them is the invariant:
//
//	PHASE 1  persist the WHOLE response before judging any of it
//	PHASE 2  judge each held item, and release only what somebody else now owns
//
// PHASE 1 IS PER RESPONSE, NOT PER FRAME, and that is not tidiness. Collect
// returns [A B C] and the relay has already forgotten all three. Persisting A,
// judging A, appending A and only then reaching for B leaves a window in which
// a dying process loses B and C completely: gone from the relay, never on our
// disk. So custody of the entire answer is taken first, and judgement cannot
// start until it is.
//
// ADMIT IS PERMISSION TO OFFER, NOT PERMISSION TO DELETE. The hold is released
// only when the NEXT layer durably owns the bytes — never merely because
// admission said yes. If the journal's own append fails on I/O, the frame
// stays held: otherwise semantic admission would have succeeded while durable
// custody was handed to nobody at all.
//
// WHAT "THE JOURNAL OWNS IT" MEANS, MEASURED RATHER THAN ASSUMED. Only
// eventlog.apply persists (log.go:225 → store.Append); a frame buffered for a
// future sequence sits in an IN-MEMORY map (log.go:186) and a restart loses it.
// So Ingest returning nil is NOT proof of durable ownership — duplicate and
// buffered are the same nil — and the question is put to the log directly:
// Log.Has(EventID) is true exactly for frames that reached apply, which
// persists before it records. Anything else stays ours.
//
// That keeps the two mechanisms from becoming two retry machines waiting on
// each other, because the hold takes on no ordering duty whatsoever: it does
// not know what a predecessor is, it schedules nothing, it merely declines to
// forget bytes nobody else has written down. Ordering stays the log's business
// alone — classify never answers Hold for a missing predecessor, and neither
// does this file.
package node

import (
	"errors"
	"fmt"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// ingressHoldTarget is the PRE-COLLECT threshold for held items, not an
// on-disk maximum (see storage.IngressHold). Sized against the per-item
// ceiling the node already enforces, maxRelayItem.
const ingressHoldTarget = 128

// maxIngressRefusals bounds the local diagnostic record (diagnostics, not a
// log), in the shape maxCustodyLapses already uses.
const maxIngressRefusals = 256

// ErrIngressCustodyLost is the honest name for the one hole this design cannot
// close locally: the relay destructively handed the bytes over and THEN our
// own disk refused to keep them (ENOSPC, I/O error). At that point the item
// exists nowhere — it cannot be put back, because putting it back is a
// protocol change (lease-and-acknowledge) that ADR-016 deliberately rules out.
//
// So this is not an ordinary frame failure and must never be reported as one.
// It is a CATASTROPHIC LOCAL FAILURE, and it halts further destructive
// collection on this node: a machine that keeps draining mailboxes it cannot
// store is converting other people's messages into nothing, quietly, one tick
// at a time.
var ErrIngressCustodyLost = errors.New("node: ingress custody lost — bytes were taken destructively and could not be stored")

// IngressRefusal is one diagnostic about material that was let go. It is
// produced BEFORE the bytes are deleted and deliberately does not contain
// them: enough to say what was refused and why, without keeping the thing.
type IngressRefusal struct {
	Hold   storage.HoldID
	Space  id.TerminalID
	Reason string
	Detail string
	Source storage.IngressSource
	At     time.Time
}

// ingressHold opens the hold once, lazily.
//
// Lazily rather than at node.Open on purpose: a node whose hold cannot be
// opened must still start, so a person can read their own history and see the
// diagnostic — but it must NOT collect destructively, and returning the error
// here is what stops that.
func (r *Runtime) ingressHold() (*storage.IngressHold, error) {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	if r.custodyLost {
		return nil, ErrIngressCustodyLost
	}
	if r.hold != nil {
		return r.hold, nil
	}
	h, err := r.root.OpenIngressHold(ingressHoldTarget)
	if err != nil {
		return nil, err
	}
	r.hold = h
	return h, nil
}

// noteCustodyLost latches the catastrophic case. One-way by design: nothing
// short of an operator's attention should let a node that has already
// destroyed ingress go back to collecting more.
func (r *Runtime) noteCustodyLost() {
	r.holdMu.Lock()
	r.custodyLost = true
	r.holdMu.Unlock()
}

// IngressCustodyLost reports the latch, so a caller about to drain a mailbox
// can decline and say why.
func (r *Runtime) IngressCustodyLost() bool {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	return r.custodyLost
}

// noteIngressRefusal records why material was let go, before it goes.
func (r *Runtime) noteIngressRefusal(ref IngressRefusal) {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	ref.At = time.Now()
	if len(r.ingressRefusals) >= maxIngressRefusals {
		r.ingressRefusals = r.ingressRefusals[1:]
	}
	r.ingressRefusals = append(r.ingressRefusals, ref)
}

// IngressRefusals returns the local diagnostic record, oldest first.
func (r *Runtime) IngressRefusals() []IngressRefusal {
	r.holdMu.Lock()
	defer r.holdMu.Unlock()
	return append([]IngressRefusal(nil), r.ingressRefusals...)
}

// takeIngressCustody is PHASE 1: it persists every item of one destructive
// response before returning, and returns them for judgement.
//
// A Put failure aborts the batch and latches ErrIngressCustodyLost. Items
// already persisted stay held — they are safe, and a later pass can judge
// them — but nothing more is drained.
func (r *Runtime) takeIngressCustody(items [][]byte, src storage.IngressSource) ([]storage.HeldIngress, error) {
	if len(items) == 0 {
		return nil, nil
	}
	hold, err := r.ingressHold()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	meta := storage.HeldIngressMeta{ReceivedAt: now, Source: src}
	held := make([]storage.HeldIngress, 0, len(items))
	for _, item := range items {
		hid, err := hold.Put(item, meta)
		if err != nil {
			r.noteCustodyLost()
			return held, fmt.Errorf("%w: %v", ErrIngressCustodyLost, err)
		}
		held = append(held, storage.HeldIngress{ID: hid, Raw: item, Meta: meta})
	}
	return held, nil
}

// releaseIngress is the delete half, and it is only ever called once the next
// layer owns the bytes or nothing ever will.
func (r *Runtime) releaseIngress(hid storage.HoldID) {
	hold, err := r.ingressHold()
	if err != nil {
		return // nothing to release from; bytes are kept, never lost
	}
	if err := hold.Delete(hid); err != nil {
		// A hold that will not delete is a disk problem, not a custody
		// problem: the bytes are safe and the journal already owns them, so
		// the worst outcome is one harmless duplicate offer after a restart.
		r.noteIngressRefusal(IngressRefusal{Hold: hid, Reason: "hold_delete_failed", Detail: err.Error()})
	}
}

// ---- reconsideration (MD-0b step 4) ----
//
// THE RULE, in one line: hook reconsideration to admission state that was
// SUCCESSFULLY APPLIED, never to the command that attempted to change it.
//
// Binding it to an authoring function like RevisePolicy would produce a
// silent asymmetry, because the same state change has two entirely different
// call paths:
//
//	LOCAL   RevisePolicy → apply → (callback here) → reconsider ✅
//	REMOTE  revision arrives over a relay → eventlog → projection
//	        updated → RevisePolicy was NEVER called → no reconsider ❌
//
// The remote path is the one that matters, since it is exactly the reorder
// that puts a message ahead of the control event authorising it. So the hook
// lives at OnAbsorb — the funnel that sees local emits AND synced frames, and
// only for frames that reached apply, which persists first.
//
// ONE NAME FOR THE WHOLE CLASS. Rather than a certificate callback, a policy
// callback, a membership callback and a freeze callback with slightly
// different semantics, everything says the same thing:
// admissionStateChanged(). No versions, no dependency graph, no index — just
// "something admission depends on is now different".

// admissionRelevantSchema reports whether an APPLIED event can change an
// admission verdict. Identity and space authorisation, nothing else: this is
// deliberately a small list of things that gate whether bytes may speak, not
// a list of interesting events.
func admissionRelevantSchema(schema string) bool {
	switch schema {
	case schemas.DeviceCertified, schemas.DeviceRevoked:
		return true // identity state
	case schemas.ManifestUpdated:
		return true // policy, curators, freeze/unfreeze all ride the manifest
	case schemas.MemberJoined, schemas.MemberLeft, schemas.MemberAdded:
		return true // space authorisation state
	}
	return false
}

// admissionStateChanged is the single entry point: something admission
// depends on has been applied. It never blocks and never runs the pass
// inline, because callers hold r.mu and the pass needs it.
func (r *Runtime) admissionStateChanged() {
	r.holdMu.Lock()
	r.reconsiderDirty = true
	if r.reconsiderRunning || !r.ingressArmed {
		// Not armed yet means we are still inside Open: the startup pass will
		// see everything at once, against a fully reconstructed world.
		r.holdMu.Unlock()
		return
	}
	r.reconsiderRunning = true
	r.holdMu.Unlock()
	go r.reconsiderLoop()
}

// reconsiderLoop drains the coalesced request. A held frame may itself be a
// control event whose admission changes admission state again, so this is a
// loop with a dirty flag rather than a recursive call out of a reducer.
func (r *Runtime) reconsiderLoop() {
	for {
		select {
		case <-r.stop:
			r.holdMu.Lock()
			r.reconsiderRunning = false
			r.holdMu.Unlock()
			return
		default:
		}
		r.holdMu.Lock()
		if !r.reconsiderDirty {
			r.reconsiderRunning = false
			r.holdMu.Unlock()
			return
		}
		r.reconsiderDirty = false
		r.holdMu.Unlock()
		r.reconsiderHeldIngress()
	}
}

// reconsiderHeldIngress re-judges everything in the hold: a full scan of a
// bounded store, with no index by design. The bound is small and a wrong
// index is a way to miss a frame forever.
func (r *Runtime) reconsiderHeldIngress() {
	hold, err := r.ingressHold()
	if err != nil {
		return
	}
	items, err := hold.List()
	if err != nil {
		// Corrupt or unreadable: fail closed and keep everything. Nothing
		// here deletes on a read error.
		r.noteIngressRefusal(IngressRefusal{Reason: "hold_unreadable", Detail: err.Error()})
		return
	}
	released := false
	for _, it := range items {
		// nil client: this is a REPLAY, so no media want is answered again —
		// the wants were answered when the bundle first arrived.
		if _, release := r.applyHeldRelayItem(nil, it); release {
			r.releaseIngress(it.ID)
			released = true
		}
	}
	r.notePassProgress(released)
}

// armIngressReconsider is called once at the end of Open, when identity,
// membership and policy projections are all rebuilt. It runs the startup pass
// — mandatory crash semantics: a prerequisite applied BEFORE the crash will
// never arrive again as an event, because it is already in the log, so
// without this the held frame waits for a trigger that cannot come.
//
// In a goroutine because Open holds r.mu throughout and the pass needs it;
// the goroutine therefore begins the moment Open releases it, which is also
// the moment the world it must see is complete.
func (r *Runtime) armIngressReconsider() {
	r.holdMu.Lock()
	r.ingressArmed = true
	r.reconsiderDirty = true
	r.reconsiderRunning = true
	done := r.startupReconsidered
	r.holdMu.Unlock()
	go func() {
		r.reconsiderLoop()
		close(done)
	}()
}

// ---- backpressure (MD-0b step 5) ----

// ErrIngressBackpressure means the node declined to DRAIN, not that anything
// was wrong with what is waiting. The relay still holds it: an uncollected
// item is the relay's, and that is the whole point of refusing before the
// destructive call rather than after it.
var ErrIngressBackpressure = errors.New("node: ingress admission backpressure — the hold has no room to keep what a drain would take")

// guardedCollect wraps a destructive drain with the capacity question, which
// must be asked BEFORE the bytes move and never after.
//
// THE DEADLOCK THIS AVOIDS, and it is not hypothetical. Certificates and
// policy revisions travel in the SAME mailbox as ordinary frames — the relay
// cannot be asked for "control only", it holds opaque items addressed by
// capability (ADR-016). So a node that stops collecting the instant its hold
// is full can be waiting for a release that is sitting on the relay behind
// the very drain it refuses to make.
//
// The rule that resolves it: when there is no room, ONE more drain is still
// allowed if the last judging pass RELEASED something. Progress earns the
// overshoot, which is bounded by a single reply (CollectMaxBytes) and matched
// by the releases that justified it. A pass that frees nothing earns nothing,
// so a stuck node stops growing instead of draining mailboxes it cannot keep.
func (r *Runtime) guardedCollect(collect func([][]byte) ([][]byte, error)) func([][]byte) ([][]byte, error) {
	return func(caps [][]byte) ([][]byte, error) {
		hold, err := r.ingressHold()
		if err != nil {
			return nil, err // custody unavailable: never drain destructively
		}
		if hold.RemainingItems() > 0 {
			return collect(caps)
		}
		r.holdMu.Lock()
		earned := r.lastPassReleased
		r.holdMu.Unlock()
		if earned {
			return collect(caps)
		}
		r.noteIngressRefusal(IngressRefusal{
			Reason: "ingress_admission_backpressure",
			Detail: fmt.Sprintf("hold at %d/%d items, last pass released nothing",
				hold.Count(), hold.TargetItems()),
		})
		return nil, ErrIngressBackpressure
	}
}

// notePassProgress records whether a judging pass handed anything on. It is
// what earns the next overshoot, so it is set from every path that judges.
func (r *Runtime) notePassProgress(released bool) {
	r.holdMu.Lock()
	r.lastPassReleased = released
	r.holdMu.Unlock()
}

// learnBundleProofs is decision C's pre-pass: before any frame of a batch is
// judged, the batch's own certificates and revocations become trust. Without
// it a batch whose data precedes its proof in slice order cannot converge in
// one pass — the data is refused, the proof is learned a moment too late, and
// whether that costs an extra cycle or a deadlock depends on who retries.
// observe() verifies the ROOT signature before anything becomes trust; the
// frame signature is checked first so a forged envelope cannot even present
// a payload.
func (r *Runtime) learnBundleProofs(frames [][]byte) {
	for _, f := range frames {
		env, err := signal.Decode(f)
		if err != nil {
			continue
		}
		switch env.Schema {
		case schemas.DeviceCertified, schemas.DeviceRevoked:
		default:
			continue
		}
		if signal.VerifyFrame(f, env) != nil {
			continue
		}
		r.ident.observe(env)
	}
}

// frameCustody is what happened to ONE frame inside a held item.
type frameCustody int

const (
	// custodyJournal — the log durably owns it. Releasable.
	custodyJournal frameCustody = iota
	// custodyRefusedForGood — no future state admits it. Releasable, with the
	// diagnostic produced first.
	custodyRefusedForGood
	// custodyStillOurs — nobody else owns it yet: awaiting a certificate,
	// awaiting its predecessor, or the journal's own write failed. Keep.
	custodyStillOurs
)

// judgeFrame offers one frame to the journal and reports who owns it after.
//
// It decodes and verifies BEFORE offering, rather than reading meaning out of
// Ingest's error afterwards. Two reasons: admission needs the envelope anyway,
// and the log's decode failures arrive as opaque errors with no sentinel to
// match — inferring "permanently bad" from an unrecognised error is how a
// transient failure gets somebody's message deleted.
//
// Caller holds r.mu.
func (r *Runtime) judgeFrame(st *spaceState, tid id.TerminalID, hid storage.HoldID, frame []byte) (frameCustody, int) {
	env, err := signal.Decode(frame)
	if err != nil {
		r.noteIngressRefusal(IngressRefusal{Hold: hid, Space: tid, Reason: "malformed", Detail: err.Error()})
		return custodyRefusedForGood, 0
	}
	if err := signal.VerifyFrame(frame, env); err != nil {
		r.noteIngressRefusal(IngressRefusal{Hold: hid, Space: tid, Reason: "invalid_signature", Detail: err.Error()})
		return custodyRefusedForGood, 0
	}
	// THE GATE IS CONSULTED ONLY WHEN IT IS SATISFIABLE. Off, every frame is
	// offered exactly as before this file existed, so custody is closed for
	// all traffic without enforcement riding in through a side door — MD-0
	// measured what happens when the gate precedes what makes it satisfiable.
	if r.identityGate {
		switch res := r.ident.classify(env); res.Verdict {
		case Hold:
			return custodyStillOurs, 0
		case Reject:
			r.noteIngressRefusal(IngressRefusal{
				Hold: hid, Space: tid, Reason: res.Reason.String(), Detail: "identity admission",
			})
			return custodyRefusedForGood, 0
		}
	}
	eid := id.EventIDOf(frame)
	if st.space.Log.Has(eid) {
		return custodyJournal, 0 // already durable before we offered it
	}
	as, ingestErr := st.space.Log.Ingest(frame)
	applied := 0
	for _, a := range as {
		st.space.AttachSyncApply(a)
		applied++
	}
	if st.space.Log.Has(eid) {
		return custodyJournal, applied
	}
	if ingestErr == nil {
		// Accepted but NOT durable: buffered for a future sequence, in
		// memory. Ordering is the log's business; not forgetting is ours.
		return custodyStillOurs, applied
	}
	if errors.Is(ingestErr, eventlog.ErrWrongTerminal) {
		// Addressed to another space. No certificate and no predecessor
		// changes that.
		r.noteIngressRefusal(IngressRefusal{Hold: hid, Space: tid, Reason: "wrong_space"})
		return custodyRefusedForGood, applied
	}
	if errors.Is(ingestErr, eventlog.ErrChainForked) {
		// A FORK IS TERMINAL — but NOT because the journal took custody, and
		// that distinction had to be measured. The quarantine the log keeps is
		// an in-memory map (log.go:65), lost on restart exactly like pending,
		// so calling this a hand-off of custody would be a fiction that
		// deletes bytes nobody durably holds.
		//
		// It is terminal on the other axis instead: `c.forked` is set and
		// never cleared anywhere in the tree, and a forked chain refuses every
		// later frame from that device. The conflicting slot is filled by a
		// PERSISTED frame, so a replay after any restart forks again on the
		// same bytes. No future control event changes that verdict — which is
		// the criterion, rather than the shape of the error.
		r.noteIngressRefusal(IngressRefusal{Hold: hid, Space: tid, Reason: "chain_forked"})
		return custodyRefusedForGood, applied
	}
	if holdClassRefusal(ingestErr) {
		// HOLD-CLASS IS TRANSIENT, measured rather than reasoned about
		// (ingress_policy_probe_test.go): the authorising revision admits the
		// same bytes, the lifted freeze admits the same bytes, the arriving
		// certificate admits the same bytes. Deleting here would recreate
		// TR-0 one layer up. The right question is never "does this error
		// look permanent" but "can one more valid control event change the
		// verdict on these exact bytes" — and here it can. The
		// denial-of-service worry is answered by capacity, never by
		// converting a temporary unknown into a permanent loss.
		return custodyStillOurs, applied
	}
	// Anything unrecognised — ErrPendingFull, an append that failed on I/O —
	// is TRANSIENT until proven otherwise. Keeping bytes costs disk; guessing
	// costs a message.
	return custodyStillOurs, applied
}
