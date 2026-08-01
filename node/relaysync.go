// Background relay sync: when a relay address is configured (Settings.Relay),
// the node periodically PUSHES spaces that grew since the last cycle and
// PULLS everything waiting for it — no manual push/pull. The push carries
// frames + asset manifests only (media bytes stay on-demand); the relay is
// blind store-and-forward, so "accepted by relay" is never "delivered".
package node

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func (a *APIServer) handleRelayStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.rt.RelaySync())
}

type relaySyncState struct {
	mu       sync.Mutex
	addr     string
	interval time.Duration
	stop     chan struct{}
	lastLen  map[id.TerminalID]int
	lastErr  string
	lastPush time.Time
	lastPull time.Time
	pushed   int
	pulled   int
	// Public projection publishing triggers (PA-0.4B): authorized log
	// growth, bucket rotation, or a stale heartbeat each force a Replace.
	lastPubLen     map[id.TerminalID]int
	lastPubBucket  map[id.TerminalID]uint64
	lastPubRefresh map[id.TerminalID]time.Time
	// Sync tiering (RR-2): tick counts cycles; lastChange remembers when a
	// space last showed activity. Quiet spaces sync on a slow cadence.
	tick       uint64
	lastChange map[id.TerminalID]time.Time
}

// Tiering policy (RR-2): a space with no observed change for quietAfter
// syncs only every quietEvery-th tick (~30s at the 2s default), staggered
// by space id so the quiet ones do not all wake on the same tick. The
// space a person is LOOKING AT is always hot. Renderer-side policy — never
// a contract.
const (
	quietAfter = 60 * time.Second
	quietEvery = 15
)

func quietStagger(tid id.TerminalID) uint64 { return uint64(tid[0]) % quietEvery }

// applyRelaySync (re)starts the background loop for a relay address and
// cadence. An empty address stops it. Safe to call from settings changes.
func (r *Runtime) applyRelaySync(addr string, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	r.mu.Lock()
	if r.relaySync == nil {
		r.relaySync = &relaySyncState{
			lastLen:        map[id.TerminalID]int{},
			lastPubLen:     map[id.TerminalID]int{},
			lastPubBucket:  map[id.TerminalID]uint64{},
			lastPubRefresh: map[id.TerminalID]time.Time{},
			lastChange:     map[id.TerminalID]time.Time{},
		}
	}
	rs := r.relaySync
	r.mu.Unlock()

	rs.mu.Lock()
	if rs.stop != nil {
		close(rs.stop)
		rs.stop = nil
	}
	if rs.addr != addr {
		// A DIFFERENT relay knows nothing we pushed to the old one. Reset
		// the push progress so everything re-publishes there (RR-6): a
		// relay ACK is "accepted by transient memory", never durability —
		// the client's log is the durable copy, and eventId dedup makes
		// the re-push idempotent on every receiver.
		rs.lastLen = map[id.TerminalID]int{}
		rs.lastPubLen = map[id.TerminalID]int{}
		rs.lastPubRefresh = map[id.TerminalID]time.Time{}
	}
	rs.addr = addr
	rs.interval = interval
	rs.lastErr = ""
	if addr == "" {
		rs.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	rs.stop = stop
	rs.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		// Polling jitter (RR-2): a stable per-device phase shift of up to
		// 20% of the interval, derived from the device id — so a thousand
		// clients started together do not pulse the relay on the same
		// tick boundaries. Deterministic: the schedule does not wander.
		phase := time.Duration(uint64(r.Device.ID[0])|uint64(r.Device.ID[1])<<8) %
			(interval / 5)
		select {
		case <-time.After(phase):
		case <-r.stop:
			return
		case <-stop:
			return
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		r.relaySyncOnce(addr) // an immediate first pass
		for {
			select {
			case <-r.stop:
				return
			case <-stop:
				return
			case <-t.C:
				r.relaySyncOnce(addr)
			}
		}
	}()
}

// relaySyncOnce pushes changed spaces and pulls once.
func (r *Runtime) relaySyncOnce(addr string) {
	rs := r.relaySync
	if rs == nil {
		return
	}
	// Snapshot spaces and their current log lengths under the lock.
	r.mu.Lock()
	type spaceLen struct {
		tid     id.TerminalID
		n       int
		pub     bool // owned public space → projection publisher
		reader  bool // reader replica → projection consumer
		contrib bool // joined community member / curator → ingress uplink
		mirror  bool // volunteers to keep the space reachable (PH-3)
		seed    bool // answers wants from blobs already held (PH-3, opt-in)
		// Per-purpose relay addresses (RR-4), resolved after the snapshot.
		readAddr  string
		writeAddr string
	}
	spaces := make([]spaceLen, 0, len(r.spaces))
	for tid, st := range r.spaces {
		meta := r.ks.Spaces[tid]
		// Local-only: no push, no projection, no ingress, no mirroring.
		// Dropping it HERE rather than at each call site is deliberate —
		// this is the one list every relay action is driven from (AI-0).
		if meta.LocalOnly {
			continue
		}
		pol := st.space.Policy()
		spaces = append(spaces, spaceLen{
			tid: tid, n: st.space.Log.Len(),
			pub:    meta.Owned && pol.IsPublic(),
			reader: meta.Role == storage.RoleReader,
			// A joined community member or activated curator: reads via
			// projections like everyone else, uplinks via ingress.
			contrib: !meta.Owned && meta.Role == "" && pol.IsPublic(),
			mirror:  meta.Mirror && pol.IsPublic(),
			// Mirroring implies seeding: a node keeping a space alive that
			// refused to hand out the media it holds would be keeping half
			// a space alive.
			seed: (meta.Seed || meta.Mirror) && pol.IsPublic(),
		})
	}
	r.mu.Unlock()
	// Per-purpose relay resolution (RR-4), outside r.mu — the resolvers
	// take their own locks. `addr` stays the PERSONAL relay: private-space
	// push/pull and the inbox drain live there; public spaces may read and
	// write elsewhere (a followed space keeps its publisher's relay).
	for i := range spaces {
		sp := &spaces[i]
		if sp.pub || sp.reader || sp.contrib {
			sp.readAddr = r.ResolvePublicReadRelay(sp.tid)
			sp.writeAddr = r.ResolvePublicWriteRelay(sp.tid)
		}
	}
	// The open space is always hot: the person is looking at it.
	vst := r.attn()
	vst.mu.Lock()
	viewing := vst.viewing
	vst.mu.Unlock()

	// Sync tiering (RR-2): mark activity, then drop QUIET spaces from most
	// ticks. A space is hot when its log moved recently, when it has media
	// wants pending, or when the person is looking at it right now.
	rs.mu.Lock()
	rs.tick++
	tick := rs.tick
	now0 := time.Now()
	hot := map[id.TerminalID]bool{}
	for _, sp := range spaces {
		r.mu.Lock()
		wanting := len(r.relayWants[sp.tid]) > 0
		r.mu.Unlock()
		if _, seen := rs.lastChange[sp.tid]; !seen {
			// First sight is HOT: a freshly joined/opened space must sync
			// immediately, not wait out a quiet window it never earned.
			rs.lastChange[sp.tid] = now0
		}
		if sp.n != rs.lastLen[sp.tid] || wanting || sp.tid == viewing {
			rs.lastChange[sp.tid] = now0
		}
		quiet := now0.Sub(rs.lastChange[sp.tid]) > quietAfter
		hot[sp.tid] = !quiet || tick%quietEvery == quietStagger(sp.tid)
	}
	rs.mu.Unlock()
	active := spaces[:0]
	for _, sp := range spaces {
		if hot[sp.tid] {
			active = append(active, sp)
		}
	}
	spaces = active

	var lastErr string
	pushed := 0
	for _, sp := range spaces {
		rs.mu.Lock()
		prev := rs.lastLen[sp.tid]
		rs.mu.Unlock()
		// Push when the log grew OR we have an outstanding media request to
		// carry (relayWants): a fetch with no new messages must still get its
		// "wants" out to a holder.
		r.mu.Lock()
		wanting := len(r.relayWants[sp.tid]) > 0
		r.mu.Unlock()
		if sp.n <= prev && !wanting {
			continue // nothing new to push and nothing to ask for
		}
		// Background push is LIGHT: frames + manifests only. Media bytes
		// stay content-addressed and travel on demand, not every cycle.
		n, recipients, _, err := r.pushToRelay(addr, sp.tid, AssetsManifests)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		if recipients == 0 {
			// Nobody addressable yet (fresh joiner before its first pull, or a
			// solo space): leave lastLen untouched so we retry once we learn a
			// peer device, instead of marking these frames as handed off.
			continue
		}
		pushed += n
		rs.mu.Lock()
		rs.lastLen[sp.tid] = sp.n
		rs.mu.Unlock()
	}

	// PA-0.4B — public projections. Publishers Replace their outbox when
	// the log grew, the 6h bucket rotated, or the heartbeat expired (a
	// squatter wipe / relay restart must self-heal without new content).
	// Readers Fetch their spaces' outboxes.
	nowBucket := relayBucketNow()
	// Drain community ingress for ALL owned public spaces in ONE batched
	// Collect PER WRITE RELAY (RR-2/RR-4) — contributions land in the
	// canonical logs BEFORE the publish triggers below look for growth.
	pubsByAddr := map[string][]id.TerminalID{}
	for _, sp := range spaces {
		if sp.pub && sp.writeAddr != "" {
			pubsByAddr[sp.writeAddr] = append(pubsByAddr[sp.writeAddr], sp.tid)
		}
	}
	ingressGot := map[id.TerminalID]int{}
	for wa, pubs := range pubsByAddr {
		got, err := r.collectPublicIngressBatch(wa, pubs)
		if err != nil {
			lastErr = err.Error()
		}
		for tid, n := range got {
			ingressGot[tid] = n
		}
	}
	for _, sp := range spaces {
		if sp.pub {
			if ingressGot[sp.tid] > 0 {
				r.mu.Lock()
				if st, ok := r.spaces[sp.tid]; ok {
					sp.n = st.space.Log.Len()
				}
				r.mu.Unlock()
			}
			rs.mu.Lock()
			rotated := rs.lastPubBucket[sp.tid] != nowBucket
			stale := time.Since(rs.lastPubRefresh[sp.tid]) > publicHeartbeat
			rs.mu.Unlock()
			// Every cycle BUILDS (content changes — log growth, custody
			// flips, aging — surface as a digest change and publish
			// themselves); the relay is force-touched only on bucket
			// rotation or the heartbeat.
			if sp.writeAddr == "" {
				continue
			}
			touched, err := r.publishPublicProjectionForce(sp.writeAddr, sp.tid, rotated || stale)
			if err != nil {
				lastErr = err.Error()
				continue
			}
			rs.mu.Lock()
			rs.lastPubLen[sp.tid] = sp.n
			rs.lastPubBucket[sp.tid] = nowBucket
			if touched {
				rs.lastPubRefresh[sp.tid] = time.Now()
			}
			rs.mu.Unlock()
			continue
		}
		if sp.reader || sp.contrib {
			// Everyone who is not the publisher reads the space through its
			// signed projection — contributors included (their local log
			// holds only their own frames). The READ address prefers the
			// space's own relay (RR-4): a followed space stops being polled
			// on YOUR relay where its publisher never writes.
			if sp.readAddr == "" {
				continue
			}
			if err := r.fetchPublicProjection(sp.readAddr, sp.tid); err != nil {
				// "no projection yet" is routine for a fresh space — only
				// surface real transport errors.
				if !errors.Is(err, ErrNoProjection) {
					lastErr = err.Error()
				}
			}
			// Contributor uplink and/or media wants ride the ingress.
			//
			// The SAME routine condition arrives here wrapped, so it needs
			// the same judgement: a contributor whose publisher is offline
			// keeps its pending set durably and delivers when they return.
			// That is the design working, not a transport fault, and it
			// must not paint the connection light red. What IS worth
			// saying — "N of your contributions are waiting" — belongs to
			// that space's row, not to the relay.
			// The WRITE address (RR-4): a non-owner never uplinks to its
			// own personal relay — publishing where the members do not look
			// is worse than the durable pending set waiting.
			if sp.writeAddr == "" {
				continue
			}
			if err := r.pushPublicIngress(sp.writeAddr, sp.tid); err != nil {
				if !errors.Is(err, ErrNoProjection) {
					lastErr = err.Error()
				}
			}
			// Keep the space reachable, and hand out what we hold. Neither
			// touches anyone else's mailbox: keepalive Puts, seeding Fetches.
			if sp.mirror {
				if err := r.mirrorKeepalive(sp.writeAddr, sp.tid); err != nil {
					lastErr = err.Error()
				}
			}
			if sp.seed {
				if err := r.seedForSpace(sp.writeAddr, sp.tid); err != nil {
					lastErr = err.Error()
				}
			}
		}
	}

	pulled, err := r.PullFromRelay(addr)
	if err != nil {
		lastErr = err.Error()
	}

	rs.mu.Lock()
	rs.lastErr = lastErr
	if pushed > 0 {
		rs.lastPush = time.Now()
		rs.pushed += pushed
	}
	if pulled > 0 {
		rs.lastPull = time.Now()
		rs.pulled += pulled
	}
	rs.mu.Unlock()
}

// RelaySyncStatus is the honest diagnostic for the UI.
type RelaySyncStatus struct {
	Addr   string `json:"addr"`
	Active bool   `json:"active"`
	// IntervalMs is the sync cadence. The UI breathes its connection light
	// in this rhythm, so the pulse means something rather than decorating.
	IntervalMs int                 `json:"interval_ms,omitempty"`
	Pushed     int                 `json:"pushed"`
	Pulled     int                 `json:"pulled"`
	LastErr    string              `json:"last_error,omitempty"`
	AgoPush    int                 `json:"seconds_since_push,omitempty"`
	AgoPull    int                 `json:"seconds_since_pull,omitempty"`
	Public     []PublicSpaceStatus `json:"public,omitempty"`
}

// PublicSpaceStatus is the per-public-space checkpoint/ingress diagnostic
// (PA-1.3): what a publisher is emitting, what a reader has accepted, and
// whether the space is frozen — so an operator can tell "quiet" from
// "stuck" at a glance.
type PublicSpaceStatus struct {
	SpaceID    string `json:"space_id"`
	Title      string `json:"title"`
	Role       string `json:"role"` // publisher | reader | contributor
	Visibility string `json:"visibility"`
	Frozen     bool   `json:"frozen,omitempty"`
	Seq        uint64 `json:"seq,omitempty"`
	// PendingUplink is how many of THIS node's own frames are still
	// waiting to reach the publisher. It exists because the condition that
	// produces it — the publisher being offline — is deliberately NOT a
	// relay error: silence about it would be the opposite mistake to the
	// red light it replaces.
	PendingUplink int    `json:"pending_uplink,omitempty"`
	AgoPublish    int    `json:"seconds_since_publish,omitempty"`
	IgnoredTotal  uint64 `json:"ignored_total,omitempty"`
	// ThrottledTotal counts frames DEFERRED by the owner's contribution
	// limit (IC-1) — separate from IgnoredTotal, which means refused for
	// good. An owner who sets a limit has to be able to see it working, and
	// a deferral must never be reported as a rejection.
	ThrottledTotal uint64 `json:"throttled_total,omitempty"`
	// RatePerCycle echoes the signed limit, so the number in the UI comes
	// from the manifest rather than from what was last typed into it.
	RatePerCycle int `json:"rate_per_cycle,omitempty"`
	// Relay is where this space's traffic actually goes (RR-4): the
	// space's own relay for followed spaces, "" meaning the personal one.
	Relay string `json:"relay,omitempty"`
}

// RelaySync reports the background loop state.
func (r *Runtime) RelaySync() RelaySyncStatus {
	r.mu.Lock()
	rs := r.relaySync
	public := r.publicSpaceStatusesLocked()
	r.mu.Unlock()
	if rs == nil {
		return RelaySyncStatus{Public: public}
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	st := RelaySyncStatus{
		Addr: rs.addr, Active: rs.addr != "" && rs.stop != nil,
		IntervalMs: int(rs.interval / time.Millisecond),
		Pushed:     rs.pushed, Pulled: rs.pulled, LastErr: rs.lastErr,
		Public: public,
	}
	if !rs.lastPush.IsZero() {
		st.AgoPush = int(time.Since(rs.lastPush).Seconds())
	}
	if !rs.lastPull.IsZero() {
		st.AgoPull = int(time.Since(rs.lastPull).Seconds())
	}
	// Publisher freshness comes from the projection refresh timer.
	for i := range st.Public {
		if st.Public[i].Role != "publisher" {
			continue
		}
		if tid, err := id.ParseTerminalID(st.Public[i].SpaceID); err == nil {
			if t := rs.lastPubRefresh[tid]; !t.IsZero() {
				st.Public[i].AgoPublish = int(time.Since(t).Seconds())
			}
		}
	}
	return st
}

// publicSpaceStatusesLocked gathers per-public-space diagnostics. Caller
// holds r.mu.
func (r *Runtime) publicSpaceStatusesLocked() []PublicSpaceStatus {
	var out []PublicSpaceStatus
	for tid, st := range r.spaces {
		pol := st.space.Policy()
		if !pol.IsPublic() {
			continue
		}
		meta := r.ks.Spaces[tid]
		role := "contributor"
		switch {
		case meta.Owned:
			role = "publisher"
		case meta.Role == storage.RoleReader:
			role = "reader"
		}
		out = append(out, PublicSpaceStatus{
			SpaceID:      tid.Hex(),
			Title:        meta.Title,
			Role:         role,
			Visibility:   string(pol.Effective()),
			Frozen:       pol.Frozen,
			Seq:          r.ks.PublicPublish[tid].ProjectionSeq,
			IgnoredTotal: st.space.PolicyStats.IgnoredTotal,

			ThrottledTotal: st.throttled,
			RatePerCycle:   pol.MaxFramesPerAuthor,
			// "" = the personal relay (owner default); a followed space
			// shows its publisher's address. Caller holds r.mu, so this is
			// the meta view, not the resolver.
			Relay: meta.SourceRelay,
		})
		if role != "publisher" {
			out[len(out)-1].PendingUplink = len(st.space.UnackedLocalFrames())
		}
	}
	return out
}
