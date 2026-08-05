// What a carrier declined to carry, kept so somebody can be told.
//
// The sync engine refuses an event that exceeds what a carrier says it will
// carry. That refusal is correct — an inline preview of 40 KiB is six and a
// half minutes of air during which nothing else moves — but a refusal nobody
// hears is worse than the jam it replaces. A jam finishes; a silence is
// indistinguishable from a message that was never sent.
package node

import (
	"sync"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// TooLargeFor records one event a carrier would not take.
type TooLargeFor struct {
	Space   id.TerminalID
	Size    int
	Ceiling int
}

// tooLarge is device-local and memory-only, like every other honesty
// projection here: it describes what this node observed, and observations do
// not belong in anybody's log.
type tooLargeSet struct {
	mu sync.Mutex
	m  map[id.EventID]TooLargeFor
}

func (r *Runtime) noteTooLargeForCarrier(eid id.EventID, tid id.TerminalID, size, ceiling int) {
	r.tooLarge.mu.Lock()
	defer r.tooLarge.mu.Unlock()
	if r.tooLarge.m == nil {
		r.tooLarge.m = map[id.EventID]TooLargeFor{}
	}
	r.tooLarge.m[eid] = TooLargeFor{Space: tid, Size: size, Ceiling: ceiling}
}

// WaitingForAWiderPath reports what this event is waiting for, if anything.
//
// The second return distinguishes "nothing is wrong with it" from "it is
// stuck" — the same distinction the delivery ladder insists on everywhere
// else, and the reason this is not a bare bool.
func (r *Runtime) WaitingForAWiderPath(eid id.EventID) (TooLargeFor, bool) {
	r.tooLarge.mu.Lock()
	defer r.tooLarge.mu.Unlock()
	t, ok := r.tooLarge.m[eid]
	return t, ok
}
