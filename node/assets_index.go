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
	// ReasonNoSource: a relay IS configured, the want WAS published, and no
	// byte ever came back. Distinct from a timeout, which means bytes were
	// moving and then stopped, and from no_peers, which means this node was
	// never asking anyone. To a person these are three different sentences —
	// "nobody online has this", "the transfer stalled", "you are offline" —
	// and only the middle one used to exist.
	ReasonNoSource FetchReason = "no_source"
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
	// Waiting is how long this fetch has been asking with nothing to show
	// for it. The UI uses it to stop claiming progress it cannot see.
	Waiting time.Duration
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
	// silent records when a still-running fetch first found nobody. It is a
	// provisional state, not a verdict: the loop keeps asking, and any byte
	// that arrives clears the entry.
	silent map[AssetKey]time.Time
}

func newAssetIndex() *assetIndex {
	return &assetIndex{
		wireSpaces:    map[id.Hash]map[id.TerminalID]struct{}{},
		refs:          map[AssetKey]*schemas.AssetRef{},
		manifestOwner: map[id.Hash]AssetKey{},
		refOrder:      map[id.TerminalID][]AssetKey{},
		fetching:      map[AssetKey]bool{},
		failed:        map[AssetKey]FetchReason{},
		silent:        map[AssetKey]time.Time{},
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
// clearFetchSilence forgets a provisional "no source" the moment anything
// arrives: a late answer must not leave a stale sentence on screen.
func (r *Runtime) clearFetchSilence(key AssetKey) {
	delete(r.assetIdx.silent, key)
}

func (r *Runtime) onBlobStored(h id.Hash) {
	if key, ok := r.assetIdx.manifestOwner[h]; ok {
		if ref, ok := r.assetIdx.refs[key]; ok {
			r.indexManifestChunks(key.Space, ref)
		}
		r.clearFetchSilence(key)
	}
	// Any chunk arriving is proof someone is answering: a fetch that had
	// been reported sourceless is simply in progress again.
	for key, ref := range r.assetIdx.refs {
		if ref.ManifestWireID != nil && *ref.ManifestWireID == h {
			r.clearFetchSilence(key)
			continue
		}
		if _, ok := r.assetIdx.silent[key]; ok {
			r.clearFetchSilence(key)
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
	// A running fetch is worth SAYING, but it is not worth lying about: if
	// every chunk is on disk the asset is complete, whatever a loop is still
	// doing about it. Without the guard the caller of RequestAsset+AssetStatus
	// — which is every poll of POST /fetch — re-arms this flag a moment
	// before reading the answer, so the answer was never "complete" for any
	// asset at all. The `failed` override on the next line has always had the
	// same guard; this one was missing it.
	if r.assetIdx.fetching[key] && out.State != assets.StateComplete {
		out.State = assets.StateFetching
	}
	if reason, ok := r.assetIdx.failed[key]; ok && out.State != assets.StateComplete {
		out.State = assets.StateFailed
		out.Reason = reason
	}
	// A fetch still running but finding nobody reports the truth NOW, while
	// staying in the fetching state — it has not given up, so calling it
	// failed would be a lie in the other direction.
	if since, ok := r.assetIdx.silent[key]; ok && out.State == assets.StateFetching {
		out.Reason = ReasonNoSource
		out.Waiting = time.Since(since)
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
	// Already here. Starting a loop to fetch what is on disk would spawn a
	// goroutine per poll — and each one re-registers relay wants for chunks
	// nobody needs, churning the want set the fetch machinery depends on.
	if s, _ := assets.StateOf(r.root, ref); s == assets.StateComplete {
		delete(r.assetIdx.failed, key)
		delete(r.assetIdx.silent, key)
		r.mu.Unlock()
		return nil
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
	relayAddr := r.ResolvePersonalRelay()
	// Track hashes we asked for over the relay so we can stop asking on exit.
	registered := map[id.Hash]struct{}{}
	// Progress is what separates a stall from a silence: if a single byte
	// ever arrived, someone answered us and the fetch is merely incomplete.
	started := time.Now()
	progressed := false
	lastMissing := -1
	// PATIENCE IS SPENT ON PROGRESS, NEVER ON SILENCE. This was a flat two
	// minutes from the first attempt, which was survivable while an asset
	// could not exceed 64 MiB and is not now: a relay hands over about 8 MiB
	// per round, so half a gigabyte is some sixty rounds and the wall would
	// arrive with the file a fifth of the way in — a fetch that fails while
	// it is visibly working, every time, for exactly the files that most
	// need to be waited for.
	//
	// So the clock is reset by ARRIVING BYTES and the wait is for silence.
	// The ceiling below is not a policy about how long a download may take;
	// it is a bound on a goroutine, so a peer that dribbles one chunk an hour
	// cannot keep one alive forever.
	lastProgress := time.Now()
	note := func() { lastProgress = time.Now(); progressed = true }
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
	for attempt := 0; time.Since(lastProgress) < fetchIdleGiveUp &&
		time.Since(started) < fetchHardCeiling; attempt++ {
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
			if lastMissing >= 0 && len(want) < lastMissing {
				note()
			}
			lastMissing = len(want)
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
			// Report a provisional verdict long before the deadline. The
			// fetch keeps running — a holder may still come online — but a
			// person staring at a spinner deserves a true sentence within
			// seconds rather than two silent minutes.
			if !progressed && time.Since(started) > noSourceAfter {
				r.noteFetchSilence(key)
			}
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
				// note(), not a local `progressed :=` — which is what this
				// was, shadowing the outer flag so a direct-peer fetch that
				// moved and then stopped reported "no source" instead of a
				// timeout, and never refreshed the clock either.
				if (res.ManifestMissing && !now.ManifestMissing) ||
					len(now.MissingChunks) < len(res.MissingChunks) {
					note()
					break // re-plan with fresh missing set
				}
			}
			_ = start
		}
	}
	// Nothing ever arrived: this was silence, not a stall.
	if !progressed {
		return ReasonNoSource
	}
	return ReasonTimeout
}

const (
	// fetchIdleGiveUp is how long a fetch waits with NOTHING new arriving
	// before it stops. Two minutes of silence is a long time to be asking a
	// relay for a blob nobody is holding.
	fetchIdleGiveUp = 2 * time.Minute
	// fetchHardCeiling bounds the goroutine, not the download. At the ~8 MiB
	// a relay round hands over, an hour is far more than the largest asset
	// this build accepts needs, and far less than forever.
	fetchHardCeiling = time.Hour
)

// noSourceAfter is how long a fetch may find nothing before the interface
// is allowed to say so. Short enough that a person is not left guessing,
// long enough that an ordinary relay round trip is not called a failure.
const noSourceAfter = 20 * time.Second

// noteFetchSilence records a provisional "no source" WITHOUT ending the
// fetch: the loop keeps asking, and a late answer clears it. Saying "not
// available right now" and being wrong a moment later is honest; saying
// nothing for two minutes is not.
func (r *Runtime) noteFetchSilence(key AssetKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.assetIdx.failed == nil {
		r.assetIdx.failed = map[AssetKey]FetchReason{}
	}
	if _, ended := r.assetIdx.failed[key]; !ended {
		r.assetIdx.silent[key] = time.Now()
	}
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
