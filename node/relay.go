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
	return r.pushToRelay(addr, tid, AssetsAvailable)
}

func (r *Runtime) pushToRelay(addr string, tid id.TerminalID, policy AssetPolicy) (int, uint64, error) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return 0, 0, errors.New("node: unknown space")
	}
	var frames [][]byte
	var eventIDs []id.EventID
	now := uint64(time.Now().Unix())
	if err := st.space.Log.Replay(func(a eventlog.Applied) error {
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
		return 0, 0, err
	}
	blobs, _ := r.collectBlobs(tid, policy, DefaultBundleBudget)
	body := bundle.EncodeWithBlobs(tid, frames, blobs)
	r.mu.Unlock()

	client, err := relay.DialClient(addr)
	if err != nil {
		return 0, 0, err
	}
	defer client.Close()
	now = uint64(time.Now().Unix())
	hint := relay.Hint(tid, relay.Bucket(now))
	deadline, err := client.Put(hint, now+uint64(DefaultRelayTTL/time.Second), body)
	if err != nil {
		return 0, 0, err
	}

	// Record the honest receipt level for every pushed event: the relay
	// accepted them; nobody received anything yet.
	r.mu.Lock()
	for _, eid := range eventIDs {
		_ = st.space.Trust.RecordTransportReceipt(eid, tid, claims.DeliveryAcceptedByRelay)
	}
	r.mu.Unlock()
	return len(frames), deadline, nil
}

// PullFromRelay collects bundles for every known space (current and
// previous hint buckets) and absorbs them. Idempotent: duplicates are
// no-ops in the event log.
func (r *Runtime) PullFromRelay(addr string) (applied int, err error) {
	r.mu.Lock()
	tids := make([]id.TerminalID, 0, len(r.spaces))
	for tid := range r.spaces {
		tids = append(tids, tid)
	}
	r.mu.Unlock()
	if len(tids) == 0 {
		return 0, nil
	}

	client, err := relay.DialClient(addr)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	now := uint64(time.Now().Unix())
	var hints [][]byte
	for _, tid := range tids {
		b := relay.Bucket(now)
		hints = append(hints, relay.Hint(tid, b))
		if b > 0 {
			hints = append(hints, relay.Hint(tid, b-1))
		}
	}
	items, err := client.Collect(hints)
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		terminal, frames, blobs, err := bundle.DecodeFull(item)
		if err != nil {
			continue // not a bundle we understand; ignore quietly
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
