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
}

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
		}
	}
	rs := r.relaySync
	r.mu.Unlock()

	rs.mu.Lock()
	if rs.stop != nil {
		close(rs.stop)
		rs.stop = nil
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
	for _, sp := range spaces {
		if sp.pub {
			// Drain community ingress FIRST: contributions land in the
			// canonical log, then the publish trigger sees the growth.
			if got, err := r.collectPublicIngress(addr, sp.tid); err != nil {
				lastErr = err.Error()
			} else if got > 0 {
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
			touched, err := r.publishPublicProjectionForce(addr, sp.tid, rotated || stale)
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
			// holds only their own frames).
			if err := r.fetchPublicProjection(addr, sp.tid); err != nil {
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
			if err := r.pushPublicIngress(addr, sp.tid); err != nil {
				if !errors.Is(err, ErrNoProjection) {
					lastErr = err.Error()
				}
			}
			// Keep the space reachable, and hand out what we hold. Neither
			// touches anyone else's mailbox: keepalive Puts, seeding Fetches.
			if sp.mirror {
				if err := r.mirrorKeepalive(addr, sp.tid); err != nil {
					lastErr = err.Error()
				}
			}
			if sp.seed {
				if err := r.seedForSpace(addr, sp.tid); err != nil {
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
		})
		if role != "publisher" {
			out[len(out)-1].PendingUplink = len(st.space.UnackedLocalFrames())
		}
	}
	return out
}
