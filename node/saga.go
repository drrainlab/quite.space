// The join saga's durable half (QL-0).
//
// ADR-012 invariant 7 already required this — "a per-request_id saga journal
// SURVIVES RESTARTS; reprocessing returns the stored result" — while the
// registry lived in memory and said so in a comment. The consequence was the
// worst shape a failure can take: a guest asked, the owner had no record,
// and nobody was told anything.
//
// Two rules govern everything in this file.
//
// ONE COMMIT. Admission adds a member, rotates the epoch and consumes a use.
// All three land in a single keystore write, because a power cut between two
// writes is exactly ADR-012 invariant 8 broken — "one request consumes at
// most one use and causes at most one rotation". Helpers here mutate the
// IN-MEMORY keystore; exactly one caller saves.
//
// LOCK ORDER: r.mu → r.passes.mu, never the reverse. The natural next edit —
// persisting from inside the passes.mu block — deadlocks.
package node

import (
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// passGrace keeps a record readable after its pass expires, matching the
// 24h response TTL: a re-delivery must still be answerable, and "why did
// this fail" must stay answerable for a day rather than becoming silence.
const passGrace = 24 * time.Hour

// maxHandledPerPass bounds the idempotency journal. Live passes allow at
// most 10 uses and a day of grace, so this holds hours of history, not
// years.
const maxHandledPerPass = 64

// updatePassesInKeystoreLocked rewrites the durable journal from the live
// registry. MEMORY ONLY — the caller decides when to save, because the whole
// point is that admission is one write. Caller holds r.mu.
func (r *Runtime) updatePassesInKeystoreLocked() {
	r.passes.mu.Lock()
	defer r.passes.mu.Unlock()

	now := uint64(time.Now().Unix())
	grace := uint64(passGrace / time.Second)
	out := make([]storage.PassRecord, 0, len(r.passes.byID))
	for pid, rec := range r.passes.byID {
		if rec.pass == nil {
			continue
		}
		// Prune what can no longer be acted on. A record past its grace
		// answers no re-delivery and explains nothing anyone is still
		// waiting on.
		if now > rec.pass.ExpiresAt+grace {
			delete(r.passes.byID, pid)
			continue
		}
		if len(rec.handled) > maxHandledPerPass {
			rec.trimHandled(maxHandledPerPass)
		}
		out = append(out, rec.toStorage())
	}
	r.ks.Passes = out
}

// updateJoinsInKeystoreLocked does the same for the guest's half. Caller
// holds r.mu.
func (r *Runtime) updateJoinsInKeystoreLocked() {
	r.passes.mu.Lock()
	defer r.passes.mu.Unlock()

	now := uint64(time.Now().Unix())
	out := make([]storage.JoinRecord, 0, len(r.joins))
	for key, at := range r.joins {
		if at.pass == nil {
			continue
		}
		// A guest keeps collecting past the pass's expiry: expiry forbids
		// a NEW request, never the completion of one already recorded.
		if now > at.collectUntil {
			delete(r.joins, key)
			continue
		}
		out = append(out, storage.JoinRecord{
			Space: at.space, PassFrame: at.pass.Frame(), Secret: at.secret,
			Request: at.requestID, Relay: at.relayAddr,
			State: string(at.state), StartedAt: at.startedAt,
			CollectUntil: at.collectUntil,
		})
	}
	r.ks.Joins = out
}

// commitSagaLocked is the ONE write. Everything that must be true together
// — membership, epochs, the use count, the request's fate — is already in
// the in-memory keystore when this runs. Caller holds r.mu.
func (r *Runtime) commitSagaLocked() error {
	r.updatePassesInKeystoreLocked()
	r.updateJoinsInKeystoreLocked()
	return r.saveKeystore()
}

// commitAdmissionLocked folds the epoch/member mutation into the same write
// as the saga, so a crash can never leave a rotated epoch with no record of
// the use that caused it. Caller holds r.mu.
func (r *Runtime) commitAdmissionLocked(tid id.TerminalID, sp *terminals.Space) error {
	if sp != nil {
		r.ks.Epochs[tid] = sp.ExportEpochs()
		if meta, ok := r.ks.Spaces[tid]; ok && meta.Owned {
			meta.Members = sp.Members()
			r.ks.Spaces[tid] = meta
		}
	}
	return r.commitSagaLocked()
}

// commitSaga takes r.mu itself, for callers outside an admission.
func (r *Runtime) commitSaga() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commitSagaLocked()
}

// ---- registry ↔ storage ----

func (rec *passRecord) toStorage() storage.PassRecord {
	out := storage.PassRecord{
		Space: rec.space, Frame: rec.pass.Frame(), Relay: rec.relay,
		Used: rec.used, Revoked: rec.revoked, Approval: rec.approval,
		Entries: append([]storage.EntryRecord(nil), rec.entries...),
	}
	for req, h := range rec.handled {
		out.Handled = append(out.Handled, storage.HandledRequest{
			Request: req, Outcome: h.outcome, At: h.at,
		})
	}
	return out
}

// trimHandled drops the oldest entries first: an old request is the least
// likely to be re-delivered, and the newest fates are the ones someone may
// still be waiting to hear.
func (rec *passRecord) trimHandled(keep int) {
	type kv struct {
		req [32]byte
		h   handledFate
	}
	all := make([]kv, 0, len(rec.handled))
	for req, h := range rec.handled {
		all = append(all, kv{req, h})
	}
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].h.at < all[j-1].h.at; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	for i := 0; i < len(all)-keep; i++ {
		delete(rec.handled, all[i].req)
	}
}

// restoreSagaLocked rebuilds both halves of the saga after Open, and — just
// as importantly — restores the INTENT TO ACT. A durable store whose doors
// nobody watches is the same bug wearing a different coat, so the pollers
// are restarted from what was loaded. Caller holds r.mu.
func (r *Runtime) restoreSagaLocked() {
	now := uint64(time.Now().Unix())
	grace := uint64(passGrace / time.Second)

	for _, pr := range r.ks.Passes {
		// A frame off disk is checked exactly like one off the wire: a
		// plain file is not a reason to trust bytes. A record that fails
		// is dropped, never quietly kept.
		p, err := terminals.DecodePassFrame(pr.Frame)
		if err != nil || p.Space != pr.Space {
			continue
		}
		if now > p.ExpiresAt+grace {
			continue
		}
		rec := &passRecord{
			pass: p, space: pr.Space, used: pr.Used, revoked: pr.Revoked,
			relay: pr.Relay, approval: pr.Approval,
			handled: map[[32]byte]handledFate{},
			entries: append([]storage.EntryRecord(nil), pr.Entries...),
		}
		for _, h := range pr.Handled {
			rec.handled[h.Request] = handledFate{outcome: h.Outcome, at: h.At}
		}
		r.passes.byID[p.PassID] = rec
	}

	for _, jr := range r.ks.Joins {
		p, err := terminals.DecodePassFrame(jr.PassFrame)
		if err != nil || p.Space != jr.Space {
			continue
		}
		if now > jr.CollectUntil {
			continue
		}
		if r.joins == nil {
			r.joins = map[string]*joinAttempt{}
		}
		at := &joinAttempt{
			pass: p, space: jr.Space, secret: jr.Secret,
			requestID: jr.Request, relayAddr: jr.Relay,
			state: JoinState(jr.State), startedAt: jr.StartedAt,
			collectUntil: jr.CollectUntil,
		}
		r.joins[hexShort(jr.Request[:])] = at
	}
}

// spaceOf is nil-safe sugar for the admission commit.
func spaceOf(st *spaceState) *terminals.Space {
	if st == nil {
		return nil
	}
	return st.space
}

// resumeJoinPolling restarts the guest's watch on every entrance it is
// still entitled to collect. Without it a restored record would be a note
// about a door nobody is standing at.
func (r *Runtime) resumeJoinPolling() {
	r.passes.mu.Lock()
	pending := make([]*joinAttempt, 0, len(r.joins))
	for _, at := range r.joins {
		if at.state == JoinWaiting {
			pending = append(pending, at)
		}
	}
	r.passes.mu.Unlock()
	for _, at := range pending {
		r.wg.Add(1)
		go r.pollJoinResponse(at)
	}
}

// setPassApproval records that this link asks a person to decide. It lives
// in the owner's record, never in the pass frame: signingBody is a fixed
// map, so a new signed field would invalidate every existing signature —
// and approval does not need signing, because the owner is the enforcer.
func (r *Runtime) setPassApproval(passIDShort, mode string) {
	r.passes.mu.Lock()
	for pid, rec := range r.passes.byID {
		if hexShort(pid[:]) == passIDShort {
			rec.approval = mode
			break
		}
	}
	r.passes.mu.Unlock()
	_ = r.commitSaga()
}
