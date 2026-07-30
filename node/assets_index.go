// Asset indexing and the fetch coordinator (Gate B).
//
// Indexes are scoped and many-to-many (plan §11): a wire id maps to the SET
// of spaces that legitimately reference it, and assets are addressed by
// (space, asset id) — there is no global asset lookup. Rebuild re-derives
// everything from the logs: AssetRefs from decrypted block events, chunk
// wire ids from locally available decrypted manifests.
package node

import (
	"errors"
	"io"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// AssetKey addresses an asset within one space (no global namespace). Asset
// is the hex of the ref's PublicID — 16-byte legacy handle or 32-byte V2
// content digest — so the key holds either width comparably.
type AssetKey struct {
	Space id.TerminalID
	Asset string
}

// FetchReason is the machine-readable failure cause (plan §Gate B).
type FetchReason string

const (
	ReasonNone         FetchReason = ""
	ReasonNotFound     FetchReason = "not_found"
	ReasonTimeout      FetchReason = "timeout"
	ReasonIntegrity    FetchReason = "integrity_error"
	ReasonStorageLimit FetchReason = "storage_limit"
	ReasonUnsupported  FetchReason = "unsupported_manifest"
	ReasonNoPeers      FetchReason = "no_peers"
	// ReasonCancelled: the node is shutting down. Distinct from a timeout,
	// because nothing was wrong with the fetch — we stopped waiting.
	ReasonCancelled FetchReason = "cancelled"
)

// sleepOrStop waits, unless the node is shutting down. It reports false when
// the wait was cut short.
//
// The bare time.Sleep calls this replaces were the last ones in non-test node
// code, and they made shutdown take up to two minutes — long enough for macOS
// or systemd to lose patience and kill the process, possibly in the middle of
// a keystore write, which is exactly what the data-directory lock exists to
// prevent.
func (r *Runtime) sleepOrStop(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.stop:
		return false
	}
}

// FetchStatus is the projection of one asset's lifecycle.
type FetchStatus struct {
	State     assets.State
	Reason    FetchReason
	Missing   int
	Available int
	Total     int
	SizeBytes uint64
}

type assetIndex struct {
	// wireSpaces: which spaces legitimately publish each wire id.
	wireSpaces map[id.Hash]map[id.TerminalID]struct{}
	// refs: (space, asset id) → ref.
	refs map[AssetKey]*schemas.AssetRef
	// manifestOwner: manifest wire id → asset key (to reindex chunks the
	// moment a fetched manifest lands).
	manifestOwner map[id.Hash]AssetKey
	// refOrder preserves first-seen order per space (deterministic bundle
	// export: assets in event order).
	refOrder map[id.TerminalID][]AssetKey
	// fetching marks assets with an active coordinator goroutine.
	fetching map[AssetKey]bool
	failed   map[AssetKey]FetchReason
}

func newAssetIndex() *assetIndex {
	return &assetIndex{
		wireSpaces:    map[id.Hash]map[id.TerminalID]struct{}{},
		refs:          map[AssetKey]*schemas.AssetRef{},
		manifestOwner: map[id.Hash]AssetKey{},
		refOrder:      map[id.TerminalID][]AssetKey{},
		fetching:      map[AssetKey]bool{},
		failed:        map[AssetKey]FetchReason{},
	}
}

func (x *assetIndex) allow(h id.Hash, space id.TerminalID) {
	set, ok := x.wireSpaces[h]
	if !ok {
		set = map[id.TerminalID]struct{}{}
		x.wireSpaces[h] = set
	}
	set[space] = struct{}{}
}

func (x *assetIndex) allowed(h id.Hash, space id.TerminalID) bool {
	set, ok := x.wireSpaces[h]
	if !ok {
		return false
	}
	_, ok = set[space]
	return ok
}

// indexRef registers one AssetRef for a space: its manifest or inline
// chunks, and — when the manifest is already local — its chunk ids too.
func (r *Runtime) indexRef(space id.TerminalID, ref *schemas.AssetRef) {
	key := AssetKey{Space: space, Asset: ref.PublicIDHex()}
	if _, seen := r.assetIdx.refs[key]; !seen {
		r.assetIdx.refOrder[space] = append(r.assetIdx.refOrder[space], key)
	}
	r.assetIdx.refs[key] = ref
	for _, h := range ref.WireIDs() {
		r.assetIdx.allow(h, space)
	}
	if ref.ManifestWireID != nil {
		r.assetIdx.manifestOwner[*ref.ManifestWireID] = key
		if r.root.HasBlob(*ref.ManifestWireID) {
			r.indexManifestChunks(space, ref)
		}
	}
}

// indexManifestChunks decrypts a locally available manifest and authorizes
// its chunk ids for the space.
func (r *Runtime) indexManifestChunks(space id.TerminalID, ref *schemas.AssetRef) {
	man, err := assets.LoadManifest(r.root, ref)
	if err != nil {
		return // corrupt or unsupported manifest: chunks stay unindexed
	}
	for _, c := range man.Chunks {
		r.assetIdx.allow(c, space)
	}
}

// onBlockEvent is the Space.OnBlock hook: harvest asset refs from every
// decrypted block event. Runs during absorb (r.mu held by callers of
// engine/emit paths; during Open it runs single-threaded).
func (r *Runtime) onBlockEvent(space id.TerminalID) func(env *signal.Envelope, eid id.EventID) {
	return func(env *signal.Envelope, eid id.EventID) {
		for _, ref := range schemas.ExtractAssetRefs(env.Schema, env.Payload) {
			if ref != nil {
				r.indexRef(space, ref)
			}
		}
	}
}

// onBlobStored reindexes chunks when a fetched manifest arrives.
func (r *Runtime) onBlobStored(h id.Hash) {
	if key, ok := r.assetIdx.manifestOwner[h]; ok {
		if ref, ok := r.assetIdx.refs[key]; ok {
			r.indexManifestChunks(key.Space, ref)
		}
	}
}

// IngestAsset encrypts and stores content for a space, returning the ref
// to embed in a block (the caller emits the block; on emit failure the
// blobs stay orphaned and unindexed — future GC's job, never DeleteBlob).
func (r *Runtime) IngestAsset(src io.Reader, size int64, meta assets.Metadata) (*schemas.AssetRef, error) {
	return assets.Ingest(r.root, src, size, meta)
}

// RetrieveAsset returns the decrypted, integrity-verified content of an
// asset published in the given space.
func (r *Runtime) RetrieveAsset(space id.TerminalID, asset string) ([]byte, *schemas.AssetRef, error) {
	r.mu.Lock()
	ref, ok := r.assetIdx.refs[AssetKey{Space: space, Asset: asset}]
	r.mu.Unlock()
	if !ok {
		return nil, nil, errors.New("node: unknown asset in this space")
	}
	data, err := assets.RetrieveBytes(r.root, ref)
	if err != nil {
		return nil, ref, err
	}
	return data, ref, nil
}

// AssetRefFor exposes a ref for the API layer (space-scoped only).
func (r *Runtime) AssetRefFor(space id.TerminalID, asset string) (*schemas.AssetRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.assetIdx.refs[AssetKey{Space: space, Asset: asset}]
	return ref, ok
}

// AssetStatus projects the current lifecycle of an asset.
func (r *Runtime) AssetStatus(space id.TerminalID, asset string) (FetchStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.assetStatusLocked(AssetKey{Space: space, Asset: asset})
}

func (r *Runtime) assetStatusLocked(key AssetKey) (FetchStatus, error) {
	ref, ok := r.assetIdx.refs[key]
	if !ok {
		return FetchStatus{}, errors.New("node: unknown asset in this space")
	}
	st, res := assets.StateOf(r.root, ref)
	out := FetchStatus{State: st, Missing: len(res.MissingChunks),
		Available: res.AvailableChunks, Total: res.TotalChunks, SizeBytes: ref.Size}
	if res.ManifestMissing {
		out.Missing = res.TotalChunks
	}
	if r.assetIdx.fetching[key] {
		out.State = assets.StateFetching
	}
	if reason, ok := r.assetIdx.failed[key]; ok && out.State != assets.StateComplete {
		out.State = assets.StateFailed
		out.Reason = reason
	}
	return out, nil
}

// RequestAsset starts (or joins) a fetch for an asset: two-phase, sequential
// over available peers, bounded retries. Returns immediately; progress is
// polled via AssetStatus.
func (r *Runtime) RequestAsset(space id.TerminalID, asset string) error {
	r.mu.Lock()
	key := AssetKey{Space: space, Asset: asset}
	ref, ok := r.assetIdx.refs[key]
	if !ok {
		r.mu.Unlock()
		return errors.New("node: unknown asset in this space")
	}
	if r.assetIdx.fetching[key] {
		r.mu.Unlock()
		return nil // in-flight dedup: join the existing fetch
	}
	st, _ := r.spaces[space]
	if st == nil {
		r.mu.Unlock()
		return errors.New("node: unknown space")
	}
	r.assetIdx.fetching[key] = true
	delete(r.assetIdx.failed, key)
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		reason := r.fetchLoop(key, ref, st)
		r.mu.Lock()
		delete(r.assetIdx.fetching, key)
		if reason != ReasonNone {
			r.assetIdx.failed[key] = reason
		}
		r.mu.Unlock()
	}()
	return nil
}

// fetchLoop drives one asset to completion: phase 1 manifest, phase 2
// chunks; peers tried sequentially (plan §5: no fan-out duplication).
func (r *Runtime) fetchLoop(key AssetKey, ref *schemas.AssetRef, st *spaceState) FetchReason {
	deadline := time.Now().Add(2 * time.Minute)
	relayAddr := r.GetSettings().Relay
	// Track hashes we asked for over the relay so we can stop asking on exit.
	registered := map[id.Hash]struct{}{}
	defer func() {
		if len(registered) == 0 {
			return
		}
		hs := make([]id.Hash, 0, len(registered))
		for h := range registered {
			hs = append(hs, h)
		}
		r.clearRelayWants(key.Space, hs)
	}()
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		r.mu.Lock()
		res, err := assets.Missing(r.root, ref)
		if err != nil {
			r.mu.Unlock()
			return ReasonUnsupported
		}
		var want []id.Hash
		if res.ManifestMissing {
			want = []id.Hash{*ref.ManifestWireID}
		} else if len(res.MissingChunks) > 0 {
			want = res.MissingChunks
		} else {
			r.mu.Unlock()
			return ReasonNone // complete
		}
		conns := append([]link(nil), st.conns...)
		r.mu.Unlock()

		if len(conns) == 0 {
			// No direct peer. If a relay is configured, register the missing
			// blobs as a request: the background auto-sync push rides them to
			// peers, a holder answers into our inbox, and PullFromRelay stores
			// the bytes — we just wait here for the asset to complete.
			if relayAddr == "" {
				return ReasonNoPeers
			}
			// Keep the want set equal to what's STILL missing: drop hashes that
			// have since arrived. Otherwise a >budget asset keeps asking for
			// chunks it already has, and the holder refills each response with
			// the same early chunks — the tail never ships and the fetch stalls.
			wantSet := make(map[id.Hash]struct{}, len(want))
			for _, h := range want {
				wantSet[h] = struct{}{}
			}
			var satisfied []id.Hash
			for h := range registered {
				if _, still := wantSet[h]; !still {
					satisfied = append(satisfied, h)
				}
			}
			if len(satisfied) > 0 {
				r.clearRelayWants(key.Space, satisfied)
			}
			r.addRelayWants(key.Space, want)
			registered = wantSet
			if !r.sleepOrStop(600 * time.Millisecond) {
				return ReasonCancelled
			}
			continue
		}
		peer := conns[attempt%len(conns)]
		if closed, _ := peer.Closed(); closed {
			continue
		}
		r.mu.Lock()
		err = st.eng.RequestBlobs(peer, want)
		r.mu.Unlock()
		if err != nil {
			continue
		}
		// Wait for progress: the pump loop stores arriving blobs.
		waitUntil := time.Now().Add(15 * time.Second)
		start := time.Now()
		for time.Now().Before(waitUntil) {
			if !r.sleepOrStop(150 * time.Millisecond) {
				return ReasonCancelled
			}
			r.mu.Lock()
			now, err := assets.Missing(r.root, ref)
			r.mu.Unlock()
			if err == nil {
				if !now.ManifestMissing && len(now.MissingChunks) == 0 {
					return ReasonNone
				}
				progressed := (res.ManifestMissing && !now.ManifestMissing) ||
					len(now.MissingChunks) < len(res.MissingChunks)
				if progressed {
					break // re-plan with fresh missing set
				}
			}
			_ = start
		}
	}
	return ReasonTimeout
}

// assetRefsLocked lists the asset ids indexed for one space. Caller holds
// r.mu. Used by the mirror status to count custody actually held.
func (r *Runtime) assetRefsLocked(space id.TerminalID) []string {
	var out []string
	for k := range r.assetIdx.refs {
		if k.Space == space {
			out = append(out, k.Asset)
		}
	}
	return out
}
