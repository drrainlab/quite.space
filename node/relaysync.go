// Background relay sync: when a relay address is configured (Settings.Relay),
// the node periodically PUSHES spaces that grew since the last cycle and
// PULLS everything waiting for it — no manual push/pull. The push carries
// frames + asset manifests only (media bytes stay on-demand); the relay is
// blind store-and-forward, so "accepted by relay" is never "delivered".
package node

import (
	"net/http"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func (a *APIServer) handleRelayStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.rt.RelaySync())
}

// relaySyncEvery is the background cadence (store-and-forward tolerates
// minutes of latency; this keeps it lively without hammering the relay).
const relaySyncEvery = 15 * time.Second

type relaySyncState struct {
	mu       sync.Mutex
	addr     string
	stop     chan struct{}
	lastLen  map[id.TerminalID]int
	lastErr  string
	lastPush time.Time
	lastPull time.Time
	pushed   int
	pulled   int
}

// applyRelaySync (re)starts the background loop for a new relay address.
// An empty address stops it. Safe to call from settings changes.
func (r *Runtime) applyRelaySync(addr string) {
	r.mu.Lock()
	if r.relaySync == nil {
		r.relaySync = &relaySyncState{lastLen: map[id.TerminalID]int{}}
	}
	rs := r.relaySync
	r.mu.Unlock()

	rs.mu.Lock()
	if rs.stop != nil {
		close(rs.stop)
		rs.stop = nil
	}
	rs.addr = addr
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
		t := time.NewTicker(relaySyncEvery)
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
		tid id.TerminalID
		n   int
	}
	spaces := make([]spaceLen, 0, len(r.spaces))
	for tid, st := range r.spaces {
		spaces = append(spaces, spaceLen{tid, st.space.Log.Len()})
	}
	r.mu.Unlock()

	var lastErr string
	pushed := 0
	for _, sp := range spaces {
		rs.mu.Lock()
		prev := rs.lastLen[sp.tid]
		rs.mu.Unlock()
		if sp.n <= prev {
			continue // nothing new since the last push
		}
		// Background push is LIGHT: frames + manifests only. Media bytes
		// stay content-addressed and travel on demand, not every cycle.
		n, _, err := r.pushToRelay(addr, sp.tid, AssetsManifests)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		pushed += n
		rs.mu.Lock()
		rs.lastLen[sp.tid] = sp.n
		rs.mu.Unlock()
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
	Addr    string `json:"addr"`
	Active  bool   `json:"active"`
	Pushed  int    `json:"pushed"`
	Pulled  int    `json:"pulled"`
	LastErr string `json:"last_error,omitempty"`
	AgoPush int    `json:"seconds_since_push,omitempty"`
	AgoPull int    `json:"seconds_since_pull,omitempty"`
}

// RelaySync reports the background loop state.
func (r *Runtime) RelaySync() RelaySyncStatus {
	r.mu.Lock()
	rs := r.relaySync
	r.mu.Unlock()
	if rs == nil {
		return RelaySyncStatus{}
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	st := RelaySyncStatus{
		Addr: rs.addr, Active: rs.addr != "" && rs.stop != nil,
		Pushed: rs.pushed, Pulled: rs.pulled, LastErr: rs.lastErr,
	}
	if !rs.lastPush.IsZero() {
		st.AgoPush = int(time.Since(rs.lastPush).Seconds())
	}
	if !rs.lastPull.IsZero() {
		st.AgoPull = int(time.Since(rs.lastPull).Seconds())
	}
	return st
}
