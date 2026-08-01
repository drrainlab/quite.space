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
//
// TWO TIERS, AND THE DIFFERENCE IS WHO IS BEING BELIEVED.
//
// The DEFENCE caps (admit) are charged before anything is verified, because
// their whole job is to bound the work of verifying. They are keyed on the
// CLAIMED signer device, which anyone can forge — and that is tolerable only
// because they are loose: a forged flood delays a real contributor by a cycle
// or two, and their frames arrive on the next one.
//
// The owner's POLICY limit (withinPolicy/charge) is charged only once Absorb
// has accepted a frame, so it counts frames that were really signed by that
// device. Charging it up front would have been three lines shorter and would
// have handed anyone a way to silence a named contributor: forge their device
// id, spend their allowance, repeat every cycle. A cheap unauthenticated key
// is fine for a ceiling on work and is not fine for a ceiling on speech.
type authorBudget struct {
	frames map[id.DeviceID]int
	bytes  map[id.DeviceID]int
	total  int
	// admitted counts frames that actually passed Absorb this cycle.
	admitted map[id.DeviceID]int
	// policyLimit is the owner's signed cap, or 0 for "defence only".
	policyLimit int
}

func newAuthorBudget(policyLimit int) *authorBudget {
	return &authorBudget{
		frames: map[id.DeviceID]int{}, bytes: map[id.DeviceID]int{},
		admitted: map[id.DeviceID]int{}, policyLimit: policyLimit,
	}
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

// withinPolicy reports whether dev has room left under the owner's limit.
// Checked before Absorb (so an over-limit frame costs no verification), but
// only ever consumed by charge, after one succeeds.
func (b *authorBudget) withinPolicy(dev id.DeviceID) bool {
	return b.policyLimit == 0 || b.admitted[dev] < b.policyLimit
}

// charge records one VERIFIED frame from dev against the owner's limit.
func (b *authorBudget) charge(dev id.DeviceID) { b.admitted[dev]++ }
