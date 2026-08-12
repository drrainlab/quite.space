// The connector seam (TR-0c): external systems feed the node through a
// normalized envelope, a durable journal, and a projector that turns
// journaled ingress into ordinary space events — signed by the GATEWAY
// participant, marked imported, carrying key-7 provenance.
//
// The projector is the pass acceptor's shape (node/pass.go acceptOne):
// consult the journal first, settled outcomes stay settled, the journal is
// written and fsynced BEFORE the side effect, and a crash between the emit
// and the settle is reconciled against the space's own log at restart —
// the log is the recovery authority, never a guess.
package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/terminals/gateway"
)

// ExternalEnvelope is the normalized runtime shape of one observed external
// message. It is a LOCAL abstraction — never a wire schema: connectors
// produce it, the journal seals it, the projector consumes it.
type ExternalEnvelope struct {
	// ExternalID is the STABLE transport-level identity (JMAP Email/id for
	// mail) — the dedup key's basis. Required; the Internet's Message-ID is
	// not trusted with this job.
	ExternalID string `json:"external_id"`
	// Kind names the boundary protocol ("email", "fixture").
	Kind string `json:"kind"`
	// Address is the external sender as observed.
	Address string `json:"address,omitempty"`
	// ExternalRef is the provenance/threading reference (RFC Message-ID).
	ExternalRef string `json:"external_ref,omitempty"`
	// ThreadRef is the external parent's reference, when one was declared.
	ThreadRef string `json:"thread_ref,omitempty"`
	// Text is the normalized body — already through the connector's policy
	// gate (text-only profile, bounded readers).
	Text string `json:"text"`
	// LossFlags name what the boundary dropped ("attachments_omitted", …).
	LossFlags []string `json:"loss_flags,omitempty"`
	// ObservedAt is the connector's own clock at ingress.
	ObservedAt int64 `json:"observed_at"`
}

var connIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// connState is one connector's runtime: its journal and its projector's
// single-flight latch.
type connState struct {
	id      string
	journal *connJournal
	running bool // projector goroutine live; guarded by r.mu
}

// connector opens (or returns) a connector's durable state.
func (r *Runtime) connector(connID string) (*connState, error) {
	if !connIDRe.MatchString(connID) {
		return nil, fmt.Errorf("node: connector id %q (want %s)", connID, connIDRe)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.connectors[connID]; ok {
		return cs, nil
	}
	j, err := openConnJournal(filepath.Join(r.dataDir, "connectors", connID))
	if err != nil {
		return nil, err
	}
	if r.connectors == nil {
		r.connectors = map[string]*connState{}
	}
	cs := &connState{id: connID, journal: j}
	r.connectors[connID] = cs
	return cs, nil
}

// ConnectorRoute closes the connector's current binding and opens the next
// one toward target — the temporal boundary (plan rev 3). Unfinished
// records of the old generation become orphaned, on disk, before this
// returns; nothing ever crosses, in either direction.
func (r *Runtime) ConnectorRoute(connID string, target id.TerminalID) (uint64, error) {
	cs, err := r.connector(connID)
	if err != nil {
		return 0, err
	}
	gen, _, err := cs.journal.OpenBinding(target, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	r.kickProjector(cs)
	return gen, nil
}

// ConnectorIngest journals one observed message — DURABLY, before any
// projection is attempted, because receiving is a fact and projecting is a
// hope. Idempotent on the transport id within the active binding.
func (r *Runtime) ConnectorIngest(connID string, env ExternalEnvelope) error {
	if env.ExternalID == "" {
		return errors.New("node: connector ingest needs a stable external id")
	}
	if env.Text == "" {
		return errors.New("node: connector ingest carries no text (the policy gate refuses upstream)")
	}
	if len(env.Text) > schemas.MaxTextLen {
		return errors.New("node: connector ingest text over protocol bound (truncate at the gate)")
	}
	cs, err := r.connector(connID)
	if err != nil {
		return err
	}
	key := connExternalKey(connID, env.ExternalID)
	// The recovery anchor: every projection carries a resolvable external
	// ref, even when the outside world declared none — reconciliation
	// against the space's own log must never depend on the Internet's
	// manners.
	if env.ExternalRef == "" {
		env.ExternalRef = "qk:" + hex.EncodeToString(key[:16])
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := r.root.SaveSealed(connSealedName(key), body); err != nil {
		return err
	}
	var threadHash [32]byte
	if env.ThreadRef != "" {
		threadHash = sha256.Sum256([]byte(env.ThreadRef))
	}
	_, dup, err := cs.journal.Ingest(key, id.Hash{},
		sha256.Sum256([]byte(env.ExternalRef)), threadHash, time.Now().Unix())
	if err != nil {
		return err
	}
	if !dup {
		r.kickProjector(cs)
	}
	return nil
}

// ConnectorStatus is the honest one-line summary.
type ConnectorStatus struct {
	ID        string
	Binding   uint64
	Target    string // hex, empty when no route is open
	Pending   int
	Published int
	Refused   int
	Orphaned  int
}

func (r *Runtime) ConnectorStatus(connID string) (ConnectorStatus, error) {
	cs, err := r.connector(connID)
	if err != nil {
		return ConnectorStatus{}, err
	}
	gen, target, ok := cs.journal.Binding()
	st := ConnectorStatus{ID: connID, Binding: gen}
	if ok {
		st.Target = target.Hex()
	}
	st.Pending, st.Published, st.Refused, st.Orphaned = cs.journal.Counts()
	return st, nil
}

// ConnectorIDs lists the connectors present on disk, sorted — the status
// page's inventory.
func (r *Runtime) ConnectorIDs() []string {
	entries, err := os.ReadDir(filepath.Join(r.dataDir, "connectors"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && connIDRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// resumeConnectors restores the INTENT TO ACT after Open (the saga rule,
// node/saga.go:164): every connector directory on disk gets its journal
// reopened and its projector kicked over whatever was left unfinished.
func (r *Runtime) resumeConnectors() {
	dir := filepath.Join(r.dataDir, "connectors")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // none yet
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if cs, err := r.connector(e.Name()); err == nil {
			r.kickProjector(cs)
		}
	}
}

// closeConnectors releases the journals at shutdown.
func (r *Runtime) closeConnectors() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cs := range r.connectors {
		_ = cs.journal.Close()
	}
}

func connExternalKey(connID, externalID string) [32]byte {
	h := sha256.New()
	h.Write([]byte(connID))
	h.Write([]byte{0})
	h.Write([]byte(externalID))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// connSealedName names the sealed envelope blob. No space hex in the name:
// deleteSealedFor sweeps sealed blobs by space-id substring, and an
// envelope's fate is the JOURNAL's to decide, not a space deletion's.
func connSealedName(key [32]byte) string {
	return "conn-in-" + hex.EncodeToString(key[:])
}

// ---- the projector ----

// kickProjector starts the connector's projector goroutine if it is not
// already running. Single-flight per connector; re-kicks while running are
// absorbed because the loop re-reads Pending() until it is empty.
func (r *Runtime) kickProjector(cs *connState) {
	r.mu.Lock()
	if cs.running {
		r.mu.Unlock()
		return
	}
	cs.running = true
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			cs.running = false
			r.mu.Unlock()
		}()
		r.projectorLoop(cs)
	}()
}

// projectorLoop drains the current generation's pending records. A
// retryable failure (target space not here yet, write gate closed) parks
// the loop on a modest ticker rather than exiting: the join may still be
// in flight, and the operator was promised the request survives.
func (r *Runtime) projectorLoop(cs *connState) {
	for {
		r.reconcileEmitting(cs)
		pending := cs.journal.Pending()
		if len(pending) == 0 {
			return
		}
		progressed := false
		for i := range pending {
			ok, terminal := r.projectOne(cs, &pending[i])
			if ok || terminal {
				progressed = true
			}
		}
		if !progressed {
			select {
			case <-r.stop:
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

// reconcileEmitting is the crash-window recovery (plan rev 2, R2): for
// records caught mid-emit, the target space's own log is the authority.
// ONE pass over the log builds the ref-hash index for every such record —
// recovery must not go quadratic (rev 3).
func (r *Runtime) reconcileEmitting(cs *connState) {
	gen, target, ok := cs.journal.Binding()
	if !ok {
		return
	}
	var stuck []IngressRecord
	for _, rec := range cs.journal.Pending() {
		if rec.State == ConnEmitting {
			stuck = append(stuck, rec)
		}
	}
	if len(stuck) == 0 {
		return
	}
	found := map[[32]byte]id.EventID{}
	_ = r.withSpace(target, func(st *spaceState) error {
		for _, e := range st.space.State.Entries() {
			if e.Content.Text == nil || e.Content.Text.External == nil {
				continue
			}
			ref := e.Content.Text.External.ExternalRef
			if ref == "" {
				continue
			}
			found[sha256.Sum256([]byte(ref))] = e.ID
		}
		return nil
	})
	now := time.Now().Unix()
	for _, rec := range stuck {
		if eid, hit := found[rec.RefHash]; hit {
			// The emit landed before the crash: settle, never re-emit.
			_, _, _ = cs.journal.Update(gen, rec.Key, now, func(in *IngressRecord) bool {
				if in.State != ConnEmitting {
					return false
				}
				in.State = ConnPublished
				in.EventID = eid
				return true
			})
		} else {
			// The emit never landed: safe to try again from Received.
			_, _, _ = cs.journal.Update(gen, rec.Key, now, func(in *IngressRecord) bool {
				if in.State != ConnEmitting {
					return false
				}
				in.State = ConnReceived
				return true
			})
		}
	}
}

// projectOne walks one record through Received → Emitting → Published.
// Returns (published, terminal): terminal covers refusals and orphans —
// outcomes that will never change, per the acceptor's rule.
func (r *Runtime) projectOne(cs *connState, rec *IngressRecord) (bool, bool) {
	gen, target, ok := cs.journal.Binding()
	if !ok || rec.Binding != gen {
		return false, true // its binding is gone; OpenBinding orphaned it
	}
	raw, err := r.root.LoadSealed(connSealedName(rec.Key))
	if err != nil {
		now := time.Now().Unix()
		_, _, _ = cs.journal.Update(gen, rec.Key, now, func(in *IngressRecord) bool {
			in.State = ConnRefused
			in.Outcome = "body_lost"
			return true
		})
		return false, true
	}
	var env ExternalEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		now := time.Now().Unix()
		_, _, _ = cs.journal.Update(gen, rec.Key, now, func(in *IngressRecord) bool {
			in.State = ConnRefused
			in.Outcome = "body_malformed"
			return true
		})
		return false, true
	}

	// The reply edge: the external thread resolves through what THIS
	// journal already imported — or flattens to nothing, honestly.
	var replyTo *id.EventID
	if rec.ThreadHash != ([32]byte{}) {
		if eid, ok := cs.journal.PublishedByRef(rec.ThreadHash); ok {
			replyTo = &eid
		}
	}

	// Journal the intent BEFORE the side effect (ADR-012 order), so a
	// crash leaves a state recovery can reason about. GUARDED transition:
	// a record that stopped being Received meanwhile (a route change
	// orphaned it under our feet) stays exactly what the boundary made it.
	now := time.Now().Unix()
	if _, wrote, err := cs.journal.Update(gen, rec.Key, now, func(in *IngressRecord) bool {
		if in.State != ConnReceived {
			return false
		}
		in.State = ConnEmitting
		return true
	}); err != nil || !wrote {
		return false, false
	}

	var eid id.EventID
	err = r.withSpace(target, func(st *spaceState) error {
		gw, err := r.ensureGatewayLocked()
		if err != nil {
			return err
		}
		r.gatewayManifest(gw, st)
		a, err := gateway.Import(gw, st.space, env.Text, &schemas.ExternalOrigin{
			ConnectorKind: env.Kind,
			Address:       env.Address,
			ExternalRef:   env.ExternalRef,
			ThreadRef:     env.ThreadRef,
			LossFlags:     env.LossFlags,
		}, replyTo, uint64(env.ObservedAt))
		if err != nil {
			return err
		}
		eid = a.ID
		return nil
	})
	if err != nil {
		// Retryable: the space is not here yet (a join in flight) or the
		// write gate is closed right now. Back to Received — but ONLY from
		// Emitting: a blind write here resurrected records the route
		// change had just orphaned (caught by the generation tests).
		_, _, _ = cs.journal.Update(gen, rec.Key, time.Now().Unix(), func(in *IngressRecord) bool {
			if in.State != ConnEmitting {
				return false
			}
			in.State = ConnReceived
			return true
		})
		return false, false
	}
	_, _, _ = cs.journal.Update(gen, rec.Key, time.Now().Unix(), func(in *IngressRecord) bool {
		if in.State != ConnEmitting {
			// Orphaned between the emit and this settle: the terminal
			// state wins. The event exists in ITS OWN binding's target —
			// the boundary still held — and a closed generation's record
			// is never rewritten.
			return false
		}
		in.State = ConnPublished
		in.EventID = eid
		return true
	})
	return true, false
}

// ---- the gateway participant ----

// ensureGatewayLocked returns the gateway participant, minting its identity
// on first use — its own device seed and terminal seed, the OPERATOR's
// principal as controller, exactly the assistant's shape and for the same
// reasons (kernel/storage/agent.go). Named -Locked for its keystore write:
// callers already run inside withSpace, which holds r.mu.
func (r *Runtime) ensureGatewayLocked() (*terminals.Participant, error) {
	if r.gateway != nil {
		return r.gateway, nil
	}
	if r.ks.Gateway.Exists() {
		dev, err := identity.NewDeviceFromKeys(r.ks.Gateway.DeviceSeed, r.ks.Gateway.DeviceX25519)
		if err != nil {
			return nil, err
		}
		p, err := terminals.NewParticipantFromManifest(r.Principal, dev,
			r.ks.Gateway.TerminalSeed, r.ks.Gateway.ManifestFrame)
		if err != nil {
			return nil, err
		}
		r.gateway = p
		return p, nil
	}
	dev, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		return nil, err
	}
	p, seed, err := terminals.NewParticipantFrom(r.Principal, dev, nil,
		gateway.Template("external gateway"))
	if err != nil {
		return nil, err
	}
	r.ks.Gateway = storage.AgentRecord{
		DeviceSeed: dev.Seed(), DeviceX25519: dev.X25519Priv(),
		TerminalSeed: seed, ManifestFrame: p.ManifestFrame,
	}
	if err := r.saveKeystore(); err != nil {
		return nil, err
	}
	r.gateway = p
	return p, nil
}

// gatewayManifest publishes the gateway's card into a space once per
// process — idempotent on the other side, cheap on this one — so the room
// honestly shows WHAT is in it before the first import lands.
func (r *Runtime) gatewayManifest(gw *terminals.Participant, st *spaceState) {
	if r.gatewayShown == nil {
		r.gatewayShown = map[id.TerminalID]bool{}
	}
	tid := st.space.Log.Terminal
	if r.gatewayShown[tid] {
		return
	}
	if _, _, err := gw.PublishManifest(st.space); err == nil {
		r.gatewayShown[tid] = true
	}
}

// loadGatewayLocked rebuilds the gateway participant at Open, before spaces
// attach, so ResumeChain can run beside the person's and the agent's —
// forgetting it forks the gateway's chain on the first import after a
// restart.
func (r *Runtime) loadGatewayLocked() error {
	rec := r.ks.Gateway
	if !rec.Exists() {
		return nil
	}
	dev, err := identity.NewDeviceFromKeys(rec.DeviceSeed, rec.DeviceX25519)
	if err != nil {
		return err
	}
	p, err := terminals.NewParticipantFromManifest(r.Principal, dev,
		rec.TerminalSeed, rec.ManifestFrame)
	if err != nil {
		return err
	}
	r.gateway = p
	return nil
}
