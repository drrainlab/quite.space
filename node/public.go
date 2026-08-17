// Public-space node runtime (PA-0.4B): the projection publisher (owner side)
// and the reader replica (stranger side). The owner pushes its signed
// projection to the relay's public outbox (atomic Replace, durable monotonic
// seq, heartbeat); a reader opens a replica from just the space id + relay
// address and installs verified projections into its projection store.
package node

import (
	"encoding/hex"
	"errors"
	"fmt"
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
	//
	// The link's relay is recorded as the space's SourceRelay RIGHT HERE,
	// before any fetch (RR-tail): until a projection installs there is no
	// signed relay set to resolve, and without this the reader's sync loop
	// would fall back to his PERSONAL relay — an address where this space
	// was never published. A publisher who is briefly offline at the moment
	// somebody opens the link would then never be found again. The link is
	// itself an observation of where the space says it lives; treating it
	// as one is what makes the first fetch a retry rather than a lottery.
	r.ks.Spaces[spaceID] = storage.SpaceMeta{
		Title: "public space", Role: storage.RoleReader,
		SourceRelay: relayAddr,
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
	// Bulk lane (RR-2): a projection is up to 768 KiB and must not block
	// the control lane's collects behind it.
	if err := r.withRelayBulk(addr, func(client *relay.Client) error {
		return r.fetchPublicProjectionWith(client, tid)
	}); err != nil {
		return err
	}
	// A projection ARRIVED from this address: remember it as the space's
	// source relay (PS-1). An observation, not a setting — a reference
	// composed later prefers it over the global relay, because this is the
	// address the publisher demonstrably writes to. Every projection path
	// funnels through here with the addr in hand, which is why the note
	// lives here and not at the call sites.
	r.mu.Lock()
	if meta, ok := r.ks.Spaces[tid]; ok && meta.SourceRelay != addr {
		meta.SourceRelay = addr
		r.ks.Spaces[tid] = meta
		_ = r.saveKeystore()
	}
	r.mu.Unlock()
	return nil
}

// bestProjectionFor reads a space's public outbox (current + previous
// bucket) and returns the newest verified envelope with its wire bytes.
// Shared by the replica sync and the transient preview (PS-3), so both
// pick the same envelope by the same rule.
func bestProjectionFor(client *relay.Client, tid id.TerminalID) (*projection.Envelope, []byte, error) {
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	hints := [][]byte{relay.HintPublicOutbox(tid, b)}
	if b > 0 {
		hints = append(hints, relay.HintPublicOutbox(tid, b-1))
	}
	items, err := client.Fetch(hints)
	if err != nil {
		return nil, nil, err
	}
	var best *projection.Envelope
	var bestWire []byte
	for _, item := range items {
		env, err := projection.Decode(item)
		if err != nil || env.SpaceID != tid {
			continue
		}
		if projection.Verify(env) != nil {
			continue
		}
		if best == nil || env.Seq > best.Seq {
			best, bestWire = env, item
		}
	}
	if best == nil {
		return nil, nil, ErrNoProjection
	}
	return best, bestWire, nil
}

func (r *Runtime) fetchPublicProjectionWith(client *relay.Client, tid id.TerminalID) error {
	best, bestWire, err := bestProjectionFor(client, tid)
	if err != nil {
		return err
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
	// Retain the envelope VERBATIM. A mirror must republish the owner's exact
	// signed bytes — re-encoding a decoded envelope is not an option, since
	// only the space key can produce a valid signature. Also remember where
	// this space says contributions go (PH-2): the address is the publisher's
	// to choose, and deriving it locally is exactly what PH-1 took away.
	st.projWire = append([]byte(nil), bestWire...)
	st.projSeq = best.Seq
	st.ingressHints = best.IngressHints
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
		mine := terminals.WriterBinding{Principal: r.PrincipalID, Device: r.Device.ID}
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
// paste box serves both artifact kinds. The envelope grammar is
// space:<tid hex>[:<doc hex>] — the optional third field is a LANDING
// HINT naming a publication inside the space, never a capability of its
// own: what the holder can read is decided by the space's policy alone.
const publicLinkPrefix = "space:"

// ParsePublicLink is the pure half of link handling: it opens nothing,
// writes nothing, and dials nothing. Everything that ACTS on a link —
// the paste path, the preview, Follow — starts here, so there is exactly
// one reading of the grammar.
func ParsePublicLink(link string) (relayAddr string, tid id.TerminalID, doc *[16]byte, err error) {
	relayAddr, envelope, err := splitShare(link)
	if err != nil {
		return "", id.TerminalID{}, nil, err
	}
	if !strings.HasPrefix(envelope, publicLinkPrefix) {
		return "", id.TerminalID{}, nil, errors.New("node: not a public space link")
	}
	rest := strings.TrimPrefix(envelope, publicLinkPrefix)
	idHex, docHex, hasDoc := strings.Cut(rest, ":")
	tid, err = id.ParseTerminalID(idHex)
	if err != nil {
		return "", id.TerminalID{}, nil, errors.New("node: malformed space id in link")
	}
	if hasDoc {
		b, err := hex.DecodeString(docHex)
		if err != nil || len(b) != 16 {
			return "", id.TerminalID{}, nil, errors.New("node: malformed document id in link")
		}
		var d [16]byte
		copy(d[:], b)
		doc = &d
	}
	return relayAddr, tid, doc, nil
}

// ComposePublicLink builds the share link of a public space, optionally
// pointing at one publication in it. IRREVOCABLE by design — whoever
// learns the id derives every future mailbox hint and reads forever; that
// is the declared semantics of the tier.
//
// The relay in the link prefers the address a projection for this space
// actually ARRIVED from over the global setting: a reader forwarding
// somebody else's post would otherwise mint a well-formed link pointing
// at their own relay, which the publisher may never write to.
func (r *Runtime) ComposePublicLink(tid id.TerminalID, doc *[16]byte) (string, error) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	r.mu.Unlock()
	if !ok {
		return "", errors.New("node: unknown space")
	}
	if !st.space.Policy().IsPublic() {
		return "", errors.New("node: not a public space")
	}
	// The space's own read ladder: its signed relay set, then the address a
	// projection actually arrived from, then this node's own. It used to
	// read the observed address and fall back to Settings.Relay, which
	// refused to compose ANY share link on a node in automatic mode — where
	// that field is empty by design. The resolver covers both, in order,
	// and takes r.mu itself, so it is called after the read above.
	relayAddr := r.ResolvePublicReadRelay(tid)
	if relayAddr == "" {
		return "", errors.New(
			"node: this space has no reachable relay yet — the link carries one, " +
				"so there is nothing to put in it")
	}
	envelope := publicLinkPrefix + tid.Hex()
	if doc != nil {
		envelope += ":" + hex.EncodeToString(doc[:])
	}
	return composeShare(relayAddr, envelope), nil
}

// CanReferenceByPublicLink is the PS wave's eligibility predicate, named
// so it cannot quietly narrow to one enum value. The property is not
// abstract publicness but: a holder of this link reads without approval
// and without membership. Today that is IsPublic() (unlisted OR public,
// both link-readable) minus LocalOnly — a space that must never leave
// this device can never be pointed at from outside it.
func (r *Runtime) canReferenceByPublicLinkLocked(tid id.TerminalID) bool {
	st, ok := r.spaces[tid]
	if !ok {
		return false
	}
	if r.ks.Spaces[tid].LocalOnly {
		return false
	}
	return st.space.Policy().IsPublic()
}

// OpenPublicLink parses a public share link and opens the reader replica.
// This is the DELIBERATE PASTE path: it may adopt the link's relay when
// none is configured, which is fine for an address a person typed and not
// fine for one that arrived inside a message — the preview and Follow
// paths never come through here.
func (r *Runtime) OpenPublicLink(link string) (id.TerminalID, error) {
	relayAddr, tid, _, err := ParsePublicLink(link)
	if err != nil {
		return id.TerminalID{}, err
	}
	if err := r.OpenPublicSpace(tid, relayAddr); err != nil {
		return id.TerminalID{}, err
	}
	// Remember the relay for the background loop if none is configured yet
	// — and "not configured yet" is NOT the same question it was when this
	// was written.
	//
	// RR changed what an empty Relay means. In automatic mode it is the
	// NORMAL state: the measured selection keeps its result in relays.json
	// and deliberately leaves Settings.Relay blank, because Settings holds
	// the person's decisions and a measurement is not one. So writing an
	// address here would not fill a gap; it would take a node OUT of
	// automatic mode and pin it, permanently and silently, to a relay named
	// by whoever wrote the link. The relay stays blind to content, but it
	// gains the metadata position and the power to withhold — bought with
	// a paste the person meant as "show me this space".
	//
	// This is the same class as the six call sites the RR tail fixed
	// (71b8776): code reading Settings.Relay as though empty meant
	// unconfigured. This one was missed because it reads as an act of
	// helpfulness rather than a resolver.
	//
	// A node in CUSTOM mode with a blank address genuinely has nowhere to
	// go, and a deliberate paste is a reasonable moment to offer one — so
	// that case still adopts, which is the behaviour this line was added
	// for in the first place.
	if s := r.GetSettings(); s.Relay == "" && !relayIsAutomatic(s) {
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
	r.publishCertLocked(st.space)
	return r.saveKeystore()
}

// pushPublicIngress uploads this replica's pending contribution to its
// deterministic ingress shard: unacked local frames (contributors) and/or
// media wants (readers and contributors alike — wants need no membership).
// The cumulative bundle is byte-stable between changes, so the relay's
// content-idempotent Put keeps exactly one slot however often we retry.
// ErrNoProjection is "the owner has not published this space yet, or
// their outbox has aged out of the relay's RAM". It is ROUTINE — a
// contributor whose counterpart is offline is not a broken relay — and it
// used to be recognised by comparing err.Error() to a literal. That worked
// on the one path that compared it and silently failed on the path that
// wrapped it with %w, which is how a normal quiet afternoon started
// showing "relay - issue" to somebody whose messages were all arriving.
// A sentinel travels through wrapping; a string does not.
var ErrNoProjection = errors.New("node: no projection available at the relay")

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
	now0 := uint64(time.Now().Unix())
	// The address comes from the publisher's signed projection (PH-2). An
	// owner publishing its own space derives it directly; everyone else must
	// have installed a projection first, which they have by definition — a
	// want only exists for an asset a projected frame referenced.
	hint := r.ingressHintLocked(st, tid, self, relay.Bucket(now0))
	// Mint (or reuse) the box this space's media answers come back to. Only
	// its HINT travels; the capability never leaves this process.
	var box []byte
	if len(wants) > 0 {
		if c := r.replyBoxCapLocked(tid, relay.Bucket(now0)); c != nil {
			box = relay.CollectHint(c)
		}
	}
	r.mu.Unlock()
	if len(frames) == 0 && len(wants) == 0 {
		return nil
	}
	body := bundle.EncodeWithReplyBox(tid, frames, nil, wants, self[:], box)
	if err := r.relayGate(); err != nil {
		return err
	}
	lane := r.withRelayControl
	if len(body) >= bulkThreshold {
		lane = r.withRelayBulk
	}
	return lane(addr, func(client *relay.Client) error {
		if hint == nil {
			// No address yet — the usual cause is a restart, which keeps the
			// durable pending set but not the in-memory projection. Fetch one and
			// re-resolve rather than failing: the contribution is legitimate and
			// the address is one round trip away.
			if err := r.fetchPublicProjectionWith(client, tid); err != nil {
				return fmt.Errorf("node: no ingress address and none could be fetched: %w", err)
			}
			r.mu.Lock()
			if st, ok := r.spaces[tid]; ok {
				hint = r.ingressHintLocked(st, tid, self, relay.Bucket(now0))
			}
			r.mu.Unlock()
			if hint == nil {
				return errors.New("node: this space publishes no ingress address")
			}
		}
		now := uint64(time.Now().Unix())
		// Short TTL: ingress is consumed by the owner within a bucket or
		// re-pushed; stale bundles must not linger against relay quotas.
		_, err := client.Put(hint, now+6*3600, body)
		return err
	})
}

// collectPublicIngress drains all ingress shards of an OWNED public space
// (current + previous bucket): absorbs contributed frames through the
// canonical gates (admission, chains, dedup), answers media wants, and
// starts custody fetches for newly referenced assets (PA-0.4D).
func (r *Runtime) collectPublicIngress(addr string, tid id.TerminalID) (int, error) {
	got, err := r.collectPublicIngressBatch(addr, []id.TerminalID{tid})
	return got[tid], err
}

// collectPublicIngressBatch drains SEVERAL owned public spaces' ingress
// shards through ONE Collect (RR-2). This batching is safe because the
// capability class is homogeneous: every item in an ingress mailbox is a
// contributor bundle that NAMES its space (parts.Terminal) — the cap it
// sat behind adds no information, so items route by content and each
// space's community gates (per-author budgets, rejected ring, frame caps)
// apply per space exactly as before.
func (r *Runtime) collectPublicIngressBatch(addr string, tids []id.TerminalID) (map[id.TerminalID]int, error) {
	applied := map[id.TerminalID]int{}
	// Roots per space, frozen spaces skipped (TRUE freeze: not read at all).
	roots := map[id.TerminalID][32]byte{}
	// The owner's signed contribution limit, read once per drain rather than
	// per frame: ParsePolicy re-decodes the manifest on every call.
	limits := map[id.TerminalID]int{}
	r.mu.Lock()
	for _, tid := range tids {
		if st, ok := r.spaces[tid]; ok {
			pol := st.space.Policy()
			if pol.Frozen {
				continue
			}
			if root, ok := st.space.IngressRoot(); ok {
				roots[tid] = root
				limits[tid] = pol.MaxFramesPerAuthor
			}
		}
	}
	r.mu.Unlock()
	if len(roots) == 0 {
		return applied, nil
	}
	if err := r.relayGate(); err != nil {
		return applied, err
	}
	// Pooled control lane (RR-2).
	client, release, err := r.pool().Control(addr)
	if err != nil {
		return applied, err
	}
	var opErr error
	defer func() { release(opErr) }()
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	// Current, previous and next bucket per space: a contributor may run a
	// slightly fast or stale clock, and losing a contribution to a rounding
	// edge would be indistinguishable from censorship. Deterministic order.
	buckets := []uint64{b, b + 1}
	if b > 0 {
		buckets = append(buckets, b-1)
	}
	ordered := make([]id.TerminalID, 0, len(roots))
	for _, tid := range tids {
		if _, ok := roots[tid]; ok {
			ordered = append(ordered, tid)
		}
	}
	var caps [][]byte
	for _, tid := range ordered {
		root := roots[tid]
		for _, bk := range buckets {
			for sh := byte(0); sh < relay.IngressShards; sh++ {
				caps = append(caps, relay.CapPublicIngress(root, bk, sh))
			}
		}
	}
	// The server bounds caps per request; split when many spaces overflow it.
	var items [][]byte
	for off := 0; off < len(caps); off += maxCapsPerCollect {
		end := min(off+maxCapsPerCollect, len(caps))
		part, err := client.Collect(caps[off:end])
		if err != nil {
			opErr = err
			return applied, err
		}
		items = append(items, part...)
	}
	nowT := time.Now()
	budgets := map[id.TerminalID]*authorBudget{} // per-space per-cycle caps
	accs := map[id.TerminalID]*wantAccumulator{} // per-space reply answers
	for _, item := range items {
		parts, err := bundle.DecodeParts(item)
		if err != nil {
			continue
		}
		tid := parts.Terminal
		if _, mine := roots[tid]; !mine {
			continue // not one of the drained spaces
		}
		if budgets[tid] == nil {
			budgets[tid] = newAuthorBudget(limits[tid])
			accs[tid] = newWantAccumulator()
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
		// Decision C's pre-pass: the batch's own proofs become trust before
		// any of its frames are judged, so a manifest that precedes its
		// author's certificate in slice order still admits in this cycle.
		r.learnBundleProofs(parts.Frames)
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
			if !budgets[tid].admit(dev, len(f)) {
				continue // over budget this cycle — arrives on a later one
			}
			// The owner's own limit, checked here so an over-limit frame
			// costs no verification — but CHARGED only below, once the
			// frame has proved whose it is. Charging on the claim would
			// let a forged device id spend a real contributor's share.
			if !budgets[tid].withinPolicy(dev) {
				st.throttled++
				continue // the owner's rate limit — arrives on a later cycle
			}
			n, err := st.space.Absorb(f)
			if err != nil {
				// Memoized ONLY when terminal — a hold-class refusal is a
				// delay, and the ring itself enforces that (MD-0b): the
				// contributor keeps re-pushing unacked frames, so the retry
				// costs nothing here.
				st.rejected.rememberRefusal(eid, err, nowT)
				continue // admission-refused or malformed: PolicyStats has it
			}
			if n > 0 {
				// Charged for frames that actually became content, which is
				// stricter than "Absorb did not error" in the one way that
				// matters: a REPLAY of somebody's already-accepted frames
				// absorbs cleanly and applies nothing, and charging it would
				// let anyone spend a named contributor's allowance by
				// re-pushing their own words back at the space.
				budgets[tid].charge(dev)
			}
			applied[tid] += n
		}
		r.mu.Unlock()
		// Custody (0.4D): any asset referenced by newly contributed block
		// frames gets fetched from its author through the existing member
		// wants machinery; until the blob is verified locally the
		// projection's Exclude filter keeps the publication unprojected.
		r.requestIncompleteAssets(tid, parts.Frames)
		accs[tid].add(parts.Wanter, parts.Wants, parts.ReplyBox)
	}
	for _, tid := range ordered {
		if acc := accs[tid]; acc != nil {
			acc.answer(r, client, tid, true)
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
//
// TWO KINDS OF REFERENCE, AND A MIRROR NEEDS BOTH. ExtractAssetRefs reads
// BLOCK schemas — the pictures, voice notes and video of a conversation. A
// PUBLICATION references its media differently: a document names asset ids
// inside its own structure (cover, images, audio, video, gallery items), and
// no block schema mentions them at all.
//
// So a mirror used to take custody of a space's chat and none of its posts.
// Measured on the demo catalog with the owner switched off: the mirror held
// exactly the three visuals of the feed, while all three posts pointed at
// cover ids it had never heard of — and every screen showed "no online
// source" for the one thing a catalogue is made of.
func (r *Runtime) requestIncompleteAssets(tid id.TerminalID, frames [][]byte) {
	want := func(aid string) {
		if aid == "" {
			return
		}
		if st, err := r.AssetStatus(tid, aid); err == nil && st.State != assets.StateComplete {
			_ = r.RequestAsset(tid, aid)
		}
	}
	for _, f := range frames {
		env, err := signal.Decode(f)
		if err != nil || env.PayloadEncoding != signal.PayloadCBOR {
			continue
		}
		for _, ref := range schemas.ExtractAssetRefs(env.Schema, env.Payload) {
			if ref != nil {
				want(ref.PublicIDHex())
			}
		}
	}
	// The documents, from the materialized state rather than from the frames:
	// a publication arrives as revisions, and what matters is the CURRENT
	// document — which is exactly what a reader will ask for.
	//
	// COLLECTED UNDER THE LOCK, ASKED FOR OUTSIDE IT. RequestAsset takes r.mu
	// itself, so calling it from inside withSpace deadlocks the runtime — the
	// same reason this whole function runs outside the lock in mirrorKeepalive.
	var docAssets []string
	_ = r.withSpace(tid, func(st *spaceState) error {
		for _, p := range st.space.State.Publications() {
			if p.Document == nil {
				continue
			}
			for aid := range p.Document.LiveAssetIDs() {
				docAssets = append(docAssets, aid)
			}
		}
		return nil
	})
	for _, aid := range docAssets {
		want(aid)
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
	// Relays replaces the signed relay set (RR-5): RelayRef strings,
	// first = primary, at most two. Nil = unchanged; empty = clear.
	Relays *[]string
	// MaxFramesPerAuthor sets the contribution limit (IC-1). Nil = unchanged;
	// 0 = back to the built-in defence cap. The pointer is what keeps those
	// two apart.
	MaxFramesPerAuthor *int
	// Kind declares what the space is for (CAT-0b). Nil = unchanged; ""
	// takes the declaration back. Same pointer reason as the limit above.
	Kind *string
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
	if meta.LocalOnly {
		return ErrLocalOnly
	}
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
			owner := terminals.WriterBinding{Principal: r.PrincipalID, Device: r.Device.ID}
			if !next.AllowsWriter(owner.Principal, owner.Device) {
				next.Writers = append(next.Writers, owner)
			}
			// A contribution limit is an open-community control. Leaving it
			// set here would make a later flip back to community silently
			// resurrect a number the owner has long forgotten.
			next.MaxFramesPerAuthor = 0
		case "all", "": // → open community
			next.Publish = terminals.PublishAll
			next.Join = terminals.JoinOpen
			next.Writers = nil
		default:
			return errors.New("node: unknown publish mode")
		}
		// next.Kind is deliberately NOT touched by either arm. The rate limit
		// above is cleared because it belongs to one mode; a purpose belongs
		// to the space. A directory that opens itself to contributors is
		// still a directory, and the code right above teaches the opposite
		// reflex to whoever edits this next.
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
		owner := terminals.WriterBinding{Principal: r.PrincipalID, Device: r.Device.ID}
		if next.Publish == terminals.PublishCurated && !next.AllowsWriter(owner.Principal, owner.Device) {
			next.Writers = append(next.Writers, owner)
		}
	}
	if d.Frozen != nil {
		next.Frozen = *d.Frozen
	}
	if d.Relays != nil {
		// Refs are validated by SpacePolicy.Validate inside ReviseManifest;
		// the grammar is checked HERE too so the error names the field.
		for _, ref := range *d.Relays {
			if _, err := ParseRelayRef(ref); err != nil {
				return err
			}
		}
		next.Relays = append([]string(nil), (*d.Relays)...)
	}
	if d.MaxFramesPerAuthor != nil {
		// Bounded by SpacePolicy.Validate inside ReviseManifest; checked here
		// too so the error names the field and its range.
		if n := *d.MaxFramesPerAuthor; n != 0 &&
			(n < terminals.MinFramesPerAuthor || n > terminals.MaxFramesPerAuthor) {
			return fmt.Errorf("node: a contribution limit is %d..%d frames per cycle, or 0 for none",
				terminals.MinFramesPerAuthor, terminals.MaxFramesPerAuthor)
		}
		next.MaxFramesPerAuthor = *d.MaxFramesPerAuthor
	}
	if d.Kind != nil {
		// Checked here as well as in Validate so the error names the field
		// rather than arriving as a policy refusal — the same courtesy the
		// relay grammar and the rate range already get.
		if k := *d.Kind; k != terminals.SpaceKindOrdinary && k != terminals.SpaceKindDirectory {
			return fmt.Errorf("node: %q is not a purpose this build knows", k)
		}
		next.Kind = *d.Kind
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
	link, err := a.rt.ComposePublicLink(tid, nil)
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
		Relays        *[]string                           `json:"relays"`
		RatePerCycle  *int                                `json:"rate_per_cycle"`
		Kind          *string                             `json:"kind"`
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
	delta := PolicyDelta{Visibility: body.Visibility, Publish: body.Publish,
		Frozen: body.Frozen, Relays: body.Relays,
		MaxFramesPerAuthor: body.RatePerCycle, Kind: body.Kind}
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
	// PH-2: advertise where contributions and media wants should go. Two
	// buckets so a reader holding a slightly stale projection still lands in
	// a shard we drain; the drain capability itself stays here.
	if root, ok := st.space.IngressRoot(); ok {
		b := relay.Bucket(now)
		for _, bk := range []uint64{b, b + 1} {
			for sh := byte(0); sh < relay.IngressShards; sh++ {
				lim.IngressHints = append(lim.IngressHints,
					relay.CollectHint(relay.CapPublicIngress(root, bk, sh)))
			}
		}
	}
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
	// Bulk lane (RR-2): a projection Replace carries up to 768 KiB.
	err = r.withRelayBulk(addr, func(client *relay.Client) error {
		expires := now + uint64(DefaultRelayTTL/time.Second)
		_, perr := client.Replace(relay.HintPublicOutbox(tid, relay.Bucket(now)), expires, wire)
		return perr
	})
	return err == nil, err
}

// ingressHintLocked picks this device's ingress address for a bucket. The
// owner derives it from its own root; everyone else reads it from the
// installed projection, because after PH-1 the address is the publisher's
// to choose and a locally derived one would simply be a mailbox nobody
// drains. Caller holds r.mu.
func (r *Runtime) ingressHintLocked(st *spaceState, tid id.TerminalID,
	self id.DeviceID, bucket uint64) []byte {

	shard := relay.IngressShard(self)
	if root, ok := st.space.IngressRoot(); ok {
		return relay.CollectHint(relay.CapPublicIngress(root, bucket, shard))
	}
	// Published layout: [current bucket × 8 shards][next bucket × 8 shards].
	if len(st.ingressHints) >= relay.IngressShards {
		return st.ingressHints[int(shard)]
	}
	return nil
}
