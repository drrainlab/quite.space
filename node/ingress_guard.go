package node

import (
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// Owner-side ingress abuse limits (PA-1.3). A public community owner drains
// contributor frames from the relay each cycle; without bounds a single
// noisy author (or a forged flood) could dominate the drain and force
// endless re-verification. These caps are DEFENSE, not policy: legitimate
// content is never lost — over-cap frames stay at the relay (or in the
// contributor's durable pending set) and arrive on a later cycle.
const (
	// Per contributor device, per drain cycle.
	ingressMaxFramesPerAuthorCycle = 48
	ingressMaxBytesPerAuthorCycle  = 1 << 20 // 1 MiB
	// Whole-space ceiling per cycle (all authors combined).
	ingressMaxFramesPerCycle = 512
	// Rejected-frame memory: a frame that failed admission is remembered so
	// a re-pushed copy is dropped cheaply instead of re-verified.
	ingressRejectedTTL = 24 * time.Hour
	ingressRejectedMax = 4096
)

// rejectedRing is a bounded, TTL'd set of EventIDs that failed admission.
// It makes repeated pushes of the same bad frame cheap: the second sight is
// dropped before signature verification. Eviction is oldest-first on
// overflow; expired entries are pruned lazily on insert.
type rejectedRing struct {
	mu    sync.Mutex
	at    map[id.EventID]int64 // eid -> unix nanos expiry
	order []id.EventID         // insertion order for size eviction
}

func newRejectedRing() *rejectedRing {
	return &rejectedRing{at: map[id.EventID]int64{}}
}

// has reports whether eid is a known-rejected frame that has not expired.
func (r *rejectedRing) has(eid id.EventID, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.at[eid]
	if !ok {
		return false
	}
	if now.UnixNano() >= exp {
		delete(r.at, eid) // lazily expire
		return false
	}
	return true
}

// remember records eid as rejected until now+TTL, evicting the oldest entry
// if the ring is full.
func (r *rejectedRing) remember(eid id.EventID, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.at[eid]; ok {
		return
	}
	for len(r.order) >= ingressRejectedMax && len(r.order) > 0 {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.at, oldest)
	}
	r.at[eid] = now.Add(ingressRejectedTTL).UnixNano()
	r.order = append(r.order, eid)
}

// authorBudget tracks one drain cycle's per-author consumption.
type authorBudget struct {
	frames map[id.DeviceID]int
	bytes  map[id.DeviceID]int
	total  int
}

func newAuthorBudget() *authorBudget {
	return &authorBudget{frames: map[id.DeviceID]int{}, bytes: map[id.DeviceID]int{}}
}

// admit reports whether a frame of size sz from dev fits within this cycle's
// caps; when it fits, the budget is consumed.
func (b *authorBudget) admit(dev id.DeviceID, sz int) bool {
	if b.total >= ingressMaxFramesPerCycle {
		return false
	}
	if b.frames[dev] >= ingressMaxFramesPerAuthorCycle {
		return false
	}
	if b.bytes[dev]+sz > ingressMaxBytesPerAuthorCycle {
		return false
	}
	b.frames[dev]++
	b.bytes[dev] += sz
	b.total++
	return true
}
