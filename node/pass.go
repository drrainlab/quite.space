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
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// passRecord is the owner's authoritative acceptance state for one pass.
type passRecord struct {
	pass      *terminals.Pass
	space     id.TerminalID
	used      uint64
	revoked   bool
	processed map[[32]byte][]byte // request_id → sealed acceptance (idempotent)
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
		pass: pass, space: tid, processed: map[[32]byte][]byte{},
	}
	r.passes.relay = relayAddr
	r.passes.mu.Unlock()

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
	defer r.passes.mu.Unlock()
	for pid, rec := range r.passes.byID {
		if hexShort(pid[:]) == passIDShort {
			rec.revoked = true
			return nil
		}
	}
	return errors.New("node: unknown pass")
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
func (r *Runtime) ensurePassPolling() {
	r.passes.mu.Lock()
	if r.passes.polling || r.passes.relay == "" {
		r.passes.mu.Unlock()
		return
	}
	r.passes.polling = true
	addr := r.passes.relay
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
			r.pollPassRequests(addr)
		}
	}()
}

func (r *Runtime) pollPassRequests(addr string) {
	r.passes.mu.Lock()
	var hints [][]byte
	recs := make([]*passRecord, 0, len(r.passes.byID))
	for _, rec := range r.passes.byID {
		if rec.revoked {
			continue
		}
		hints = append(hints, terminals.ReqHint(rec.pass.Rendezvous))
		recs = append(recs, rec)
	}
	r.passes.mu.Unlock()
	if len(hints) == 0 {
		return
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return
	}
	defer client.Close()
	items, err := client.Collect(hints)
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
		// Idempotent: a re-delivered request returns the stored acceptance.
		if resp, done := rec.processed[req.RequestID]; done {
			r.passes.mu.Unlock()
			if resp != nil {
				_, _ = client.Put(terminals.RespHint(rec.pass.Rendezvous, req.RequestID), now+86400, resp)
			}
			return
		}
		// Validate the pass at the moment of reservation (owner clock is the
		// trusted one — ADR-012 review §6).
		if rec.revoked || now >= rec.pass.ExpiresAt || rec.used >= rec.pass.MaxUses {
			rec.processed[req.RequestID] = nil // consumed the attempt, no access
			r.passes.mu.Unlock()
			return
		}
		rec.used++
		r.passes.mu.Unlock()

		// Membership change + rotation + canonical event, then seal the
		// acceptance back to the newcomer's device.
		r.mu.Lock()
		st := r.spaces[rec.space]
		var resp []byte
		if st != nil {
			epochN, epochKey, mf, err := st.space.AcceptIntoSpace(r.Self,
				req.Device, req.DeviceXpub, req.DisplayName, r.Principal.ID, now)
			if err == nil {
				acc := &terminals.Accepted{RequestID: req.RequestID,
					ManifestFrame: mf, EpochN: epochN, EpochKey: epochKey}
				resp, _ = terminals.BuildAccepted(rec.space, req.DeviceXpub, acc)
				r.persistEpochsLocked(rec.space, st.space)
			}
		}
		r.mu.Unlock()

		r.passes.mu.Lock()
		rec.processed[req.RequestID] = resp
		r.passes.mu.Unlock()
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
	JoinWaiting  JoinState = "waiting_for_owner"
	JoinReady    JoinState = "ready"
	JoinExpired  JoinState = "expired"
	JoinRevoked  JoinState = "revoked"
	JoinRejected JoinState = "rejected"
)

type joinAttempt struct {
	pass      *terminals.Pass
	secret    []byte
	requestID [32]byte
	relayAddr string
	space     id.TerminalID
	state     JoinState
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
	at := &joinAttempt{pass: pass, secret: secret, requestID: reqID,
		relayAddr: relayAddr, space: pass.Space, state: JoinWaiting}
	r.passes.mu.Lock()
	if r.joins == nil {
		r.joins = map[string]*joinAttempt{}
	}
	r.joins[hexShort(reqID[:])] = at
	r.passes.mu.Unlock()

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
		return JoinRejected, ""
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
	deadline := time.Now().Add(10 * time.Minute)
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
		}
		if time.Now().After(deadline) {
			return
		}
		client, err := relay.DialClient(at.relayAddr)
		if err != nil {
			continue
		}
		items, err := client.Collect([][]byte{terminals.RespHint(at.pass.Rendezvous, at.requestID)})
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
	s.RestoreEpochs([]crypto.EpochKey{{N: acc.EpochN, Key: acc.EpochKey}})
	r.Self.ResumeChain(s)
	title := "joined space"
	if m := manifestTitle(acc.ManifestFrame); m != "" {
		title = m
	}
	r.ks.Spaces[at.space] = storage.SpaceMeta{Title: title}
	r.attach(at.space, s)
	if _, _, err := r.Self.PublishManifest(s); err != nil {
		return err
	}
	r.persistEpochsLocked(at.space, s)
	return nil
}
