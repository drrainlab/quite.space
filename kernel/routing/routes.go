// Route projections (TN-1): LOCAL views of which links carried which
// destinations recently — never events, never replicated (ADR-015 §8:
// topology does not belong in a space log).
package routing

import (
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// RouteInfo summarizes one destination's recent delivery surface.
type RouteInfo struct {
	Destination  id.TerminalID
	Links        []string // link ids, most recent first
	LastActivity time.Time
	CustodyDepth int // frames currently held for this destination
}

// Routes tracks per-destination link activity, bounded.
type Routes struct {
	mu    sync.Mutex
	byDst map[id.TerminalID]*routeRec
	cap   int
}

type routeRec struct {
	links map[LinkID]time.Time
	last  time.Time
	depth int
}

// NewRoutes creates a bounded projection (cap<=0 → 1024 destinations).
func NewRoutes(capacity int) *Routes {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Routes{byDst: map[id.TerminalID]*routeRec{}, cap: capacity}
}

// Observe records that a frame for dest moved on link.
func (r *Routes) Observe(dest id.TerminalID, link LinkID, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byDst[dest]
	if !ok {
		if len(r.byDst) >= r.cap {
			// Evict the stalest destination.
			var oldest id.TerminalID
			var oldestAt time.Time
			first := true
			for d, rr := range r.byDst {
				if first || rr.last.Before(oldestAt) {
					oldest, oldestAt, first = d, rr.last, false
				}
			}
			delete(r.byDst, oldest)
		}
		rec = &routeRec{links: map[LinkID]time.Time{}}
		r.byDst[dest] = rec
	}
	rec.links[link] = now
	rec.last = now
}

// SetDepth records current custody depth for a destination.
func (r *Routes) SetDepth(dest id.TerminalID, depth int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.byDst[dest]; ok {
		rec.depth = depth
	} else if depth > 0 {
		r.byDst[dest] = &routeRec{links: map[LinkID]time.Time{}, depth: depth}
	}
}

// Info projects one destination.
func (r *Routes) Info(dest id.TerminalID) (RouteInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byDst[dest]
	if !ok {
		return RouteInfo{}, false
	}
	type lv struct {
		l  LinkID
		at time.Time
	}
	links := make([]lv, 0, len(rec.links))
	for l, at := range rec.links {
		links = append(links, lv{l, at})
	}
	sort.Slice(links, func(i, j int) bool { return links[i].at.After(links[j].at) })
	out := RouteInfo{Destination: dest, LastActivity: rec.last, CustodyDepth: rec.depth}
	for _, x := range links {
		out.Links = append(out.Links, string(x.l))
	}
	return out, true
}
