// Public-space node runtime (PA-0.4B): the projection publisher (owner side)
// and the reader replica (stranger side). The owner pushes its signed
// projection to the relay's public outbox (atomic Replace, durable monotonic
// seq, heartbeat); a reader opens a replica from just the space id + relay
// address and installs verified projections into its projection store.
package node

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// publicHeartbeat bounds how stale a healthy outbox may get: a squatter
// wipe or relay restart is repaired within this window even with no new
// content.
const publicHeartbeat = 5 * time.Minute

func relayBucketNow() uint64 { return relay.Bucket(uint64(time.Now().Unix())) }

// OpenPublicSpace opens (or returns) a READER replica of a public space:
// no invite, no keys — the space id from a link is the read capability.
// The first projection fetch runs synchronously so the space is not empty
// on first paint when the relay is reachable.
func (r *Runtime) OpenPublicSpace(spaceID id.TerminalID, relayAddr string) error {
	r.mu.Lock()
	if _, exists := r.spaces[spaceID]; exists {
		r.mu.Unlock()
		return nil // idempotent — already a member or reader
	}
	s, err := terminals.OpenReplicaAt(spaceID, r.root.EventsDir(spaceID))
	if err != nil {
		r.mu.Unlock()
		return err
	}
	s.ReadOnly = true
	s.OnBlock = r.onBlockEvent(spaceID)
	// Bootstrap state (I1): no manifest yet → neither public nor private;
	// the verified manifest arrives inside the first projection.
	r.ks.Spaces[spaceID] = storage.SpaceMeta{
		Title: "public space", Role: storage.RoleReader,
	}
	r.attach(spaceID, s)
	err = r.saveKeystore()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if relayAddr != "" {
		_ = r.fetchPublicProjection(relayAddr, spaceID) // best-effort first paint
	}
	return nil
}

// fetchPublicProjection reads the space's public outbox (current + previous
// bucket) and installs the newest acceptable projection. Acceptance (I7 +
// anti-equivocation): seq must not regress; an equal seq must carry the
// same content digest — a different one is refused and surfaced.
func (r *Runtime) fetchPublicProjection(addr string, tid id.TerminalID) error {
	if err := r.relayGate(); err != nil {
		return err
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	return r.fetchPublicProjectionWith(client, tid)
}

func (r *Runtime) fetchPublicProjectionWith(client *relay.Client, tid id.TerminalID) error {
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	hints := [][]byte{relay.HintPublicOutbox(tid, b)}
	if b > 0 {
		hints = append(hints, relay.HintPublicOutbox(tid, b-1))
	}
	items, err := client.Fetch(hints)
	if err != nil {
		return err
	}
	var best *projection.Envelope
	for _, item := range items {
		env, err := projection.Decode(item)
		if err != nil || env.SpaceID != tid {
			continue
		}
		if projection.Verify(env) != nil {
			continue
		}
		if best == nil || env.Seq > best.Seq {
			best = env
		}
	}
	if best == nil {
		return errors.New("node: no projection available at the relay")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if r.ks.PublicPublish == nil {
		r.ks.PublicPublish = map[id.TerminalID]storage.PublicPublishState{}
	}
	acc := r.ks.PublicPublish[tid]
	digest := best.ContentDigest()
	switch {
	case best.Seq < acc.ProjectionSeq:
		return errors.New("node: projection sequence regressed — kept the newer one")
	case best.Seq == acc.ProjectionSeq && acc.ProjectionSeq != 0 &&
		digest != acc.LastContentDigest:
		// Same seq, different content: equivocation or forgery survivor.
		// Refuse loudly — never silently re-materialize.
		return errors.New("node: projection equivocation detected (same seq, different content)")
	}
	if _, err := st.space.InstallPublicProjection(best); err != nil {
		return err
	}
	// Accept: remember the publisher's seq + digest, repair the meta cache
	// (title, visibility) from the now-verified manifest.
	r.ks.PublicPublish[tid] = storage.PublicPublishState{
		PublisherDevice: best.PublisherDevice,
		ProjectionSeq:   best.Seq, LastContentDigest: digest,
	}
	meta := r.ks.Spaces[tid]
	if title := manifestTitle(st.space.ManifestFrame); title != "" {
		meta.Title = title
	}
	meta.ManifestFrame = st.space.ManifestFrame
	pol := st.space.Policy()
	meta.Visibility = string(pol.Effective())
	r.ks.Spaces[tid] = meta
	// Curator auto-activation: broadcast spaces have no Join button, but a
	// curator opening the public link IS an attested writer — recognized
	// from the verified signed policy, never from local claims.
	if meta.Role == storage.RoleReader && pol.Publish == terminals.PublishCurated {
		mine := terminals.WriterBinding{Principal: r.Principal.ID, Device: r.Device.ID}
		for _, w := range pol.Writers {
			if w == mine {
				if err := r.activateContributorLocked(tid, st); err != nil {
					return err
				}
				break
			}
		}
	}
	return r.saveKeystore()
}

// publicLinkPrefix marks a public-space share link's envelope half —
// distinguishable from a Space Pass by the same splitShare parser, so one
// paste box serves both artifact kinds.
const publicLinkPrefix = "space:"

// ComposePublicLink builds the share link of a public space: the relay
// address (from settings) + the space id. IRREVOCABLE by design — whoever
// learns the id derives every future mailbox hint and reads forever; that
// is the declared semantics of the tier.
func (r *Runtime) ComposePublicLink(tid id.TerminalID) (string, error) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	r.mu.Unlock()
	if !ok {
		return "", errors.New("node: unknown space")
	}
	if !st.space.Policy().IsPublic() {
		return "", errors.New("node: not a public space")
	}
	relayAddr := r.GetSettings().Relay
	if relayAddr == "" {
		return "", errors.New("node: set a relay in Settings first — the link carries it")
	}
	return composeShare(relayAddr, publicLinkPrefix+tid.Hex()), nil
}

// OpenPublicLink parses a public share link and opens the reader replica.
func (r *Runtime) OpenPublicLink(link string) (id.TerminalID, error) {
	relayAddr, envelope, err := splitShare(link)
	if err != nil {
		return id.TerminalID{}, err
	}
	if !strings.HasPrefix(envelope, publicLinkPrefix) {
		return id.TerminalID{}, errors.New("node: not a public space link")
	}
	tid, err := id.ParseTerminalID(strings.TrimPrefix(envelope, publicLinkPrefix))
	if err != nil {
		return id.TerminalID{}, errors.New("node: malformed space id in link")
	}
	if err := r.OpenPublicSpace(tid, relayAddr); err != nil {
		return id.TerminalID{}, err
	}
	// Remember the relay for the background loop if none is configured yet.
	if s := r.GetSettings(); s.Relay == "" {
		s.Relay = relayAddr
		_ = r.SetSettings(s)
	}
	return tid, nil
}

// JoinPublicSpace turns a reader replica into a contributor (open-join
// communities). Low-level checks — a UI cannot skip them: the manifest must
// be VERIFIED and installed, the policy public with join=open, and the
// current role reader. The join itself is the first ingress bundle carrying
// the self manifest: no pass, no approval event.
func (r *Runtime) JoinPublicSpace(tid id.TerminalID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	meta := r.ks.Spaces[tid]
	if meta.Role != storage.RoleReader {
		return errors.New("node: already a contributor here")
	}
	if len(st.space.ManifestFrame) == 0 {
		return errors.New("node: the space's manifest has not arrived yet")
	}
	pol := st.space.Policy()
	if !pol.IsPublic() {
		return errors.New("node: not a public space")
	}
	if pol.Join != terminals.JoinOpen {
		return errors.New("node: this space does not take self-serve joins")
	}
	return r.activateContributorLocked(tid, st)
}

// activateContributorLocked flips reader → contributor: enables the local
// write gate, resumes the participant chain, and publishes the self
// manifest into the LOCAL log — the durable pending queue the ingress
// uplink drains. Caller holds r.mu.
func (r *Runtime) activateContributorLocked(tid id.TerminalID, st *spaceState) error {
	meta := r.ks.Spaces[tid]
	meta.Role = ""
	r.ks.Spaces[tid] = meta
	st.space.ReadOnly = false
	r.Self.ResumeChain(st.space)
	if _, _, err := r.Self.PublishManifest(st.space); err != nil {
		return err
	}
	return r.saveKeystore()
}

// pushPublicIngress uploads this replica's pending contribution to its
// deterministic ingress shard: unacked local frames (contributors) and/or
// media wants (readers and contributors alike — wants need no membership).
// The cumulative bundle is byte-stable between changes, so the relay's
// content-idempotent Put keeps exactly one slot however often we retry.
func (r *Runtime) pushPublicIngress(addr string, tid id.TerminalID) error {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return errors.New("node: unknown space")
	}
	if st.space.Policy().Frozen {
		// TRUE freeze: pause the uplink; pending stays local and durable
		// (I8) and delivery resumes after the unfreezing revision — the
		// relay never fills with retries against a sealed space.
		r.mu.Unlock()
		return nil
	}
	frames := st.space.UnackedLocalFrames()
	wants := r.relayWantsLocked(tid)
	self := r.Device.ID
	r.mu.Unlock()
	if len(frames) == 0 && len(wants) == 0 {
		return nil
	}
	body := bundle.EncodeWithWants(tid, frames, nil, wants, self[:])
	if err := r.relayGate(); err != nil {
		return err
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	now := uint64(time.Now().Unix())
	// Short TTL: ingress is consumed by the owner within a bucket or
	// re-pushed; stale bundles must not linger against relay quotas.
	hint := relay.HintPublicIngress(tid, relay.Bucket(now), relay.IngressShard(self))
	_, err = client.Put(hint, now+6*3600, body)
	return err
}

// collectPublicIngress drains all ingress shards of an OWNED public space
// (current + previous bucket): absorbs contributed frames through the
// canonical gates (admission, chains, dedup), answers media wants, and
// starts custody fetches for newly referenced assets (PA-0.4D).
func (r *Runtime) collectPublicIngress(addr string, tid id.TerminalID) (int, error) {
	r.mu.Lock()
	if st, ok := r.spaces[tid]; ok && st.space.Policy().Frozen {
		r.mu.Unlock()
		return 0, nil // TRUE freeze: ingress is not read at all
	}
	r.mu.Unlock()
	if err := r.relayGate(); err != nil {
		return 0, err
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	var hints [][]byte
	for sh := byte(0); sh < relay.IngressShards; sh++ {
		hints = append(hints, relay.HintPublicIngress(tid, b, sh))
		if b > 0 {
			hints = append(hints, relay.HintPublicIngress(tid, b-1, sh))
		}
	}
	items, err := client.Collect(hints)
	if err != nil {
		return 0, err
	}
	applied := 0
	nowT := time.Now()
	budget := newAuthorBudget() // per-drain-cycle abuse caps (PA-1.3)
	for _, item := range items {
		parts, err := bundle.DecodeParts(item)
		if err != nil || parts.Terminal != tid {
			continue
		}
		r.mu.Lock()
		st, ok := r.spaces[tid]
		if !ok {
			r.mu.Unlock()
			continue
		}
		if st.rejected == nil {
			st.rejected = newRejectedRing()
		}
		for _, f := range parts.Frames {
			if len(f) > maxIngressFrame {
				continue // community frame-size cap
			}
			eid := id.EventIDOf(f)
			if st.rejected.has(eid, nowT) {
				continue // known-bad frame: drop before re-verifying
			}
			// Per-author caps keyed on the CLAIMED signer device: an
			// invalid signer is rejected by Absorb below anyway, and a
			// valid one cannot exceed its per-cycle share.
			dev := ingressFrameDevice(f)
			if !budget.admit(dev, len(f)) {
				continue // over budget this cycle — arrives on a later one
			}
			n, err := st.space.Absorb(f)
			if err != nil {
				st.rejected.remember(eid, nowT)
				continue // admission-refused or malformed: PolicyStats has it
			}
			applied += n
		}
		r.mu.Unlock()
		// Custody (0.4D): any asset referenced by newly contributed block
		// frames gets fetched from its author through the existing member
		// wants machinery; until the blob is verified locally the
		// projection's Exclude filter keeps the publication unprojected.
		r.requestIncompleteAssets(tid, parts.Frames)
		if len(parts.Wants) > 0 {
			r.answerWants(client, tid, parts.Wanter, parts.Wants)
		}
	}
	return applied, nil
}

// ingressFrameDevice reads the CLAIMED signer device of a frame without
// verifying its signature — a cheap key for per-author rate limiting. A
// malformed frame maps to the zero device (its own bucket); Absorb rejects
// it regardless.
func ingressFrameDevice(frame []byte) id.DeviceID {
	env, err := signal.Decode(frame)
	if err != nil {
		return id.DeviceID{}
	}
	return env.Device
}

// maxIngressFrame caps one contributed frame (community limit).
const maxIngressFrame = 256 << 10

// requestIncompleteAssets starts background custody fetches for assets
// referenced by the given frames that are not yet locally complete.
func (r *Runtime) requestIncompleteAssets(tid id.TerminalID, frames [][]byte) {
	for _, f := range frames {
		env, err := signal.Decode(f)
		if err != nil || env.PayloadEncoding != signal.PayloadCBOR {
			continue
		}
		for _, ref := range schemas.ExtractAssetRefs(env.Schema, env.Payload) {
			if ref == nil {
				continue
			}
			aid := ref.PublicIDHex()
			if st, err := r.AssetStatus(tid, aid); err == nil && st.State != assets.StateComplete {
				_ = r.RequestAsset(tid, aid)
			}
		}
	}
}

// assetIncompleteExclude is the projection custody filter (PA-0.4D): block
// frames whose referenced assets are not locally verified stay OUT of the
// public projection — "a participant's asset becomes publicly visible only
// after the publisher holds its blob". Custody status lives in the node's
// content-addressed asset store, never in canonical reducer state.
// The closure runs inside BuildPublicProjection while the CALLER holds
// r.mu — it must use the locked asset-status variant.
func (r *Runtime) assetIncompleteExclude(tid id.TerminalID) func(env *signal.Envelope, eid id.EventID) bool {
	return func(env *signal.Envelope, eid id.EventID) bool {
		if env.PayloadEncoding != signal.PayloadCBOR || !schemas.IsBlockSchema(env.Schema) {
			return false
		}
		for _, ref := range schemas.ExtractAssetRefs(env.Schema, env.Payload) {
			if ref == nil {
				continue
			}
			st, err := r.assetStatusLocked(AssetKey{Space: tid, Asset: ref.PublicIDHex()})
			if err == nil && st.State != assets.StateComplete {
				return true
			}
		}
		return false
	}
}

// PolicyDelta is one owner-requested policy revision (PA-1). Nil fields
// stay unchanged; mode switches normalize join/writers consistently.
type PolicyDelta struct {
	Visibility    *string // "unlisted" | "public"
	Publish       *string // "all" | "curated" — flips the mode
	AddCurator    *terminals.WriterBinding
	RemoveCurator *terminals.WriterBinding // exact (principal, device) pair
	Frozen        *bool
}

// RevisePolicy re-signs the space manifest with the revised policy
// (owner device only; the space key signs). The revised manifest rides
// the next projection automatically — its digest changes, so the
// publisher bumps Seq and every reader picks it up with anti-rollback.
func (r *Runtime) RevisePolicy(tid id.TerminalID, d PolicyDelta) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	meta := r.ks.Spaces[tid]
	if !meta.Owned {
		return errors.New("node: only the owner revises policy")
	}
	title, character := st.space.Character()
	next := st.space.Policy()
	if d.Visibility != nil {
		next.Visibility = terminals.Visibility(*d.Visibility)
	}
	if d.Publish != nil {
		switch *d.Publish {
		case "curated": // → broadcast
			next.Publish = terminals.PublishCurated
			next.Join = terminals.JoinNone
			// The owner's binding must always exist among the writers.
			owner := terminals.WriterBinding{Principal: r.Principal.ID, Device: r.Device.ID}
			if !next.AllowsWriter(owner.Principal, owner.Device) {
				next.Writers = append(next.Writers, owner)
			}
		case "all", "": // → open community
			next.Publish = terminals.PublishAll
			next.Join = terminals.JoinOpen
			next.Writers = nil
		default:
			return errors.New("node: unknown publish mode")
		}
	}
	if d.AddCurator != nil {
		next.Writers = append(next.Writers, *d.AddCurator)
	}
	if d.RemoveCurator != nil {
		// Remove the EXACT (principal, device) binding — never a whole
		// principal implicitly.
		kept := next.Writers[:0]
		for _, w := range next.Writers {
			if w != *d.RemoveCurator {
				kept = append(kept, w)
			}
		}
		next.Writers = kept
		// Canonicalization keeps the owner; guard against removing the
		// last writer of a curated space anyway.
		owner := terminals.WriterBinding{Principal: r.Principal.ID, Device: r.Device.ID}
		if next.Publish == terminals.PublishCurated && !next.AllowsWriter(owner.Principal, owner.Device) {
			next.Writers = append(next.Writers, owner)
		}
	}
	if d.Frozen != nil {
		next.Frozen = *d.Frozen
	}
	if err := st.space.ReviseManifest(title, character, next); err != nil {
		return err
	}
	meta.ManifestFrame = st.space.ManifestFrame
	meta.Visibility = string(st.space.Policy().Effective())
	r.ks.Spaces[tid] = meta
	return r.saveKeystore()
}

// ---- API ----

func (a *APIServer) handlePublicOpen(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Link string `json:"link"`
	}](r)
	if err != nil || strings.TrimSpace(body.Link) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("link required"))
		return
	}
	tid, err := a.rt.OpenPublicLink(strings.TrimSpace(body.Link))
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": tid.Hex()})
}

func (a *APIServer) handlePublicLink(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	link, err := a.rt.ComposePublicLink(tid)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"link": link})
}

func (a *APIServer) handleRevisePolicy(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Visibility    *string                             `json:"visibility"`
		Publish       *string                             `json:"publish"`
		AddCurator    *struct{ Principal, Device string } `json:"add_curator"`
		RemoveCurator *struct{ Principal, Device string } `json:"remove_curator"`
		Frozen        *bool                               `json:"frozen"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	parseBinding := func(p, d string) (*terminals.WriterBinding, error) {
		prin, err := id.ParsePrincipalID(strings.TrimSpace(p))
		if err != nil {
			return nil, errors.New("bad curator principal id")
		}
		dev, err := id.ParseDeviceID(strings.TrimSpace(d))
		if err != nil {
			return nil, errors.New("bad curator device id")
		}
		return &terminals.WriterBinding{Principal: prin, Device: dev}, nil
	}
	delta := PolicyDelta{Visibility: body.Visibility, Publish: body.Publish, Frozen: body.Frozen}
	if body.AddCurator != nil {
		b, err := parseBinding(body.AddCurator.Principal, body.AddCurator.Device)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		delta.AddCurator = b
	}
	if body.RemoveCurator != nil {
		b, err := parseBinding(body.RemoveCurator.Principal, body.RemoveCurator.Device)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		delta.RemoveCurator = b
	}
	if err := a.rt.RevisePolicy(tid, delta); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"status": "revised"})
}

func (a *APIServer) handlePublicJoin(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.JoinPublicSpace(tid); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"status": "joined"})
}

// publishPublicProjection builds, versions, and Replaces the owner's public
// projection at the relay (I4 single writer, durable seq). The seq bumps
// exactly when the content digest changes; heartbeats republish the same
// seq. The durable state is persisted BEFORE the relay accepts the new
// sequence, so a crash can never mint a reused seq.
//
// Content changes are DISCOVERED by building: log growth, custody flips
// (an excluded media publication becoming ready), truncation-window aging —
// all show up as a digest change. When the digest is unchanged, the relay
// is touched only when force (bucket rotation / heartbeat) says so.
func (r *Runtime) publishPublicProjection(addr string, tid id.TerminalID) error {
	_, err := r.publishPublicProjectionForce(addr, tid, true)
	return err
}

// publishPublicProjectionForce reports whether the relay was actually
// touched (so the caller can reset its heartbeat timer).
func (r *Runtime) publishPublicProjectionForce(addr string, tid id.TerminalID, force bool) (bool, error) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return false, errors.New("node: unknown space")
	}
	if r.ks.PublicPublish == nil {
		r.ks.PublicPublish = map[id.TerminalID]storage.PublicPublishState{}
	}
	pp := r.ks.PublicPublish[tid]
	now := uint64(time.Now().Unix())
	seq := pp.ProjectionSeq
	if seq == 0 {
		seq = 1
	}
	lim := terminals.DefaultProjectionLimits()
	lim.Exclude = r.assetIncompleteExclude(tid) // custody gate (0.4D)
	wire, digest, err := st.space.BuildPublicProjection(seq, r.Device.ID, now, lim)
	if err != nil {
		r.mu.Unlock()
		return false, err
	}
	changed := digest != pp.LastContentDigest
	if changed {
		// Content changed → new sequence, re-sign, persist BEFORE publish.
		seq = pp.ProjectionSeq + 1
		wire, digest, err = st.space.BuildPublicProjection(seq, r.Device.ID, now, lim)
		if err != nil {
			r.mu.Unlock()
			return false, err
		}
		r.ks.PublicPublish[tid] = storage.PublicPublishState{
			PublisherDevice: r.Device.ID,
			ProjectionSeq:   seq, LastContentDigest: digest,
		}
		if err := r.saveKeystore(); err != nil {
			r.mu.Unlock()
			return false, err
		}
	}
	r.mu.Unlock()
	if !changed && !force {
		return false, nil // nothing new and no heartbeat due — spare the relay
	}

	if err := r.relayGate(); err != nil {
		return false, err
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return false, err
	}
	defer client.Close()
	expires := now + uint64(DefaultRelayTTL/time.Second)
	_, err = client.Replace(relay.HintPublicOutbox(tid, relay.Bucket(now)), expires, wire)
	return err == nil, err
}
