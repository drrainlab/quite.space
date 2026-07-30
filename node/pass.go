// Space Pass runtime (ADR-012, UI-2): mint passes, drive the async join
// over a relay rendezvous, and run the idempotent acceptance on the owner.
//
// A pass carries no epoch keys — pending has no access. The newcomer sends a
// sealed join request to a relay dead-drop; the owner's device polls it,
// validates and consumes the pass, admits the device, rotates the epoch, and
// seals the acceptance (manifest + new epoch) back through the rendezvous.
//
// v1 limitation (honest): the pass registry lives in memory. Passes are
// short-lived and the owner is typically online to accept, so this suffices;
// cross-restart persistence of the registry is future work.
package node

import (
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// handledFate is one request's settled outcome. The EXACT fate, never a
// generic "refused": after a restart the owner must reproduce the true
// reason, and a re-delivery must never report a different fate for the
// same request id.
type handledFate struct {
	outcome storage.EntryOutcome
	at      uint64
}

// passRecord is the owner's authoritative acceptance state for one pass.
//
// `handled` replaces the old map of sealed acceptance BYTES. Storing the
// outcome instead keeps epoch material off disk a second time, and costs
// nothing: AcceptIntoSpace returns the current epoch for an existing member
// without rotating, so a grant is re-derivable at any later epoch.
type passRecord struct {
	pass    *terminals.Pass
	space   id.TerminalID
	used    uint64
	revoked bool
	// relay is per-record because a single global address left passes on a
	// second relay in a mailbox nobody was watching.
	relay    string
	approval string // "" open · "host"
	handled  map[[32]byte]handledFate
	entries  []storage.EntryRecord
}

type passRegistry struct {
	mu      sync.Mutex
	byID    map[[16]byte]*passRecord
	relay   string // rendezvous relay address
	polling bool
}

func newPassRegistry() *passRegistry {
	return &passRegistry{byID: map[[16]byte]*passRecord{}}
}

// PassInfo is the UI projection of a minted pass.
type PassInfo struct {
	PassID    string
	Space     string
	ExpiresAt uint64
	MaxUses   uint64
	Used      uint64
	Revoked   bool
	Link      string // present only right after minting
}

// MintPass creates a join pass for an owned space. The relay address is the
// rendezvous the newcomer will use; it is baked into the pass link.
func (r *Runtime) MintPass(tid id.TerminalID, maxUses, ttlHours uint64, relayAddr string) (PassInfo, error) {
	if maxUses == 0 {
		maxUses = 1
	}
	if maxUses > 10 {
		return PassInfo{}, errors.New("node: v1 passes allow at most 10 entries")
	}
	if ttlHours == 0 {
		ttlHours = 24
	}
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return PassInfo{}, errors.New("node: unknown space")
	}
	now := uint64(time.Now().Unix())
	link, pass, _, err := st.space.NewPass(maxUses, ttlHours*3600, now)
	r.mu.Unlock()
	if err != nil {
		return PassInfo{}, err
	}

	r.passes.mu.Lock()
	r.passes.byID[pass.PassID] = &passRecord{
		pass: pass, space: tid, relay: relayAddr,
		handled: map[[32]byte]handledFate{},
	}
	r.passes.mu.Unlock()

	// Persist BEFORE returning: a live pass with no record is precisely the
	// silent failure this gate removes — the guest asks and the owner has
	// nothing to match it against.
	if err := r.commitSaga(); err != nil {
		return PassInfo{}, err
	}
	r.ensurePassPolling()
	return PassInfo{
		PassID: hexShort(pass.PassID[:]), Space: tid.Hex(),
		ExpiresAt: pass.ExpiresAt, MaxUses: maxUses,
		Link: composeShare(relayAddr, link),
	}, nil
}

// composeShare bundles the rendezvous relay with the signed pass envelope into
// one shareable token, so a newcomer needs to paste/scan only a single thing.
// The bearer secret still lives only here (inside the envelope), never on disk.
func composeShare(relay, envelope string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(relay + "\n" + envelope))
}

func splitShare(shared string) (relay, envelope string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(shared))
	if err != nil {
		return "", "", errors.New("node: this does not look like a Space Pass")
	}
	i := strings.IndexByte(string(raw), '\n')
	if i < 0 {
		return "", "", errors.New("node: malformed Space Pass")
	}
	return string(raw[:i]), string(raw[i+1:]), nil
}

// ListPasses returns the minted passes for the UI.
func (r *Runtime) ListPasses() []PassInfo {
	r.passes.mu.Lock()
	defer r.passes.mu.Unlock()
	out := make([]PassInfo, 0, len(r.passes.byID))
	for pid, rec := range r.passes.byID {
		out = append(out, PassInfo{
			PassID: hexShort(pid[:]), Space: rec.space.Hex(),
			ExpiresAt: rec.pass.ExpiresAt, MaxUses: rec.pass.MaxUses,
			Used: rec.used, Revoked: rec.revoked,
		})
	}
	return out
}

// RevokePass blocks new and still-pending join requests for a pass. It never
// removes members already accepted (ADR-012 invariant 6).
func (r *Runtime) RevokePass(passIDShort string) error {
	r.passes.mu.Lock()
	found := false
	for pid, rec := range r.passes.byID {
		if hexShort(pid[:]) == passIDShort {
			rec.revoked = true
			found = true
			break
		}
	}
	r.passes.mu.Unlock()
	if !found {
		return errors.New("node: unknown pass")
	}
	// Revocation is a promise about a link already in someone's hands. It
	// has to outlive the process that made it.
	return r.commitSaga()
}

func hexShort(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2] = hexd[x>>4]
		out[i*2+1] = hexd[x&0xf]
	}
	return string(out)
}

// ensurePassPolling starts one background loop that collects join requests
// for every active pass from the rendezvous relay and accepts them.
// ensurePassPolling starts the single loop that watches every relay this
// node has passes on. It used to capture ONE address at first start and
// then no-op forever, so a pass minted against a second relay waited in a
// mailbox nobody looked at — and after a restart nothing set the address at
// all, which was the other half of the silent-join bug.
func (r *Runtime) ensurePassPolling() {
	r.passes.mu.Lock()
	if r.passes.polling {
		r.passes.mu.Unlock()
		return
	}
	r.passes.polling = true
	r.passes.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
			}
			// Regroup every tick: passes come and go, and so do relays.
			for addr := range r.passRelays() {
				r.pollPassRequests(addr)
			}
		}
	}()
}

// passRelays is the set of relays with live passes on them right now.
func (r *Runtime) passRelays() map[string]struct{} {
	r.passes.mu.Lock()
	defer r.passes.mu.Unlock()
	out := map[string]struct{}{}
	for _, rec := range r.passes.byID {
		if !rec.revoked && rec.relay != "" {
			out[rec.relay] = struct{}{}
		}
	}
	return out
}

func (r *Runtime) pollPassRequests(addr string) {
	r.passes.mu.Lock()
	var caps [][]byte
	recs := make([]*passRecord, 0, len(r.passes.byID))
	for _, rec := range r.passes.byID {
		if rec.revoked || rec.relay != addr {
			continue
		}
		caps = append(caps, terminals.ReqCap(rec.pass.Rendezvous))
		recs = append(recs, rec)
	}
	r.passes.mu.Unlock()
	if len(caps) == 0 {
		return
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return
	}
	defer client.Close()
	items, err := client.Collect(caps)
	if err != nil {
		return
	}
	for _, sealed := range items {
		r.acceptOne(client, recs, sealed)
	}
}

// acceptOne runs the idempotent acceptance saga for one sealed request.
func (r *Runtime) acceptOne(client *relay.Client, recs []*passRecord, sealed []byte) {
	now := uint64(time.Now().Unix())
	for _, rec := range recs {
		req, err := terminals.OpenJoinRequest(rec.space, rec.pass.PassID,
			r.Device.X25519Priv(), sealed)
		if err != nil {
			continue // not for this pass (or not decryptable by us)
		}
		r.passes.mu.Lock()
		// Idempotent: a re-delivered request gets the SAME fate it already
		// had. The fate is stored, not the sealed bytes — a grant is
		// re-sealed below from the current epoch, which is why this stays
		// correct after the epoch has since rotated for somebody else.
		fate, done := rec.handled[req.RequestID]
		alreadyMember := false
		r.passes.mu.Unlock()

		if !done {
			// Does this device already hold the space? Then re-admitting it
			// costs nothing: AcceptIntoSpace returns the current epoch
			// without rotating. Without this check MaxUses counts ATTEMPTS
			// rather than admissions, and a personal link is "one request"
			// rather than "one device".
			r.mu.Lock()
			if st := r.spaces[rec.space]; st != nil {
				alreadyMember = st.space.HasMember(req.Device)
			}
			r.mu.Unlock()
		}

		if !done && !alreadyMember {
			r.passes.mu.Lock()
			// Validate at the moment of reservation (the owner's clock is
			// the trusted one — ADR-012 review §6), recording WHY.
			switch {
			case rec.revoked:
				rec.handled[req.RequestID] = handledFate{storage.OutcomeRevoked, now}
				r.passes.mu.Unlock()
				_ = r.commitSaga()
				return
			case now >= rec.pass.ExpiresAt:
				rec.handled[req.RequestID] = handledFate{storage.OutcomeExpiredWaiting, now}
				r.passes.mu.Unlock()
				_ = r.commitSaga()
				return
			case rec.used >= rec.pass.MaxUses:
				rec.handled[req.RequestID] = handledFate{storage.OutcomeCapacityExhausted, now}
				r.passes.mu.Unlock()
				_ = r.commitSaga()
				return
			}
			rec.used++
			r.passes.mu.Unlock()
		} else if done && fate.outcome != storage.OutcomeGranted {
			return // a settled refusal stays settled
		}

		// Membership change + rotation + canonical event, then seal the
		// acceptance back to the newcomer's device — all in ONE keystore
		// write, so a crash can never leave a rotated epoch with no record
		// of the use that caused it (ADR-012 invariant 8).
		r.mu.Lock()
		st := r.spaces[rec.space]
		var resp []byte
		if st != nil {
			epochN, epochKey, mf, err := st.space.AcceptIntoSpace(r.Self,
				req.Device, req.DeviceXpub, req.DisplayName, r.Principal.ID, now)
			if err == nil {
				acc := &terminals.Accepted{RequestID: req.RequestID,
					ManifestFrame: mf, EpochN: epochN, EpochKey: epochKey}
				// The memory policy decides whether the past travels with
				// the pass (LR-4): private_history keeps earlier epochs
				// sealed to those who lived them.
				if _, ch := st.space.Character(); ch.Memory != "private_history" {
					acc.History = st.space.EpochHistory()
				}
				resp, _ = terminals.BuildAccepted(rec.space, req.DeviceXpub, acc)
				r.passes.mu.Lock()
				rec.handled[req.RequestID] = handledFate{storage.OutcomeGranted, now}
				r.passes.mu.Unlock()
			}
		}
		err = r.commitAdmissionLocked(rec.space, spaceOf(st))
		r.mu.Unlock()
		if err != nil {
			return // nothing was promised; the request will be re-delivered
		}

		if resp != nil {
			_, _ = client.Put(terminals.RespHint(rec.pass.Rendezvous, req.RequestID), now+86400, resp)
		}
		return
	}
}

// ---- Newcomer side ----

// JoinState is the async join state machine (ADR-012 / plan UI-2).
type JoinState string

const (
	// JoinUnknown: this device has no record of that request. Distinct from
	// a refusal, and the distinction is the point — see JoinStatus.
	JoinUnknown JoinState = "unknown"
	// JoinExpiredWaiting: the window closed with no decision. Distinct from
	// a refusal too: nobody said no, time simply ran out.
	JoinExpiredWaiting JoinState = "expired_while_waiting"

	JoinWaiting  JoinState = "waiting_for_owner"
	JoinReady    JoinState = "ready"
	JoinExpired  JoinState = "expired"
	JoinRevoked  JoinState = "revoked"
	JoinRejected JoinState = "rejected"
)

type joinAttempt struct {
	pass      *terminals.Pass
	secret    [32]byte
	requestID [32]byte
	relayAddr string
	space     id.TerminalID
	state     JoinState
	startedAt uint64
	// collectUntil outlives the pass on purpose: expiry forbids a NEW
	// request, never the collection of a decision already made. A host who
	// admits someone at 23:59 must still reach them at 01:00.
	collectUntil uint64
}

// JoinByPass parses a pass, sends a sealed join request to its rendezvous,
// and starts polling for the acceptance. Returns a request id to poll.
func (r *Runtime) JoinByPass(shared string) (string, error) {
	relayAddr, envelope, err := splitShare(shared)
	if err != nil {
		return "", err
	}
	pass, secret, err := terminals.DecodePass(envelope)
	if err != nil {
		return "", err
	}
	now := uint64(time.Now().Unix())
	if now >= pass.ExpiresAt {
		return "", errors.New("node: this pass has expired")
	}
	nonce, err := terminals.FreshNonce()
	if err != nil {
		return "", err
	}
	reqID, sealedReq, err := terminals.BuildJoinRequestWithSecret(pass, secret,
		r.Device, r.DisplayName(), nonce, r.Device.SignKey())
	if err != nil {
		return "", err
	}
	client, err := relay.DialClient(relayAddr)
	if err != nil {
		return "", err
	}
	defer client.Close()
	if _, err := client.Put(terminals.ReqHint(pass.Rendezvous), now+86400, sealedReq); err != nil {
		return "", err
	}
	at := &joinAttempt{pass: pass, requestID: reqID,
		relayAddr: relayAddr, space: pass.Space, state: JoinWaiting,
		startedAt: now,
		// The guest may collect a decision LONGER than the pass may be used:
		// a host who admits someone at 23:59 must still reach them at 01:00.
		collectUntil: pass.ExpiresAt + uint64(passGrace/time.Second)}
	copy(at.secret[:], secret)
	r.passes.mu.Lock()
	if r.joins == nil {
		r.joins = map[string]*joinAttempt{}
	}
	r.joins[hexShort(reqID[:])] = at
	r.passes.mu.Unlock()

	// Persist the guest's half BEFORE returning: the request is already on
	// the relay, so a crash here would leave somebody waiting on an
	// entrance their own device has forgotten.
	if err := r.commitSaga(); err != nil {
		return "", err
	}
	r.wg.Add(1)
	go r.pollJoinResponse(at)
	return hexShort(reqID[:]), nil
}

// JoinStatus reports the state of an in-flight join.
func (r *Runtime) JoinStatus(reqIDShort string) (JoinState, string) {
	r.passes.mu.Lock()
	defer r.passes.mu.Unlock()
	at, ok := r.joins[reqIDShort]
	if !ok {
		// "I have no record of that" is not "you were refused". Conflating
		// them told every guest whose session was lost that somebody had
		// turned them away — a decision nobody made.
		return JoinUnknown, ""
	}
	if at.state == JoinReady {
		return JoinReady, at.space.Hex()
	}
	return at.state, ""
}

func (r *Runtime) pollJoinResponse(at *joinAttempt) {
	defer r.wg.Done()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
		}
		// Collect until CollectUntil, not until the pass expires: a
		// decision already made must still reach the person it was made
		// about. And when the window really closes, SAY so — this used to
		// return silently, leaving the spinner turning forever.
		if uint64(time.Now().Unix()) > at.collectUntil {
			r.passes.mu.Lock()
			if at.state == JoinWaiting {
				at.state = JoinExpiredWaiting
			}
			r.passes.mu.Unlock()
			_ = r.commitSaga()
			return
		}
		client, err := relay.DialClient(at.relayAddr)
		if err != nil {
			continue
		}
		items, err := client.Collect([][]byte{terminals.RespCap(at.pass.Rendezvous, at.requestID)})
		client.Close()
		if err != nil || len(items) == 0 {
			continue
		}
		for _, sealed := range items {
			acc, err := terminals.OpenAccepted(at.space, at.requestID, r.Device.X25519Priv(), sealed)
			if err != nil {
				continue
			}
			if err := r.adoptAccepted(at, acc); err == nil {
				r.passes.mu.Lock()
				at.state = JoinReady
				r.passes.mu.Unlock()
				return
			}
		}
	}
}

// adoptAccepted opens the space with the granted epoch and starts syncing.
// History begins here (ADR-012): only the current epoch was wrapped for us.
func (r *Runtime) adoptAccepted(at *joinAttempt, acc *terminals.Accepted) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.spaces[at.space]; exists {
		return nil // idempotent — already joined
	}
	s, err := terminals.OpenReplicaAt(at.space, r.root.EventsDir(at.space))
	if err != nil {
		return err
	}
	s.ManifestFrame = acc.ManifestFrame
	s.EnablePrivate(r.Device)
	keys := append([]crypto.EpochKey{}, acc.History...)
	keys = append(keys, crypto.EpochKey{N: acc.EpochN, Key: acc.EpochKey})
	s.RestoreEpochs(keys)
	r.Self.ResumeChain(s)
	title := "joined space"
	if m := manifestTitle(acc.ManifestFrame); m != "" {
		title = m
	}
	// Keep the manifest with the meta: the character (archetype, mood,
	// memory policy) must survive restarts on joined replicas too.
	r.ks.Spaces[at.space] = storage.SpaceMeta{Title: title,
		ManifestFrame: acc.ManifestFrame}
	r.attach(at.space, s)
	if _, _, err := r.Self.PublishManifest(s); err != nil {
		return err
	}
	r.persistEpochsLocked(at.space, s)
	return nil
}
