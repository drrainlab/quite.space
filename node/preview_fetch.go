// The transient fetch session (PM-1 + PM-2).
//
// After Read post, media assembles from whoever holds the bytes — owner,
// mirror, or seeding peer — through the Space-free swarm core. Nothing
// becomes part of the reader's durable state: the session store is
// memory, the keys die with the session, and every death path (close,
// TTL, cap eviction, runtime shutdown) kills the fetcher.
//
// THE BOUNDARY: a session may fetch only what is reachable from a
// publication the person actually opened — Publication.LiveAssetIDs()
// intersected with the projection's signed carrier refs. An id outside
// that graph is refused; the session route must never become a browse
// handle on the space's whole asset store. An id IN the graph whose
// carrier the projection no longer holds is descriptor_unavailable —
// "this publication does not currently carry enough information to
// request this media" — which is a fact about the projection, never a
// network silence.
package node

import (
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// Session fetch states — an explicit enum describing OBSERVATION, never
// guessing about the network. Whether a verified file can be DECODED is
// the renderer's business, not a fetch state.
const (
	FetchNotRequested   = "not_requested"
	FetchRequesting     = "requesting"
	FetchPartial        = "partial"
	FetchReady          = "ready"
	FetchNoResponse     = "no_response"
	FetchDescriptorGone = "descriptor_unavailable"
	FetchBudget         = "budget_exceeded"
	FetchIntegrity      = "integrity_error"
	FetchCancelled      = "cancelled"
)

// Budgets. The global cap covers the REAL peak — encrypted chunks AND the
// assembled plaintext both count — across every live session. Hostile to
// nobody's laptop, honest about mobile targets.
const (
	previewGlobalBudget = 160 << 20
	// fetchDeadline bounds one asset's attempt; no_response after it.
	fetchDeadline = 60 * time.Second
	// collectEvery paces the reply-box drain (relay allows 60/min).
	collectEvery = 1200 * time.Millisecond
	// repushEvery re-advertises an unchanged want set before its relay
	// TTL runs out; set changes and explicit retries re-push immediately.
	repushEvery = 5 * time.Minute
)

// previewBudget is the shared accounting across all sessions.
var previewBudget struct {
	mu    sync.Mutex
	spent int64
}

func budgetAdmit(n int) bool {
	previewBudget.mu.Lock()
	defer previewBudget.mu.Unlock()
	if previewBudget.spent+int64(n) > previewGlobalBudget {
		return false
	}
	previewBudget.spent += int64(n)
	return true
}

func budgetRelease(n int64) {
	previewBudget.mu.Lock()
	previewBudget.spent -= n
	if previewBudget.spent < 0 {
		previewBudget.spent = 0
	}
	previewBudget.mu.Unlock()
}

// sessionStore is the session's assets.Store: memory, budget-accounted,
// with READ-THROUGH to the node's content-addressed root — a node that
// mirrors the space (or holds a blob for any reason) serves it locally
// with zero network, and Follow-after-preview re-downloads nothing in
// either direction.
type sessionStore struct {
	mu    sync.Mutex
	mem   map[id.Hash][]byte
	root  assets.Store
	bytes int64
}

func newSessionStore(root assets.Store) *sessionStore {
	return &sessionStore{mem: map[id.Hash][]byte{}, root: root}
}

func (s *sessionStore) PutBlob(data []byte) (id.Hash, error) {
	h := id.HashOf(data)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mem[h]; ok {
		return h, nil
	}
	s.mem[h] = append([]byte(nil), data...)
	s.bytes += int64(len(data))
	return h, nil
}

func (s *sessionStore) GetBlob(h id.Hash) ([]byte, error) {
	s.mu.Lock()
	b, ok := s.mem[h]
	s.mu.Unlock()
	if ok {
		return b, nil
	}
	return s.root.GetBlob(h)
}

func (s *sessionStore) HasBlob(h id.Hash) bool {
	s.mu.Lock()
	_, ok := s.mem[h]
	s.mu.Unlock()
	return ok || s.root.HasBlob(h)
}

// release returns everything this store accounted for to the budget.
func (s *sessionStore) release() {
	s.mu.Lock()
	n := s.bytes
	s.mem, s.bytes = map[id.Hash][]byte{}, 0
	s.mu.Unlock()
	budgetRelease(n)
}

// sessionAsset is one allowlisted asset of the session's graph.
type sessionAsset struct {
	Ref  *schemas.AssetRef
	Role string
}

// fetchJob is one asset's fetch, coalescing concurrent requests.
type fetchJob struct {
	state     string
	missing   int
	total     int
	reason    string
	started   time.Time
	plaintext []byte // assembled ONCE at ready; every Range serves it
}

// sessionFetcher drives the swarm for one preview session.
type sessionFetcher struct {
	mu      sync.Mutex
	space   id.TerminalID
	relay   string
	dial    func(string) (*relay.Client, error) // trust-profile dialer (RR-1)
	hints   [][]byte
	wanter  id.DeviceID
	cap     []byte
	store   *sessionStore
	graph   map[string]sessionAsset // allowlist: asset hex → ref+role
	jobs    map[string]*fetchJob
	stop    chan struct{}
	stopped bool
	wake    chan struct{}
	// wantEpoch bumps on set changes and explicit retries, forcing a
	// re-push before repushEvery.
	wantEpoch uint64
}

func newSessionFetcher(space id.TerminalID, relayAddr string, hints [][]byte,
	root assets.Store, dial func(string) (*relay.Client, error)) (*sessionFetcher, error) {
	wanter, err := newSwarmIdentity()
	if err != nil {
		return nil, err
	}
	replyCap, err := relay.NewReplyCap()
	if err != nil {
		return nil, err
	}
	if dial == nil {
		dial = relay.DialClient
	}
	return &sessionFetcher{
		space: space, relay: relayAddr, dial: dial, hints: hints,
		wanter: wanter, cap: replyCap,
		store: newSessionStore(root),
		graph: map[string]sessionAsset{}, jobs: map[string]*fetchJob{},
		stop: make(chan struct{}), wake: make(chan struct{}, 1),
	}, nil
}

// close kills the loop and releases every byte. Idempotent — it is called
// from ALL the death paths: explicit close, TTL expiry, cap eviction, and
// runtime shutdown.
func (f *sessionFetcher) close() {
	f.mu.Lock()
	if f.stopped {
		f.mu.Unlock()
		return
	}
	f.stopped = true
	close(f.stop)
	for _, j := range f.jobs {
		if j.state == FetchRequesting || j.state == FetchPartial {
			j.state, j.reason = FetchCancelled, "preview closed"
		}
		if j.plaintext != nil {
			budgetRelease(int64(len(j.plaintext)))
			j.plaintext = nil
		}
	}
	f.mu.Unlock()
	f.store.release()
}

// extendGraph adds one previewed publication's asset graph to the
// allowlist. Only what a person actually OPENED becomes requestable.
func (f *sessionFetcher) extendGraph(live map[string]string, refs map[string]*schemas.AssetRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for aid, role := range live {
		if _, ok := f.graph[aid]; ok {
			continue
		}
		f.graph[aid] = sessionAsset{Ref: refs[aid], Role: role}
	}
}

// request starts (or returns) the fetch of one allowlisted asset.
// Concurrent requests for the same asset coalesce into one job.
func (f *sessionFetcher) request(aid string) (state string, missing, total int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sa, allowed := f.graph[aid]
	if !allowed {
		return "", 0, 0, "outside the publication's graph"
	}
	if sa.Ref == nil {
		// In the publication, but the projection no longer carries its
		// carrier: a fact about the descriptor, never a network silence.
		return FetchDescriptorGone, 0, 0,
			"this publication does not currently carry enough information to request this media"
	}
	j := f.jobs[aid]
	if j == nil {
		// Peak-preflight (PM-2): encrypted chunks + one plaintext buffer
		// coexist at assembly. An asset whose peak cannot fit is refused
		// at ADMISSION, even when Size alone would.
		peak := int(2*sa.Ref.Size) + 1<<20
		if sa.Ref.Size > assets.MaxAssetSize || peak > previewGlobalBudget {
			j = &fetchJob{state: FetchBudget,
				reason: "this media is larger than the temporary preview limit"}
			f.jobs[aid] = j
			return j.state, 0, 0, j.reason
		}
		j = &fetchJob{state: FetchRequesting, started: time.Now()}
		f.jobs[aid] = j
		f.bumpLocked()
	} else if j.state == FetchNoResponse {
		// An explicit retry after silence re-arms the job and re-pushes.
		j.state, j.started, j.reason = FetchRequesting, time.Now(), ""
		f.bumpLocked()
	}
	return j.state, j.missing, j.total, j.reason
}

func (f *sessionFetcher) bumpLocked() {
	f.wantEpoch++
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// status reads one job without side effects (the GET path stays passive).
func (f *sessionFetcher) status(aid string) (state string, missing, total int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.graph[aid]; !ok {
		return "", 0, 0, "outside the publication's graph"
	}
	j := f.jobs[aid]
	if j == nil {
		return FetchNotRequested, 0, 0, ""
	}
	return j.state, j.missing, j.total, j.reason
}

// bytesFor serves a READY asset's assembled plaintext.
func (f *sessionFetcher) bytesFor(aid string) ([]byte, *schemas.AssetRef, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs[aid]
	sa := f.graph[aid]
	if j == nil || j.state != FetchReady || sa.Ref == nil {
		return nil, nil, false
	}
	return j.plaintext, sa.Ref, true
}

// run is the session's one fetch loop: gather wants across every active
// job, push ONE sorted bundle (byte-stable → one relay slot), collect,
// advance the 2-phase ladder, assemble and verify at completion.
func (f *sessionFetcher) run(runtimeStop <-chan struct{}) {
	var client *relay.Client
	defer func() {
		if client != nil {
			client.Close()
		}
	}()
	lastPush := time.Time{}
	lastEpoch := uint64(0)
	var lastWants string
	for {
		select {
		case <-f.stop:
			return
		case <-runtimeStop:
			return
		case <-f.wake:
		case <-time.After(collectEvery):
		}

		wants, expected := f.gatherWants()
		if len(wants) > 0 {
			if client == nil {
				c, err := f.dial(f.relay)
				if err != nil {
					f.settleDeadlines()
					continue
				}
				client = c
			}
			// Re-push on: set change, explicit bump, or advertisement age.
			key := wantsKey(wants)
			f.mu.Lock()
			epoch := f.wantEpoch
			f.mu.Unlock()
			if key != lastWants || epoch != lastEpoch ||
				time.Since(lastPush) > repushEvery {
				_ = swarmPushWants(client, f.space, f.hints, f.wanter, wants, f.cap)
				lastWants, lastEpoch, lastPush = key, epoch, time.Now()
			}
			swarmCollect(client, f.cap,
				func(h id.Hash) bool { return expected[h] },
				budgetAdmitTracked(f.store), f.store)
			f.advance()
		}
		f.settleDeadlines()
	}
}

// budgetAdmitTracked admits into the global budget and mirrors the
// store's own accounting (the store tracks per-session for release).
func budgetAdmitTracked(_ *sessionStore) func(int) bool {
	return func(n int) bool { return budgetAdmit(n) }
}

// gatherWants collects, across every requesting/partial job, the next
// hashes to want — sorted, so the encoded bundle is byte-stable.
func (f *sessionFetcher) gatherWants() ([][]byte, map[id.Hash]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	expected := map[id.Hash]bool{}
	var wants [][]byte
	for aid, j := range f.jobs {
		if j.state != FetchRequesting && j.state != FetchPartial {
			continue
		}
		sa := f.graph[aid]
		if sa.Ref == nil {
			continue
		}
		hs, complete, err := swarmMissing(f.store, sa.Ref)
		if err != nil || complete {
			continue // advance() settles these
		}
		for _, h := range hs {
			if !expected[h] {
				expected[h] = true
				wants = append(wants, h[:])
			}
		}
	}
	sort.Slice(wants, func(i, j int) bool { return string(wants[i]) < string(wants[j]) })
	return wants, expected
}

// advance moves each job along the ladder and assembles at completion.
// Assembly happens ONCE: RetrieveBytes verifies every chunk AAD and the
// PlaintextDigest, the result is cached for every later Range request,
// and an integrity failure is its own state — never "no source".
func (f *sessionFetcher) advance() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for aid, j := range f.jobs {
		if j.state != FetchRequesting && j.state != FetchPartial {
			continue
		}
		sa := f.graph[aid]
		if sa.Ref == nil {
			continue
		}
		res, err := assets.Missing(f.store, sa.Ref)
		if err != nil {
			continue
		}
		j.total = res.TotalChunks
		j.missing = len(res.MissingChunks)
		if res.ManifestMissing {
			j.missing = res.TotalChunks
			continue
		}
		if j.missing > 0 {
			j.state = FetchPartial
			continue
		}
		data, err := assets.RetrieveBytes(f.store, sa.Ref)
		if err != nil {
			j.state = FetchIntegrity
			j.reason = "the received media did not match its content hash and was not shown"
			continue
		}
		if !budgetAdmit(len(data)) {
			j.state = FetchBudget
			j.reason = "this media is larger than the temporary preview limit"
			continue
		}
		j.plaintext = data
		j.state, j.missing, j.reason = FetchReady, 0, ""
	}
}

// wantsKey is a byte-stable fingerprint of a sorted want set, so an
// unchanged set is recognized without re-encoding the bundle.
func wantsKey(wants [][]byte) string {
	var b []byte
	for _, w := range wants {
		b = append(b, w...)
	}
	return string(b)
}

// settleDeadlines turns long silence into an honest sentence: after the
// deadline all we KNOW is that nobody answered in the window.
func (f *sessionFetcher) settleDeadlines() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if (j.state == FetchRequesting || j.state == FetchPartial) &&
			time.Since(j.started) > fetchDeadline {
			j.state = FetchNoResponse
			j.reason = "no holder answered this request yet"
		}
	}
}
