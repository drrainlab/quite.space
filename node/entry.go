// The door queue (QL-2): people waiting for a person to decide.
//
// The interface has claimed since UI-2 that entering "sends a request they
// must accept". It did not — a valid request was admitted automatically and
// unattended. This is where that sentence becomes true for links that ask
// for it, while an open link keeps admitting exactly as before.
package node

import (
	"errors"
	"net/http"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// maxPendingPerPass bounds the queue. Without it the door is a
// memory-growth vector for anyone holding the words, and a host would be
// asked to read a list somebody else decided the length of.
const maxPendingPerPass = 20

// maxRecentEntries keeps a short tail of decided entries. Accepting
// somebody currently leaves no trace anywhere else — the canonical
// membership event has no reducer and is rendered nowhere — so deleting the
// row on accept would make the action invisible the moment it succeeded.
const maxRecentEntries = 10

// recentEntryTTL bounds that tail in time as well as count.
const recentEntryTTL = 24 * time.Hour

// parkAtTheDoor records somebody waiting and tells them a person has to
// look. Caller holds neither lock.
func (r *Runtime) parkAtTheDoor(client *relay.Client, rec *passRecord,
	req *terminals.JoinRequest, now uint64) {

	r.passes.mu.Lock()
	// Already waiting? Say so again rather than queueing them twice: a
	// re-delivered request is the same knock, not a second one.
	known := false
	pending := 0
	for _, e := range rec.entries {
		if e.Request == req.RequestID {
			known = true
		}
		if e.State == storage.EntryPending {
			pending++
		}
	}
	full := !known && pending >= maxPendingPerPass
	if !known && !full {
		rec.entries = append(rec.entries, storage.EntryRecord{
			Request: req.RequestID, Device: req.Device, Xpub: req.DeviceXpub,
			Name: req.DisplayName, AskedAt: now, State: storage.EntryPending,
		})
	}
	rendezvous := rec.pass.Rendezvous
	space := rec.space
	r.passes.mu.Unlock()

	if full {
		// Refuse plainly rather than growing without limit. This is not a
		// judgement of the person — say what actually happened.
		r.sealDecision(client, space, rendezvous, req.RequestID, req.DeviceXpub,
			terminals.DecisionDeclined, "too many people are waiting at this link")
		return
	}
	_ = r.commitSaga()
	r.sealDecision(client, space, rendezvous, req.RequestID, req.DeviceXpub,
		terminals.DecisionReceived, "")
}

// sealDecision delivers the host's answer to the requester's dead-drop.
func (r *Runtime) sealDecision(client *relay.Client, space id.TerminalID,
	rendezvous [16]byte, reqID [32]byte, xpub [32]byte,
	state terminals.DecisionState, reason string) {

	sealed, err := terminals.BuildDecision(space, xpub, &terminals.Decision{
		RequestID: reqID, State: state, Reason: reason,
	})
	if err != nil {
		return
	}
	now := uint64(time.Now().Unix())
	_, _ = client.Put(terminals.RespHint(rendezvous, reqID), now+86400, sealed)
}

// EntryRequest is one person at the door, as the owner's interface sees it.
type EntryRequest struct {
	RequestID string
	Space     id.TerminalID
	// Name is what the requesting device calls itself — self-declared and
	// shown as such, never used for any decision.
	Name      string
	Device    string
	AskedAt   uint64
	DecidedAt uint64
	State     string
	PassID    string
	PassUntil uint64
}

// EntryRequests lists who is waiting, and who was recently let in or turned
// away — the tail exists because accepting somebody otherwise leaves no
// trace at all.
func (r *Runtime) EntryRequests() []EntryRequest {
	r.passes.mu.Lock()
	defer r.passes.mu.Unlock()
	var out []EntryRequest
	for pid, rec := range r.passes.byID {
		for _, e := range rec.entries {
			out = append(out, EntryRequest{
				RequestID: hexShort(e.Request[:]), Space: rec.space,
				Name: e.Name, Device: hexShort(e.Device[:]),
				AskedAt: e.AskedAt, DecidedAt: e.DecidedAt,
				State: entryStateName(e.State), PassID: hexShort(pid[:]),
				PassUntil: rec.pass.ExpiresAt,
			})
		}
	}
	return out
}

func entryStateName(s storage.EntryState) string {
	switch s {
	case storage.EntryPending:
		return "pending"
	case storage.EntryAdmitted:
		return "admitted"
	case storage.EntryDeclined:
		return "declined"
	case storage.EntryExpired:
		return "expired"
	}
	return "unknown"
}

// DecideEntry admits or turns away somebody waiting. A use is consumed HERE
// and not when they knocked, so a refusal costs the host nothing — and a
// pending request can still fail honestly if the pass ran out while it was
// waiting.
func (r *Runtime) DecideEntry(reqIDShort string, admit bool, reason string) error {
	addr := r.GetSettings().Relay
	if addr == "" {
		return errors.New("node: deciding needs the relay the link was made on")
	}
	r.passes.mu.Lock()
	var (
		rec   *passRecord
		entry *storage.EntryRecord
	)
	for _, cand := range r.passes.byID {
		for i := range cand.entries {
			if hexShort(cand.entries[i].Request[:]) == reqIDShort {
				rec, entry = cand, &cand.entries[i]
			}
		}
	}
	if rec == nil {
		r.passes.mu.Unlock()
		return errors.New("node: nobody is waiting under that request")
	}
	if entry.State != storage.EntryPending {
		r.passes.mu.Unlock()
		return errors.New("node: that request was already decided")
	}
	now := uint64(time.Now().Unix())
	space, rendezvous := rec.space, rec.pass.Rendezvous
	xpub, reqID := entry.Xpub, entry.Request
	device := entry.Device

	// Re-validate at the moment of the decision, not at the knock: time
	// passed while a person thought about it.
	var refusal string
	switch {
	case rec.revoked:
		refusal = "the link was withdrawn"
	case now >= rec.pass.ExpiresAt:
		refusal = "the pass ran out while this was waiting"
	case admit && rec.used >= rec.pass.MaxUses:
		refusal = "this link has no places left"
	}
	r.passes.mu.Unlock()

	client, err := relay.DialClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	if refusal != "" {
		r.settleEntry(reqID, storage.EntryExpired, storage.OutcomeExpiredWaiting, now)
		r.sealDecision(client, space, rendezvous, reqID, xpub,
			terminals.DecisionUnavailable, refusal)
		return nil
	}
	if !admit {
		if reason == "" {
			reason = "not this time"
		}
		r.settleEntry(reqID, storage.EntryDeclined, storage.OutcomeDeclinedByHost, now)
		r.sealDecision(client, space, rendezvous, reqID, xpub,
			terminals.DecisionDeclined, reason)
		return nil
	}
	return r.admitFromDoor(client, rec, reqID, device, xpub, now)
}

// settleEntry moves an entry out of pending, records its exact fate, and
// prunes the tail. The Outcome is the canonical one — a re-delivery is
// answered from it, so it must never be a generic "refused".
func (r *Runtime) settleEntry(reqID [32]byte, state storage.EntryState,
	outcome storage.EntryOutcome, now uint64) {

	r.passes.mu.Lock()
	for _, rec := range r.passes.byID {
		for i := range rec.entries {
			if rec.entries[i].Request != reqID {
				continue
			}
			rec.entries[i].State = state
			rec.entries[i].Outcome = outcome
			rec.entries[i].DecidedAt = now
			rec.handled[reqID] = handledFate{outcome: outcome, at: now}
			pruneEntries(rec, now)
		}
	}
	r.passes.mu.Unlock()
	_ = r.commitSaga()
}

// pruneEntries keeps every pending entry and a short tail of decided ones.
// Caller holds passes.mu.
func pruneEntries(rec *passRecord, now uint64) {
	ttl := uint64(recentEntryTTL / time.Second)
	kept := make([]storage.EntryRecord, 0, len(rec.entries))
	decided := 0
	for i := len(rec.entries) - 1; i >= 0; i-- {
		e := rec.entries[i]
		if e.State == storage.EntryPending {
			kept = append(kept, e)
			continue
		}
		if decided >= maxRecentEntries || (e.DecidedAt > 0 && now > e.DecidedAt+ttl) {
			continue
		}
		decided++
		kept = append(kept, e)
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	rec.entries = kept
}

// admitFromDoor runs the admission a host just approved: membership, epoch
// rotation, the use, and the request's fate — in ONE keystore write, the
// same rule the automatic path follows.
func (r *Runtime) admitFromDoor(client *relay.Client, rec *passRecord,
	reqID [32]byte, device id.DeviceID, xpub [32]byte, now uint64) error {

	r.passes.mu.Lock()
	space, rendezvous := rec.space, rec.pass.Rendezvous
	var name string
	for _, e := range rec.entries {
		if e.Request == reqID {
			name = e.Name
		}
	}
	rec.used++
	for i := range rec.entries {
		if rec.entries[i].Request == reqID {
			rec.entries[i].State = storage.EntryAdmitted
			rec.entries[i].Outcome = storage.OutcomeGranted
			rec.entries[i].DecidedAt = now
		}
	}
	rec.handled[reqID] = handledFate{outcome: storage.OutcomeGranted, at: now}
	pruneEntries(rec, now)
	r.passes.mu.Unlock()

	r.mu.Lock()
	st := r.spaces[space]
	var resp []byte
	if st != nil {
		epochN, epochKey, mf, err := st.space.AcceptIntoSpace(r.Self,
			device, xpub, name, r.Principal.ID, now)
		if err == nil {
			acc := &terminals.Accepted{RequestID: reqID,
				ManifestFrame: mf, EpochN: epochN, EpochKey: epochKey}
			if _, ch := st.space.Character(); ch.Memory != "private_history" {
				acc.History = st.space.EpochHistory()
			}
			resp, _ = terminals.BuildAccepted(space, xpub, acc)
		}
	}
	err := r.commitAdmissionLocked(space, spaceOf(st))
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if resp != nil {
		_, _ = client.Put(terminals.RespHint(rendezvous, reqID), now+86400, resp)
	}
	return nil
}

// applyDecision records what the host said. Returns true when the answer is
// final and polling should stop.
//
// The three states are three different sentences, and collapsing any two of
// them would put words in somebody's mouth: "a person is looking at it",
// "a person said no", "nobody refused you — the door closed".
func (r *Runtime) applyDecision(at *joinAttempt, d *terminals.Decision) bool {
	r.passes.mu.Lock()
	switch d.State {
	case terminals.DecisionReceived:
		if at.state == JoinWaiting {
			at.state = JoinWaitingHost
		}
		at.reason = d.Reason
		r.passes.mu.Unlock()
		_ = r.commitSaga()
		return false
	case terminals.DecisionDeclined:
		at.state, at.reason = JoinDeclined, d.Reason
	case terminals.DecisionUnavailable:
		at.state, at.reason = JoinExpiredWaiting, d.Reason
	default:
		r.passes.mu.Unlock()
		return false
	}
	r.passes.mu.Unlock()
	_ = r.commitSaga()
	return true
}

// declinePendingForPass turns everyone away when a link is withdrawn. A
// person waiting at a door that no longer exists deserves to be told, not
// left to time out.
func (r *Runtime) declinePendingForPass(passIDShort, reason string) {
	addr := r.GetSettings().Relay
	if addr == "" {
		return
	}
	r.passes.mu.Lock()
	type target struct {
		reqID [32]byte
		xpub  [32]byte
		space id.TerminalID
		rdv   [16]byte
	}
	var waiting []target
	for pid, rec := range r.passes.byID {
		if hexShort(pid[:]) != passIDShort {
			continue
		}
		for _, e := range rec.entries {
			if e.State == storage.EntryPending {
				waiting = append(waiting, target{e.Request, e.Xpub, rec.space, rec.pass.Rendezvous})
			}
		}
	}
	r.passes.mu.Unlock()
	if len(waiting) == 0 {
		return
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return
	}
	defer client.Close()
	now := uint64(time.Now().Unix())
	for _, w := range waiting {
		r.settleEntry(w.reqID, storage.EntryDeclined, storage.OutcomeWithdrawn, now)
		r.sealDecision(client, w.space, w.rdv, w.reqID, w.xpub,
			terminals.DecisionUnavailable, reason)
	}
}

// handleEntryRequests lists who is at the door.
func (a *APIServer) handleEntryRequests(w http.ResponseWriter, r *http.Request) {
	type row struct {
		RequestID string `json:"request_id"`
		Space     string `json:"space"`
		Name      string `json:"name"`
		Device    string `json:"device"`
		AskedAt   uint64 `json:"asked_at"`
		DecidedAt uint64 `json:"decided_at,omitempty"`
		State     string `json:"state"`
		PassID    string `json:"pass_id"`
		PassUntil uint64 `json:"pass_until"`
	}
	reqs := a.rt.EntryRequests()
	out := make([]row, 0, len(reqs))
	for _, e := range reqs {
		out = append(out, row{e.RequestID, e.Space.Hex(), e.Name, e.Device,
			e.AskedAt, e.DecidedAt, e.State, e.PassID, e.PassUntil})
	}
	writeJSON(w, out)
}

// handleDecideEntry admits or turns away one person.
func (a *APIServer) handleDecideEntry(w http.ResponseWriter, r *http.Request) {
	reqID := r.PathValue("req")
	body, err := readBody[struct {
		Admit  bool   `json:"admit"`
		Reason string `json:"reason"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.DecideEntry(reqID, body.Admit, body.Reason); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
