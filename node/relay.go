// Store-and-forward via blind relay (M1.5): push a space's frames to a
// relay under its rotating hint; an offline peer pulls them later. The
// relay sees a hint and a ciphertext bundle — payloads are epoch-encrypted
// (ADR-005) and the trust engine records exactly accepted_by_relay for
// pushed events, never delivery (ADR-008).
package node

import (
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/eventlog"
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

// pushToRelay returns (framesPrepared, recipients, deadline, err). recipients
// is how many peer inboxes actually received a copy — 0 means "nobody
// addressable yet" (a solo space, or a fresh joiner before its first pull),
// which is a clean no-op, not an error. The auto-sync loop keys its progress
// on recipients so it retries rather than marking undelivered frames as sent.
func (r *Runtime) pushToRelay(addr string, tid id.TerminalID, policy AssetPolicy) (int, int, uint64, error) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return 0, 0, 0, errors.New("node: unknown space")
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
	if err := st.space.Log.Replay(func(a eventlog.Applied) error {
		devSet[a.Env.Device] = struct{}{} // author is a member, custody aside
		// Custody filter (ADR-015): expired frames never spend relay
		// storage or later airtime; NoCustody frames refuse
		// store-and-forward by author declaration. The relay itself
		// stays structure-blind — the PUSHER excludes them.
		if a.Env.Expired(now) || a.Env.ForwardingScope() == signal.NoCustody {
			return nil
		}
		frames = append(frames, a.Frame)
		eventIDs = append(eventIDs, a.ID)
		return nil
	}); err != nil {
		r.mu.Unlock()
		return 0, 0, 0, err
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
	body := bundle.EncodeWithWants(tid, frames, blobs, wants, wanter)
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
		// no-op. Reporting recipients==0 lets the auto-sync loop retry rather
		// than mark these frames as handed off.
		return len(frames), 0, 0, nil
	}
	if err := r.relayGate(); err != nil {
		return 0, 0, 0, err
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		return 0, 0, 0, err
	}
	defer client.Close()
	now = uint64(time.Now().Unix())
	bucket := relay.Bucket(now)
	expires := now + uint64(DefaultRelayTTL/time.Second)
	var deadline uint64
	for _, dev := range recipients {
		d, err := client.Put(relay.HintFor(tid, dev, bucket), expires, body)
		if err != nil {
			return 0, 0, 0, err
		}
		deadline = d
	}

	// Record the honest receipt level for every pushed event: the relay
	// accepted them; nobody received anything yet.
	r.mu.Lock()
	for _, eid := range eventIDs {
		_ = st.space.Trust.RecordTransportReceipt(eid, tid, claims.DeliveryAcceptedByRelay)
	}
	r.mu.Unlock()
	return len(frames), len(recipients), deadline, nil
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
	return out
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
	client, err := relay.DialClient(addr)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	now := uint64(time.Now().Unix())
	self := r.Device.ID
	var caps [][]byte
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
	items, err := client.Collect(caps)
	if err != nil {
		return 0, err
	}
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
		if len(parts.Wants) > 0 {
			r.answerWants(client, terminal, parts.Wanter, parts.Wants, parts.ReplyBox, false)
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
		for _, b := range blobs {
			_, _ = r.root.PutBlob(b)
		}
		// A delivered manifest may unlock chunk indexing.
		for h := range r.assetIdx.manifestOwner {
			if r.root.HasBlob(h) {
				r.onBlobStored(h)
			}
		}
		r.persistEpochsLocked(terminal, st.space)
		r.mu.Unlock()
	}
	return applied, nil
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
