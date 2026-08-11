// Store-and-forward via blind relay (M1.5): push a space's frames to a
// relay under its rotating hint; an offline peer pulls them later. The
// relay sees a hint and a ciphertext bundle — payloads are epoch-encrypted
// (ADR-005) and the trust engine records exactly accepted_by_relay for
// pushed events, never delivery (ADR-008).
package node

import (
	"errors"
	"log"
	"sort"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// DefaultRelayTTL bounds how long pushed bundles wait for their recipient.
const DefaultRelayTTL = 48 * time.Hour

// AssetPolicy controls which asset blobs a bundle export carries.
type AssetPolicy string

const (
	AssetsNone      AssetPolicy = "none"      // no manifests, no chunks
	AssetsManifests AssetPolicy = "manifests" // locally available manifests only
	AssetsAvailable AssetPolicy = "available" // manifests + local chunks up to budget
)

// DefaultBundleBudget bounds a dead-drop bundle (must fit relay item limits).
const DefaultBundleBudget = 8 << 20

// maxRelayItem caps one relay item's payload. The relay wire carries each item
// as a single CBOR byte string, bounded by codec.MaxItemLen (1 MiB); we stay
// safely under it so large media answers split into several decodable items.
const maxRelayItem = 768 << 10

// bundleHeadroom leaves room for the bundle's own CBOR framing — the map, the
// arrays, the terminal id, and the want list — so a batch measured by payload
// bytes alone still encodes under the item cap.
const bundleHeadroom = 8 << 10

// splitBundles packs a space's frames and blobs into relay items that each fit
// under maxRelayItem.
//
// The relay carries one item as a single CBOR byte string bounded by
// codec.MaxItemLen, so a whole-log bundle is a working design only while the
// log is small. It is not a threshold a space approaches gently, either: once
// past it NOTHING is delivered, to anybody, and it never recovers on its own
// because the bundle only ever grows. The media path next door has always
// known this (see answerWants); this is the same rule for frames.
//
// The wants ride on the FIRST item only — asking once is asking.
//
// Returns the bodies in order, and how many frames were too large to travel at
// all. A frame bigger than one item cannot be split here: it is one signed
// object, and cutting it would produce something no verifier would accept.
func splitBundles(tid id.TerminalID, frames, blobs, wants [][]byte, wanter []byte) ([][]byte, int) {
	limit := maxRelayItem - bundleHeadroom
	var out [][]byte
	var batch [][]byte
	oversize, size := 0, 0

	flush := func(isFirst bool) {
		if len(batch) == 0 {
			return
		}
		if isFirst {
			out = append(out, bundle.EncodeWithWants(tid, batch, nil, wants, wanter))
		} else {
			out = append(out, bundle.Encode(tid, batch))
		}
		batch, size = nil, 0
	}
	for _, f := range frames {
		if len(f) > limit {
			oversize++
			continue
		}
		if size+len(f) > limit {
			flush(len(out) == 0)
		}
		batch = append(batch, f)
		size += len(f)
	}
	flush(len(out) == 0)

	// Blobs travel the same way and under the same cap. collectBlobs works to
	// its own multi-megabyte budget, which was never a size ONE item could
	// carry — so its output is batched here rather than trusted.
	var bb [][]byte
	bsize := 0
	fb := func() {
		if len(bb) == 0 {
			return
		}
		out = append(out, bundle.EncodeWithBlobs(tid, nil, bb))
		bb, bsize = nil, 0
	}
	for _, b := range blobs {
		if len(b) > limit {
			continue // one blob past the cap; the on-demand path handles it
		}
		if bsize+len(b) > limit {
			fb()
		}
		bb = append(bb, b)
		bsize += len(b)
	}
	fb()

	// Nothing to send but a question still to ask: the wants must go anyway,
	// or a node with no unacked frames could never request media at all.
	if len(out) == 0 && (len(wants) > 0 || len(frames) > 0 || len(blobs) > 0) {
		out = append(out, bundle.EncodeWithWants(tid, nil, nil, wants, wanter))
	}
	return out, oversize
}

// ExportReport honestly describes what a bundle carries.
type ExportReport struct {
	CompleteAssets int
	PartialAssets  int
	Blobs          int
	BlobBytes      int
	Truncated      bool
}

// collectBlobs gathers asset blobs for a space per policy, in deterministic
// order (assets in first-seen event order, manifest first, chunks by
// index), stopping strictly before the byte budget. Partial assets are
// allowed — lazy retrieval completes them later.
func (r *Runtime) collectBlobs(space id.TerminalID, policy AssetPolicy, budget int) ([][]byte, ExportReport) {
	rep := ExportReport{}
	if policy == AssetsNone {
		return nil, rep
	}
	var blobs [][]byte
	add := func(h id.Hash) bool {
		data, err := r.root.GetBlob(h)
		if err != nil {
			return false
		}
		if rep.BlobBytes+len(data) > budget {
			rep.Truncated = true
			return false
		}
		blobs = append(blobs, data)
		rep.Blobs++
		rep.BlobBytes += len(data)
		return true
	}
	for _, key := range r.assetIdx.refOrder[space] {
		ref := r.assetIdx.refs[key]
		complete := true
		if ref.ManifestWireID != nil {
			if !add(*ref.ManifestWireID) {
				complete = false
			}
		}
		if policy == AssetsAvailable {
			chunks := ref.WireIDs()
			if ref.ManifestWireID != nil {
				if man, err := assets.LoadManifest(r.root, ref); err == nil {
					chunks = man.Chunks
				} else {
					chunks = nil
					complete = false
				}
			}
			for _, c := range chunks {
				if !add(c) {
					complete = false
				}
			}
		} else {
			complete = false // manifests-only never claims completeness
		}
		if complete {
			rep.CompleteAssets++
		} else {
			rep.PartialAssets++
		}
	}
	return blobs, rep
}

// PushToRelay is the MANUAL full dead-drop (the "push current space"
// button): frames + available asset chunks up to the budget — an explicit
// user action to hand everything over. The large-packet relay node carries
// it. The BACKGROUND auto-sync uses AssetsManifests instead (media bytes
// stay on-demand — pushing whole assets every cycle is what hung large
// spaces and blew the old 1 MiB packet cap).
func (r *Runtime) PushToRelay(addr string, tid id.TerminalID) (int, uint64, error) {
	// The gate is the FIRST thing, not merely the last thing before the
	// dial: a forbidden transport must produce no work, no frames prepared,
	// and above all no connection. Checking later would still let a space
	// with nothing to send skip the refusal entirely.
	if !r.TransportAllowed(TransportRelay, tid) {
		return 0, 0, ErrTransportBlocked{
			Transport: TransportRelay, Mode: r.connectivity().modeFor(tid)}
	}
	n, _, deadline, err := r.pushToRelay(addr, tid, AssetsAvailable)
	return n, deadline, err
}

// pushToRelay delivers every recipient's copy through ONE named relay — the
// manual push API and the single-relay tests. The background loop uses
// deliverSpace instead, which routes per recipient.
func (r *Runtime) pushToRelay(addr string, tid id.TerminalID, policy AssetPolicy) (int, int, uint64, error) {
	pushed, reached, _, deadline, err := r.deliverSpaceRouted(tid, policy,
		func(id.DeviceID) string { return addr })
	return pushed, reached, deadline, err
}

// deliverSpace is RT-0's egress: each recipient's copy goes to THAT
// recipient's route, recipients sharing an endpoint share a connection, and
// a recipient whose known routes are all down is COUNTED and held — never
// silently re-aimed at the sender's own relay in the hope that somebody
// listens there.
//
// THE LEGACY BOOTSTRAP POLICY, named and scoped. A device the book knows
// NOTHING about — a membership formed through a channel that carries no
// routes: a direct invite, a radio join, the pre-upgrade era — is delivered
// at this node's own relay, and that assumption is RECORDED as a legacy
// route the moment it is first used, so diagnostics show it for what it is
// and any real route supersedes it. That is the single-relay era's rule,
// kept for the relationships formed under it. What it must never become is
// a fallback for a device whose stated routes are merely unhealthy: falling
// from "Bob said B" to "my own relay then" is the divergence this wave
// removes, so known-but-down HOLDS — without exception. A peer route
// coinciding with our own ingress is not provenance: from "we both listened
// on A, A died, I moved to C" it does not follow that the peer is on C, and
// inferring it would be RT-0's disease back under a legacy label. Until T5's
// pre-advertised fallback, a dead stated route has exactly one honest
// answer: NO_ROUTE, said out loud.
//
// syncingAt is the endpoint the calling sync cycle is armed with — an
// EXECUTION CONTEXT for legacy single-address sync (relaySyncOnce, manual
// sync, tests), scoped to this one cycle. It backstops the bootstrap when
// the settings resolve to nothing, and it is never RouteBook knowledge: it
// must not create PeerRoutes or SelfIngress entries, must not survive the
// cycle, and is never returned by the general resolver.
func (r *Runtime) deliverSpace(tid id.TerminalID, policy AssetPolicy, syncingAt string) (pushed, reached, noRoute int, err error) {
	pushed, reached, noRoute, _, err = r.deliverSpaceRouted(tid, policy,
		func(dev id.DeviceID) string {
			r.mu.Lock()
			known := len(r.ks.PeerRoutes[dev])
			r.mu.Unlock()
			if known > 0 {
				if eps := r.PeerRoutesFor(dev); len(eps) > 0 {
					return eps[0]
				}
				return "" // stated routes exist and are all down: HOLD (until T5)
			}
			// Nothing known at all: the legacy bootstrap, recorded.
			if ep := r.ResolvePersonalRelay(); ep != "" {
				r.mu.Lock()
				r.recordPeerRouteLocked(dev, ep, "relay", storage.RouteLegacy)
				r.mu.Unlock()
				return ep
			}
			// The cycle's explicit endpoint: used, never recorded.
			return syncingAt
		})
	return pushed, reached, noRoute, err
}

// deliverSpaceRouted returns (framesPrepared, reached, noRoute, deadline,
// err). reached is how many peer inboxes actually received a copy — 0 means
// "nobody addressable yet" (a solo space, or a fresh joiner before its first
// pull), which is a clean no-op, not an error. The auto-sync loop keys its
// progress on reached so it retries rather than marking undelivered frames
// as sent. route answers "which endpoint carries this recipient's copy";
// "" means no route is known and the recipient is skipped and counted.
func (r *Runtime) deliverSpaceRouted(tid id.TerminalID, policy AssetPolicy,
	route func(id.DeviceID) string) (int, int, int, uint64, error) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return 0, 0, 0, 0, errors.New("node: unknown space")
	}
	var frames [][]byte
	var eventIDs []id.EventID
	self := r.Device.ID
	// Recipient devices: the union of who we can address. The controller
	// side knows invited devices immediately (Members(), populated at invite
	// time) — that reaches a peer who has never synced a frame yet. Every
	// replica ALSO learns a peer's device from the frames it authored (the
	// signed envelope carries Env.Device), which is how a joiner — whose
	// Members() map is empty (it is controller-only, see terminals/private.go)
	// — discovers the owner and can push back. Union of both, minus self.
	devSet := map[id.DeviceID]struct{}{}
	now := uint64(time.Now().Unix())
	// Custody filter (ADR-015): expired frames never spend relay storage or
	// later airtime; NoCustody frames refuse store-and-forward by author
	// declaration. The relay itself stays structure-blind — the PUSHER
	// excludes them.
	//
	// BUT A LINK IS NOT A PAYLOAD, and this filter used to sever
	// conversations. Presence is emitted as an ordinary event in the
	// per-device hash chain — it takes a Sequence, and everything the device
	// says afterwards carries its id as Previous — and it is ALSO declared
	// NoCustody with an expiry, because nobody wants a relay holding stale
	// presence. Skipping it therefore withheld the one frame the receiver
	// needs to advance: its log parked seq 7, 8, 9… in the reorder buffer
	// waiting for a seq 6 that every future push filtered out again. One
	// presence update and that device could never be heard through a relay
	// again — silently, with the sender's diagnostics reporting healthy.
	// Seen between a phone and a laptop on 2026-08-09.
	//
	// So the declaration is honoured for what it is about — not storing
	// stale STATE — and ignored where the frame has become structure.
	//
	// AND A FRESH ONE IS NOT STALE. Holding trailing presence back entirely
	// meant a status only ever reached anybody if its author happened to say
	// something afterwards, which is when it stopped being trailing. Two
	// people on a relay could sit there with each other's presence stuck on
	// the sending side. What the declaration is against is a relay HOLDING a
	// status past its life, so that is what is arranged: those frames travel
	// in their own bundle whose mailbox TTL is the frame's own expiry, and
	// the relay drops them by itself the moment they go stale. They are kept
	// out of the durable bundle because one Put carries one deadline, and
	// putting messages on a five-minute clock to carry a status would be the
	// worse trade by far.
	type candidate struct {
		frame     []byte
		id        id.EventID
		dev       id.DeviceID
		seq       uint64
		skippable bool
		expires   uint64
	}
	var cands []candidate
	needed := map[id.DeviceID]uint64{} // highest seq that must be reachable
	if err := st.space.Log.Replay(func(a eventlog.Applied) error {
		devSet[a.Env.Device] = struct{}{} // author is a member, custody aside
		skip := a.Env.Expired(now) || a.Env.ForwardingScope() == signal.NoCustody
		if !skip && a.Env.Sequence > needed[a.Env.Device] {
			needed[a.Env.Device] = a.Env.Sequence
		}
		cands = append(cands, candidate{a.Frame, a.ID, a.Env.Device,
			a.Env.Sequence, skip, a.Env.ExpiresAt})
		return nil
	}); err != nil {
		r.mu.Unlock()
		return 0, 0, 0, 0, err
	}
	var fleeting [][]byte    // frames that carry their own deadline
	var fleetingUntil uint64 // the earliest of those deadlines
	for _, c := range cands {
		if c.skippable && c.seq >= needed[c.dev] {
			// Nothing depends on it yet. It still goes — on its own clock.
			if c.expires > now {
				fleeting = append(fleeting, c.frame)
				if fleetingUntil == 0 || c.expires < fleetingUntil {
					fleetingUntil = c.expires
				}
			}
			continue
		}
		frames = append(frames, c.frame)
		eventIDs = append(eventIDs, c.id)
	}
	for dev := range st.space.Members() {
		devSet[dev] = struct{}{}
	}
	blobs, _ := r.collectBlobs(tid, policy, DefaultBundleBudget)
	// Ride an outstanding media request (if any) on the same bundle: wants =
	// blob hashes we are missing, wanter = our device so a holder knows which
	// inbox to answer into. Empty when nothing is pending (a plain bundle).
	wants := r.relayWantsLocked(tid)
	var wanter []byte
	if len(wants) > 0 {
		wanter = self[:]
	}
	bodies, oversize := splitBundles(tid, frames, blobs, wants, wanter)
	// The fleeting frames get a bundle of their own, so their short deadline
	// cannot land on anybody's messages.
	var fleetingBodies [][]byte
	if len(fleeting) > 0 {
		fleetingBodies, _ = splitBundles(tid, fleeting, nil, nil, nil)
	}
	// Per-recipient dead-drop: hand a copy to every OTHER member's own relay
	// inbox. The shared per-terminal mailbox is single-reader (destructive
	// Collect), so with many members polling one relay the first poller drains
	// everyone's mail; per-recipient boxes let all members sync concurrently.
	// Self is skipped — we already hold our own frames.
	delete(devSet, self)
	var recipients []id.DeviceID
	for dev := range devSet {
		recipients = append(recipients, dev)
	}
	r.mu.Unlock()

	if len(recipients) == 0 {
		// Nobody addressable yet (solo space, or a fresh joiner before its
		// first pull). Frames were prepared but delivered to no one — a clean
		// no-op. Reporting reached==0 lets the auto-sync loop retry rather
		// than mark these frames as handed off.
		return len(frames), 0, 0, 0, nil
	}
	if err := r.relayGate(); err != nil {
		return 0, 0, 0, 0, err
	}
	now = uint64(time.Now().Unix())
	bucket := relay.Bucket(now)
	expires := now + uint64(DefaultRelayTTL/time.Second)


	var deadline uint64
	biggest := 0
	for _, b := range bodies {
		if len(b) > biggest {
			biggest = len(b)
		}
	}
	withLane := r.withRelayControl
	if biggest >= bulkThreshold {
		withLane = r.withRelayBulk
	}

	// EACH RECIPIENT'S COPY GOES TO THAT RECIPIENT'S ROUTE (RT-0).
	// Recipients sharing an endpoint share one connection and one batch; a
	// recipient with no route is counted and skipped — never posted to the
	// sender's own relay in the hope that somebody happens to listen there.
	// The mailbox hint itself is route-independent (space, device, bucket),
	// so nothing about addressing changes — only which machine holds it.
	byEndpoint := map[string][]id.DeviceID{}
	noRoute := 0
	for _, dev := range recipients {
		ep := route(dev)
		if ep == "" {
			noRoute++
			continue
		}
		byEndpoint[ep] = append(byEndpoint[ep], dev)
	}

	reached := 0
	var sendErr error
	// Deterministic endpoint order, so failures are stable to read.
	eps := make([]string, 0, len(byEndpoint))
	for ep := range byEndpoint {
		eps = append(eps, ep)
	}
	sort.Strings(eps)
	for _, ep := range eps {
		devs := byEndpoint[ep]
		if err := withLane(ep, func(client *relay.Client) error {
			for _, dev := range devs {
				hint := relay.HintFor(tid, dev, bucket)
				// One mailbox, several items. A Collect drains them all and
				// the receiver folds each independently, so the only thing
				// the split costs is wire ops — and the alternative was a
				// space that stops delivering the day its log outgrows one
				// item.
				for _, b := range bodies {
					d, err := client.Put(hint, expires, b)
					if err != nil {
						return err
					}
					deadline = d
				}
				// A status, on its own clock. The relay forgets it when it
				// goes stale, which is the whole of what "no custody" was
				// protecting.
				for _, b := range fleetingBodies {
					if _, err := client.Put(hint, fleetingUntil, b); err != nil {
						return err
					}
				}
				reached++
			}
			return nil
		}); err != nil {
			// One endpoint down must not silence the others: keep going and
			// report the first failure — the loop retries the whole space.
			if sendErr == nil {
				sendErr = err
			}
		}
	}
	if reached == 0 && sendErr != nil {
		return 0, 0, noRoute, 0, sendErr
	}
	if oversize > 0 {
		// Said out loud rather than dropped in silence: a single frame past
		// the item cap can never travel this way, and nothing downstream can
		// tell that from a peer being offline.
		log.Printf("relay: %d frame(s) in space %s exceed the %d-byte item cap and were not pushed",
			oversize, tid.String()[:8], maxRelayItem)
	}

	if reached > 0 {
		// Record the honest receipt level for every pushed event: the relay
		// accepted them; nobody received anything yet.
		r.mu.Lock()
		for _, eid := range eventIDs {
			_ = st.space.Trust.RecordTransportReceipt(eid, tid, claims.DeliveryAcceptedByRelay)
		}
		r.mu.Unlock()
	}
	return len(frames), reached, noRoute, deadline, sendErr
}

// addRelayWants records blob hashes to request over the relay for a space
// (media on-demand with no direct peer). The next auto-sync push carries them.
func (r *Runtime) addRelayWants(tid id.TerminalID, hashes []id.Hash) {
	if len(hashes) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.relayWants[tid]
	if set == nil {
		set = map[id.Hash]struct{}{}
		r.relayWants[tid] = set
	}
	for _, h := range hashes {
		set[h] = struct{}{}
	}
}

// wantsBlobLocked says whether this node actually asked for a blob in this
// space — the gate on relay-carried blob ingest. Caller holds r.mu.
//
// A mirror's greedy pull and the ordinary on-demand fetch both register
// through addRelayWants, so this is the one question worth asking: not
// "may this peer speak to me" (a blind relay cannot answer that) but "did
// I ask for these bytes".
func (r *Runtime) wantsBlobLocked(tid id.TerminalID, h id.Hash) bool {
	_, ok := r.relayWants[tid][h]
	return ok
}

// clearRelayWants drops hashes from the want set (fetch completed or gave up),
// so we stop asking for them.
func (r *Runtime) clearRelayWants(tid id.TerminalID, hashes []id.Hash) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.relayWants[tid]
	if set == nil {
		return
	}
	for _, h := range hashes {
		delete(set, h)
	}
	if len(set) == 0 {
		delete(r.relayWants, tid)
	}
}

// relayWantsLocked returns the pending want hashes for a space as wire bytes.
// Caller holds r.mu.
func (r *Runtime) relayWantsLocked(tid id.TerminalID) [][]byte {
	set := r.relayWants[tid]
	if len(set) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(set))
	for h := range set {
		out = append(out, h[:])
	}
	return out
}

// answerWants pushes the blobs we hold for a peer's requested hashes into that
// peer's own relay inbox — the response half of relay media fetch. Blind: the
// relay still sees only an opaque hint and ciphertext. Bounded by the bundle
// budget; the requester re-asks for whatever did not fit.
// answerWants delivers held blobs to whoever asked for them. replyBox is
// PH-1's address: a mailbox the requester alone can drain. When it is
// absent we fall back to deriving the requester's inbox from its device id —
// correct for PRIVATE spaces, where membership already gates the derivation.
// For a public space there is no safe fallback: both ids travel in the
// clear, so an inbox derived from them is an inbox anyone can empty. A
// public request without a reply box therefore gets no answer, and saying
// that plainly is better than delivering into a box we know is open.
// wantAccumulator dedupes wanted hashes ACROSS the items of one drain
// cycle, per reply box (PM-0 hardening). Before it, every item carrying
// the same wants earned its own full answer — up to 8 MiB each — and a
// handful of duplicate bundles multiplied a holder's egress for free. One
// box, one deduped answer, one budget per cycle.
type wantAccumulator struct {
	byBox map[string]*boxWants
}

type boxWants struct {
	replyBox []byte
	wanter   []byte
	seen     map[string]bool
	wants    [][]byte
}

func newWantAccumulator() *wantAccumulator {
	return &wantAccumulator{byBox: map[string]*boxWants{}}
}

func (w *wantAccumulator) add(wanter []byte, wants [][]byte, replyBox []byte) {
	if len(wants) == 0 {
		return
	}
	key := string(replyBox)
	if key == "" {
		key = "w:" + string(wanter) // private path addresses by wanter
	}
	bw := w.byBox[key]
	if bw == nil {
		bw = &boxWants{replyBox: replyBox, wanter: wanter, seen: map[string]bool{}}
		w.byBox[key] = bw
	}
	for _, h := range wants {
		if !bw.seen[string(h)] {
			bw.seen[string(h)] = true
			bw.wants = append(bw.wants, h)
		}
	}
}

func (w *wantAccumulator) answer(r *Runtime, client *relay.Client, tid id.TerminalID, public bool) {
	for _, bw := range w.byBox {
		r.answerWants(client, tid, bw.wanter, bw.wants, bw.replyBox, public)
	}
}

// answerWantsRouted answers a private-space media want at the WANTER's own
// route rather than on the connection the want arrived through. The mailbox
// hint is route-independent — (space, device, bucket) — so the only decision
// here is which machine to Put it on, and the honest answer is "wherever the
// asker listens". No route known → no answer sent: the asker's own retry
// ladder keeps asking, and LAN/direct may serve them meanwhile — better than
// depositing bytes on a machine nobody reads.
func (r *Runtime) answerWantsRouted(tid id.TerminalID, wanter []byte, wants [][]byte) {
	if len(wanter) != len(id.DeviceID{}) {
		return
	}
	var dev id.DeviceID
	copy(dev[:], wanter)
	if dev == r.Device.ID {
		return
	}
	eps := r.PeerRoutesFor(dev)
	if len(eps) == 0 {
		return
	}
	// Bulk lane: answers carry media blobs.
	_ = r.withRelayBulk(eps[0], func(client *relay.Client) error {
		r.answerWants(client, tid, wanter, wants, nil, false)
		return nil
	})
}

func (r *Runtime) answerWants(client *relay.Client, tid id.TerminalID, wanter []byte, wants [][]byte, replyBox []byte, public bool) {
	if len(wants) == 0 {
		return
	}
	now := uint64(time.Now().Unix())
	var hint []byte
	switch {
	case len(replyBox) == relay.HintLen:
		hint = replyBox
	case public:
		return // no safe address; the requester re-asks with a reply box
	case len(wanter) == id.Size:
		var dev id.DeviceID
		copy(dev[:], wanter)
		if dev == r.Device.ID {
			return // never answer our own request
		}
		hint = relay.HintFor(tid, dev, relay.Bucket(now))
	default:
		return
	}
	expires := now + uint64(DefaultRelayTTL/time.Second)

	// WHICH OF THESE HASHES BELONGS TO THIS SPACE, decided once, before any
	// network I/O — this function deliberately runs without r.mu (it dials
	// and Puts), so the index is read here in one short critical section
	// rather than inside the send loop, where it would be an unsynchronised
	// map read racing every indexRef.
	//
	// The check itself closes a cross-space oracle: the blob store is
	// node-global, so without it a member of one space could ask for a hash
	// they learned somewhere else and learn from the answer whether this
	// node holds it. The peer path already refuses that question
	// (kernel/sync's serveBlobs gates on BlobAllowed); the relay path did
	// not. Small in practice — blobs stay encrypted, and knowing a
	// ciphertext's hash usually means already having it — but a rule that
	// holds on one transport and not the other is how the gap got here.
	inScope := make(map[id.Hash]bool, len(wants))
	r.mu.Lock()
	for _, hb := range wants {
		if len(hb) != id.Size {
			continue
		}
		var h id.Hash
		copy(h[:], hb)
		inScope[h] = r.assetIdx.allowed(h, tid)
	}
	r.mu.Unlock()

	// The relay carries each item as one CBOR byte string, capped by
	// codec.MaxItemLen (1 MiB). So a media answer must be SPLIT into
	// sub-cap items — a single 8 MiB bundle would fail to decode at the relay
	// (and the failure would be silent). We ship up to DefaultBundleBudget of
	// chunks total, batched into items each under maxRelayItem; the requester
	// collects them all and re-asks for whatever did not fit.
	var batch [][]byte
	batchBytes, totalSent := 0, 0
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		body := bundle.EncodeWithBlobs(tid, nil, batch)
		_, err := client.Put(hint, expires, body)
		batch, batchBytes = nil, 0
		return err == nil
	}
	for _, hb := range wants {
		if totalSent >= DefaultBundleBudget {
			break
		}
		if len(hb) != id.Size {
			continue
		}
		var h id.Hash
		copy(h[:], hb)
		if !inScope[h] {
			continue
		}
		data, err := r.root.GetBlob(h)
		if err != nil {
			continue // we do not hold it; another member may
		}
		if len(data) > maxRelayItem {
			continue // one blob alone exceeds the relay item cap; skip it
		}
		if batchBytes+len(data) > maxRelayItem {
			if !flush() {
				return // relay refused a batch; stop, the requester re-asks
			}
		}
		batch = append(batch, data)
		batchBytes += len(data)
		totalSent += len(data)
	}
	flush()
}

// PullFromRelay collects bundles for every known space (current and
// previous hint buckets) and absorbs them. Idempotent: duplicates are
// no-ops in the event log.
// relayMailboxSpaces is which spaces this node will derive a relay address
// for. It exists as one function because the leak it guards is not "the
// relay was given something" but "the relay was ASKED about an address":
// a mailbox poll tells the relay the hint exists even when it is empty.
//
// A local-only space is absent, so no hint is derived and no mailbox is
// polled — the relay never learns the address at all (AI-0).
func (r *Runtime) relayMailboxSpaces() []id.TerminalID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]id.TerminalID, 0, len(r.spaces))
	for tid := range r.spaces {
		if r.ks.Spaces[tid].LocalOnly {
			continue
		}
		out = append(out, tid)
	}
	// SORTED, because a chunk cursor is meaningless over a random order.
	//
	// Go randomises map iteration on purpose, so this list came back in a
	// different order every call — which made "resume at chunk two" point at a
	// different set of spaces each cycle. The tail could still starve, and the
	// starvation would move around, which is worse than a fixed one: it looks
	// like flakiness rather than like a bug.
	//
	// The same reason Log.Summary sorts by device id: a position only means
	// something over a stable sequence.
	sort.Slice(out, func(i, j int) bool {
		return string(out[i][:]) < string(out[j][:])
	})
	return out
}

// maxCapsPerCollect mirrors the relay's own CollectMaxHints. Both paths that
// drain a mailbox bound themselves by it, and they share ONE constant: two
// private copies of the same server limit is how they drift apart, which is
// exactly what happened — the public path split and the private one did not.
const maxCapsPerCollect = 64

// applyRelayItems is the durable half of one chunk: everything collected is
// ingested and applied before the next request goes out.
func (r *Runtime) applyRelayItems(client *relay.Client, items [][]byte) (int, error) {
	applied := 0
	for _, item := range items {
		parts, err := bundle.DecodeParts(item)
		if err != nil {
			continue // not a bundle we understand; ignore quietly
		}
		terminal, frames, blobs := parts.Terminal, parts.Frames, parts.Blobs
		r.mu.Lock()
		_, known := r.spaces[terminal]
		r.mu.Unlock()
		if !known {
			continue // not our space; also nothing to answer with
		}
		// A relay media request rode along: answer with any wanted blobs we
		// hold, pushed into the requester's inbox (the response half of
		// on-demand media over the relay). Runs without r.mu — it does network
		// I/O.
		//
		// ROUTED TO THE WANTER (RT-0). This used to answer on `client` — the
		// connection the want arrived through, which is OUR ingress — and
		// while everybody shared one relay that was also the asker's relay.
		// The moment it was not, the answer landed in a mailbox on a machine
		// the asker never reads: the last cross-relay gap the T0 media test
		// caught after messages already crossed. A reply box still answers
		// in place — the asker minted it for THIS relay on purpose.
		if len(parts.Wants) > 0 {
			if len(parts.ReplyBox) > 0 {
				r.answerWants(client, terminal, parts.Wanter, parts.Wants, parts.ReplyBox, false)
			} else {
				r.answerWantsRouted(terminal, parts.Wanter, parts.Wants)
			}
		}
		r.mu.Lock()
		st, ok := r.spaces[terminal]
		if !ok {
			r.mu.Unlock()
			continue
		}
		for _, f := range frames {
			as, err := st.space.Log.Ingest(f)
			if err != nil {
				continue
			}
			for _, a := range as {
				st.space.AttachSyncApply(a)
				applied++
			}
		}
		// Carried blobs: verify-by-hash happens inside PutBlob addressing
		// (content-addressed store recomputes the id); possession proves
		// nothing about access — decryption still needs the block's key.
		//
		// EXPECTED-ONLY, like every other blob ingest. This loop used to
		// store whatever a bundle carried, and it was the one path that
		// did not gate: kernel/sync's acceptBlob refuses anything outside
		// e.pending, and swarmCollect drops an unexpected hash before it
		// costs anything. Nobody can forge a blob's identity — the store
		// is content-addressed — but the ingress hint for a space's inbox
		// is derivable by any member, so an unfiltered loop let one member
		// write unbounded padding into every other member's blob store,
		// permanently: nothing references those bytes and there is no GC.
		//
		// "Expected" here is wider than the want set, and it has to be.
		// A relay bundle carries blobs PROACTIVELY — that is the whole
		// dead drop: an offline peer collects the event, the manifest and
		// the chunks in one pull, having asked for nothing. So a blob is
		// admitted when the space's own asset graph references it
		// (assetIdx.allowed, the same question serveBlobs asks on the peer
		// path) or when this node asked for it outright. Frames are
		// ingested above, so a ref that arrived in THIS bundle already
		// counts. What is refused is the thing that was never referenced
		// by anything and never requested: padding.
		store := func(b []byte) bool {
			h := id.HashOf(b)
			if !r.assetIdx.allowed(h, terminal) && !r.wantsBlobLocked(terminal, h) {
				return false
			}
			_, _ = r.root.PutBlob(b)
			return true
		}
		var deferred [][]byte
		for _, b := range blobs {
			if !store(b) {
				deferred = append(deferred, b)
			}
		}
		// A delivered manifest may unlock chunk indexing.
		for h := range r.assetIdx.manifestOwner {
			if r.root.HasBlob(h) {
				r.onBlobStored(h)
			}
		}
		// Second pass, for the bundle that carried a manifest AND its
		// chunks together: until that manifest was stored and indexed just
		// above, its chunk ids were referenced by nothing this node knew
		// about, so the first pass could not tell them from padding. One
		// retry resolves it without a second round trip — and a blob that
		// is still unreferenced after the manifest landed is refused for
		// good, which is the case the gate exists for.
		if len(deferred) > 0 {
			again := false
			for _, b := range deferred {
				if store(b) {
					again = true
				}
			}
			if again {
				for h := range r.assetIdx.manifestOwner {
					if r.root.HasBlob(h) {
						r.onBlobStored(h)
					}
				}
			}
		}
		r.persistEpochsLocked(terminal, st.space)
		r.mu.Unlock()
	}
	return applied, nil
}

// collectOutcome is what a drain actually did, as a value rather than a pair.
//
// THE SHAPE IS THE POINT. `(items, err)` invites the most ordinary line in Go
// — `if err != nil { return err }` — and with a DESTRUCTIVE collect that line
// silently throws away messages the relay has already forgotten. A struct
// whose first field is the count of what was applied cannot be dropped by
// accident: there is nothing to ignore, because the work is already done and
// recorded by the time the error is read.
// stopReason says why a cycle ended, because "partial" alone cannot tell a
// caller whether to wait for a retry-after, discard a connection, or fix local
// storage. Those are three different actions and one boolean picks none of
// them.
type stopReason string

const (
	stopNone          stopReason = ""
	stopCollectFailed stopReason = "collect_failed"
	stopCommitFailed  stopReason = "commit_failed"
)

type collectOutcome struct {
	// Applied is how many events reached the log. Already durable — this is
	// not a promise of future work.
	Applied int
	// Chunks is how many chunks the capabilities were split into, and Served
	// how many completed. Served < Chunks is a partial pass.
	Chunks int
	Served int
	// NextChunk is where the following cycle should start. After a failure it
	// is the chunk that failed, so the tail of a long list cannot starve
	// behind a chunk that keeps being refused.
	NextChunk int
	// Stop says why the cycle ended. A collect that was refused and a commit
	// that could not be written are both "partial", and they call for opposite
	// responses — wait for the relay, or stop asking it for anything until
	// local storage works again.
	Stop stopReason
}

func (o collectOutcome) partial() bool { return o.Served < o.Chunks }

// collectInChunks drains capabilities in requests the server will accept,
// making each chunk durable BEFORE asking for the next.
//
// The order is what bounds the loss: Collect is destructive, so several chunks
// held in memory are several chunks a dying process loses at once. Committing
// between requests reduces that to one chunk — the most this shape can promise
// without the relay holding a batch until the client acknowledges it.
//
// It starts at `start` rather than at zero. A cycle that always began at the
// beginning would re-serve the same spaces every time and let the tail starve
// behind a chunk that keeps failing — which is exactly what a rate limit
// produces once it starts biting.
func (r *Runtime) collectInChunks(
	caps [][]byte,
	limit int,
	start int,
	collect func([][]byte) ([][]byte, error),
	commit func([][]byte) (int, error),
) (collectOutcome, error) {
	out := collectOutcome{}
	if len(caps) == 0 || limit <= 0 {
		return out, nil
	}
	out.Chunks = (len(caps) + limit - 1) / limit
	if start < 0 || start >= out.Chunks {
		start = 0
	}
	out.NextChunk = start

	for i := 0; i < out.Chunks; i++ {
		c := (start + i) % out.Chunks
		off := c * limit
		end := min(off+limit, len(caps))

		part, err := collect(caps[off:end])
		if err != nil {
			out.NextChunk = c // resume HERE, not at the beginning
			out.Stop = stopCollectFailed
			return out, err
		}
		// THE CURSOR MOVES ONLY AFTER THE COMMIT. A collect that succeeded and
		// a commit that failed leaves those messages gone from the relay and
		// absent locally — the lease-and-acknowledge window this shape cannot
		// close — but the cursor must not step past the chunk anyway. Stepping
		// on would start systematically serving the tail while local storage
		// is broken, and hide the failure behind apparent progress.
		applied, err := commit(part)
		out.Applied += applied
		if err != nil {
			out.NextChunk = c
			out.Stop = stopCommitFailed
			return out, err
		}
		out.Served++
		out.NextChunk = (c + 1) % out.Chunks
	}
	return out, nil
}

// throttledUntil is when a relay may be asked again, and asking before then is
// the one response a rate limit specifically did not want.
//
// The relay says how long to wait; this remembers it. Without the memory the
// next tick asks again immediately, is refused again, and the two sides spend
// a minute proving the same point to each other — which is how a cooldown loop
// looks from the outside and why it never recovers.
func (r *Runtime) relayThrottled(addr string) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.relayWaitUntil[addr]
	if !ok {
		return 0, false
	}
	left := time.Until(until)
	if left <= 0 {
		delete(r.relayWaitUntil, addr)
		return 0, false
	}
	return left, true
}

// noteRefusal records a relay's own answer about when to come back. Only a
// refusal that waiting fixes sets a deadline: a malformed request would fail
// identically after any wait, and sleeping on it would hide a bug behind a
// delay.
func (r *Runtime) noteRefusal(addr string, err error) {
	var re relay.ErrRelay
	if !errors.As(err, &re) || !re.Throttled() || re.RetryAfter <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.relayWaitUntil == nil {
		r.relayWaitUntil = map[string]time.Time{}
	}
	r.relayWaitUntil[addr] = time.Now().Add(re.RetryAfter)
}

// chunkCursor remembers where each relay's next drain should begin.
//
// In memory only, deliberately: it exists to stop the tail of a long list
// starving inside one running process, and a cursor persisted across restarts
// would be a second thing to get wrong for a hazard that ends when the process
// does.
func (r *Runtime) chunkCursor(addr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.relayChunkAt == nil {
		return 0
	}
	return r.relayChunkAt[addr]
}

func (r *Runtime) rememberChunkCursor(addr string, o collectOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.relayChunkAt == nil {
		r.relayChunkAt = map[string]int{}
	}
	r.relayChunkAt[addr] = o.NextChunk
}

func (r *Runtime) PullFromRelay(addr string) (applied int, err error) {
	if err := r.relayGate(); err != nil {
		return 0, err
	}
	all := r.relayMailboxSpaces()
	// The connection may exist because ANOTHER space permits the relay.
	// That does not make this one's traffic fair game: a space set to
	// Meshtastic only must have no mailbox polled and no hint derived, or
	// its activity would be visible on the relay anyway.
	tids := make([]id.TerminalID, 0, len(all))
	for _, tid := range all {
		if r.TransportAllowed(TransportRelay, tid) {
			tids = append(tids, tid)
		}
	}
	if len(tids) == 0 {
		return 0, nil
	}

	if err := r.relayGate(); err != nil {
		return 0, err
	}
	// Pooled control lane (RR-2): the drain is latency-bound and must
	// never sit behind a bulk fetch on the serial wire.
	client, release, err := r.pool().Control(addr)
	if err != nil {
		return 0, err
	}
	var opErr error
	defer func() { release(opErr) }()

	now := uint64(time.Now().Unix())
	self := r.Device.ID
	var caps [][]byte //nolint:prealloc // two per space, plus reply boxes
	for _, tid := range tids {
		b := relay.Bucket(now)
		caps = append(caps, relay.CapFor(tid, self, b))
		if b > 0 {
			caps = append(caps, relay.CapFor(tid, self, b-1))
		}
	}
	// PH-1 reply boxes: media answers for public spaces come back here, to
	// an address nobody else can drain.
	caps = append(caps, r.replyBoxCaps(tids, relay.Bucket(now))...)

	// SPLIT, BECAUSE THE SERVER BOUNDS ONE CALL AND THIS PATH SENDS TWO
	// CAPABILITIES PER SPACE.
	//
	// The relay refuses a Collect carrying more than CollectMaxHints (64), and
	// with the current and previous bucket per space that ceiling arrives at
	// about thirty-two spaces — plus a reply box for each public one. Past it
	// EVERY tick was refused, for as long as the person held that many spaces,
	// and from the interface it looked like messages had simply stopped: the
	// node was running, the relay was reachable, and the failures read as a
	// flaky connection.
	//
	// The public ingress path has split its capabilities since PA-1, with a
	// comment saying why. This one — the path every ordinary private space
	// uses — did not.
	// A FAILED CHUNK DOES NOT DISCARD THE ONES BEFORE IT, and that is not
	// tidiness — Collect is DESTRUCTIVE. The relay hands the items over and
	// forgets them, so returning early with everything the first chunks
	// drained would lose those messages permanently, on both sides, with
	// nothing anywhere reporting it.
	//
	// So the loop stops asking and the caller keeps what arrived. The cycle
	// reports the error as well as the count: the chunk that failed was never
	// served, and calling the whole collect successful would advance state as
	// though it had been.
	// PER CHUNK: COLLECT, THEN MAKE IT DURABLE, THEN ASK FOR THE NEXT ONE.
	//
	// Collect is destructive — the relay hands the items over and forgets
	// them — so holding several chunks in memory and applying them at the end
	// puts every message of every earlier chunk inside one window in which a
	// dying process loses all of them at once. Committing between requests
	// bounds that window to a single chunk, which is the most this shape can
	// promise without a lease-and-acknowledge round trip in the protocol.
	//
	// What is still NOT promised, and is written down rather than implied: a
	// process that dies between the relay's answer and the local commit loses
	// that chunk. Closing that needs the relay to keep the batch until the
	// client acknowledges it, which is a protocol change and its own decision.
	// ASKING WHILE THROTTLED IS THE ONE RESPONSE THE RELAY RULED OUT. It
	// refused because it wanted less traffic, so another request is not a
	// retry — it is the thing being asked to stop, and it keeps the window
	// from ever refilling.
	if left, yes := r.relayThrottled(addr); yes {
		return applied, relay.ErrRelay{Reason: relay.ReasonRateLimited, RetryAfter: left}
	}

	outcome, chunkErr := r.collectInChunks(caps, maxCapsPerCollect, r.chunkCursor(addr),
		client.Collect,
		func(items [][]byte) (int, error) { return r.applyRelayItems(client, items) })
	applied += outcome.Applied
	if chunkErr != nil {
		opErr = chunkErr
		r.noteRefusal(addr, chunkErr)
	}
	r.rememberChunkCursor(addr, outcome)

	// PARTIAL IS ITS OWN ANSWER. What arrived has been applied and is not
	// undone — it is already gone from the relay — but the cycle says the
	// chunk that failed was never served, so nothing upstream can mistake this
	// for a complete pass over every space.
	if chunkErr != nil {
		return applied, chunkErr
	}
	return applied, nil
}

// CollectReplyBoxesAt drains media answers for the given public spaces from
// ONE relay — the space's own — rather than from this node's personal one.
//
// WHY THIS HAS TO EXIST. A public space carries its own relay in signed
// policy, so a reader's media want travels to THAT relay (pushPublicIngress
// uses the space's write address) and a holder answers into a reply box
// there. The collect, meanwhile, has always run against the personal relay:
// PullFromRelay asks one address for every space it knows.
//
// While every space in the world shared one relay those were the same
// address and nothing was wrong. The moment a second official relay existed
// they came apart — the answer is written to one machine and waited for on
// another — and the symptom is a picture that never arrives from a mirror
// that demonstrably holds it, with every diagnostic on both sides healthy.
// Seen the day a second region was added.
func (r *Runtime) CollectReplyBoxesAt(addr string, tids []id.TerminalID) (int, error) {
	if addr == "" || len(tids) == 0 {
		return 0, nil
	}
	if err := r.relayGate(); err != nil {
		return 0, err
	}
	caps := r.replyBoxCaps(tids, relay.Bucket(uint64(time.Now().Unix())))
	if len(caps) == 0 {
		return 0, nil
	}
	if left, yes := r.relayThrottled(addr); yes {
		return 0, relay.ErrRelay{Reason: relay.ReasonRateLimited, RetryAfter: left}
	}
	client, release, err := r.pool().Control(addr)
	if err != nil {
		return 0, err
	}
	var opErr error
	defer func() { release(opErr) }()
	outcome, chunkErr := r.collectInChunks(caps, maxCapsPerCollect, r.chunkCursor(addr),
		client.Collect,
		func(items [][]byte) (int, error) { return r.applyRelayItems(client, items) })
	r.rememberChunkCursor(addr, outcome)
	if chunkErr != nil {
		opErr = chunkErr
		r.noteRefusal(addr, chunkErr)
	}
	return outcome.Applied, chunkErr
}

// replyBoxCapLocked returns this space's current media reply capability,
// minting or rotating it when the relay bucket turns. Caller holds r.mu.
func (r *Runtime) replyBoxCapLocked(tid id.TerminalID, bucket uint64) []byte {
	if r.replyBoxes == nil {
		r.replyBoxes = map[id.TerminalID]*replyBox{}
	}
	if b, ok := r.replyBoxes[tid]; ok && b.bucket == bucket {
		return b.cap
	}
	c, err := relay.NewReplyCap()
	if err != nil {
		return nil // no entropy: fall back to no reply box rather than a weak one
	}
	var prev []byte
	if b, ok := r.replyBoxes[tid]; ok {
		prev = b.cap // an answer may already be sitting in the old box
	}
	r.replyBoxes[tid] = &replyBox{cap: c, prev: prev, bucket: bucket}
	return c
}

// replyBoxCaps lists the drain capabilities to collect answers from: the
// current bucket and the previous one, since an answer may have been posted
// just before a rotation.
func (r *Runtime) replyBoxCaps(tids []id.TerminalID, bucket uint64) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var caps [][]byte
	for _, tid := range tids {
		if b, ok := r.replyBoxes[tid]; ok {
			caps = append(caps, b.cap)
			if len(b.prev) > 0 {
				caps = append(caps, b.prev)
			}
		}
	}
	return caps
}
