// Package loopback is the T0 transport (plan §19): an in-memory endpoint
// pair with deterministic fault injection — drops, duplicates, reorder,
// partitions, MTU limits. Faults are driven by a seeded PRNG so every test
// failure reproduces exactly.
package loopback

import (
	"fmt"
	"math/rand"
	"sync"

	"github.com/drrainlab/quiet_places/transports"
)

// Faults configures injected misbehavior. Zero value = perfect link.
type Faults struct {
	Seed          int64
	DropRate      float64 // 0..1 probability a packet vanishes
	DuplicateRate float64 // 0..1 probability a packet arrives twice
	Reorder       bool    // insert at random queue position
	MTU           int     // 0 = unlimited; Send errors above this
}

// Pair is a bidirectional loopback link.
type Pair struct {
	mu          sync.Mutex
	rng         *rand.Rand
	faults      Faults
	partitioned bool
	aToB, bToA  [][]byte
	A, B        *End
}

// End is one side of the pair.
type End struct {
	pair *Pair
	side int // 0 = A, 1 = B
}

// NewPair builds a loopback link with the given faults.
func NewPair(f Faults) *Pair {
	p := &Pair{rng: rand.New(rand.NewSource(f.Seed)), faults: f}
	p.A = &End{pair: p, side: 0}
	p.B = &End{pair: p, side: 1}
	return p
}

// Partition severs (or restores) the link. Packets sent while partitioned
// are dropped — that is what a partition is; the sync protocol must recover
// by re-summarizing, not by transport magic.
func (p *Pair) Partition(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.partitioned = on
}

func (e *End) Send(pkt []byte) error {
	p := e.pair
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.faults.MTU > 0 && len(pkt) > p.faults.MTU {
		return fmt.Errorf("loopback: packet %d exceeds MTU %d", len(pkt), p.faults.MTU)
	}
	if p.partitioned {
		return nil // silently lost, like the real world
	}
	deliveries := 1
	if p.faults.DropRate > 0 && p.rng.Float64() < p.faults.DropRate {
		deliveries = 0
	} else if p.faults.DuplicateRate > 0 && p.rng.Float64() < p.faults.DuplicateRate {
		deliveries = 2
	}
	queue := &p.aToB
	if e.side == 1 {
		queue = &p.bToA
	}
	for range deliveries {
		cp := append([]byte(nil), pkt...)
		if p.faults.Reorder && len(*queue) > 0 {
			pos := p.rng.Intn(len(*queue) + 1)
			*queue = append((*queue)[:pos], append([][]byte{cp}, (*queue)[pos:]...)...)
		} else {
			*queue = append(*queue, cp)
		}
	}
	return nil
}

func (e *End) Poll() [][]byte {
	p := e.pair
	p.mu.Lock()
	defer p.mu.Unlock()
	queue := &p.bToA
	if e.side == 1 {
		queue = &p.aToB
	}
	out := *queue
	*queue = nil
	return out
}

func (e *End) Capabilities() transports.Capabilities {
	return transports.Capabilities{
		MaxPayload: e.pair.faults.MTU,
		Realtime:   true,
		Ack:        transports.AckNone,
	}
}
