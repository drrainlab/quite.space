// Stranded frames: the ones a relay item cannot carry.
//
// A frame bigger than maxRelayItem is one signed object, and cutting it
// would produce something no verifier accepts — so it never travels by
// relay. That alone would be a bounded loss: one frame. What makes it a
// hole is the chain. Every later frame of that device names it as Previous,
// and a reader who only ever meets the device through a relay parks all of
// them in its reorder buffer, waiting for a predecessor that no push will
// ever bring. Seen as the field case that motivated this file: a newcomer
// admitted with memory=everything, whose room stayed empty — the frames
// were arriving and being held, one oversized frame upstream of all of them.
//
// The honest answer has three parts, and this file is the first two: the
// owner's log NAMES the frame, and the diagnostics bundle carries it; the
// reader's state counts what it holds (eventlog.Pending) so an empty room
// and a stuck room stop looking alike. The third — carrying the frame some
// other way — belongs to a transport that can chunk, not to this one.
//
// AS SHIPPED, NO VALID FRAME CAN REACH THIS. The protocol caps a frame at
// signal.MaxFrameLen (256 KiB) and a relay item carries three times that,
// so the oversize branch in splitBundles is dead for every frame a verifier
// would accept — stranded_test.go pins the two caps in that order. This
// file exists for the day one of them moves: a cap that drifts should
// produce a named frame in the owner's log, not a room that empties in
// silence. And the reader-side count it pairs with is live today, because
// a hole in a chain has more causes than size.
package node

import (
	"log"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// StrandedFrame is one signed frame this node holds and cannot push to any
// relay. Nothing here is secret: ids as short prefixes, a size, a sequence.
type StrandedFrame struct {
	Space  string `json:"space"`  // short prefix
	Event  string `json:"event"`  // short prefix
	Device string `json:"device"` // short prefix — the chain it stalls
	Seq    uint64 `json:"seq"`
	Bytes  int    `json:"bytes"`
	Reason string `json:"reason"`
}

// strandedReason is the one verdict this file hands out. It says what the
// consequence is, not only the cause, because the cause ("too big") sounds
// like a bounded loss and the consequence is not.
const strandedReason = "exceeds the relay item cap; relay-only peers cannot read this device past this sequence"

// noteStranded records the frames a delivery could not hand over because
// one relay item cannot carry them, and tells the owner — once per change,
// not once per cycle: the sync loop runs every couple of seconds for as
// long as the node lives, and a log that repeats itself is a log nobody
// reads. frames and eventIDs are index-aligned (deliverSpaceRouted builds
// them together); oversize indexes into both.
func (r *Runtime) noteStranded(tid id.TerminalID, frames [][]byte, eventIDs []id.EventID, oversize []int) {
	var now []StrandedFrame
	for _, i := range oversize {
		if i < 0 || i >= len(frames) || i >= len(eventIDs) {
			continue
		}
		sf := StrandedFrame{
			Space: tid.Hex()[:8], Event: eventIDs[i].Hex()[:8],
			Bytes: len(frames[i]), Reason: strandedReason,
		}
		if env, err := signal.Decode(frames[i]); err == nil {
			sf.Device = env.Device.Hex()[:8]
			sf.Seq = env.Sequence
		}
		now = append(now, sf)
	}
	sort.Slice(now, func(i, j int) bool {
		if now[i].Device != now[j].Device {
			return now[i].Device < now[j].Device
		}
		return now[i].Seq < now[j].Seq
	})

	r.mu.Lock()
	prev := r.stranded[tid]
	changed := len(prev) != len(now)
	for i := 0; !changed && i < len(now); i++ {
		changed = prev[i] != now[i]
	}
	if len(now) == 0 {
		delete(r.stranded, tid)
	} else {
		if r.stranded == nil {
			r.stranded = map[id.TerminalID][]StrandedFrame{}
		}
		r.stranded[tid] = now
	}
	r.mu.Unlock()

	if !changed {
		return
	}
	if len(now) == 0 {
		log.Printf("relay: space %s has no stranded frames any more", tid.Hex()[:8])
		return
	}
	for _, sf := range now {
		log.Printf("relay: frame %s in space %s (device %s, seq %d) is %d bytes and the relay item cap is %d — "+
			"it was not pushed, and relay-only peers cannot read that device past seq %d",
			sf.Event, sf.Space, sf.Device, sf.Seq, sf.Bytes, maxRelayItem, sf.Seq)
	}
}

// strandedLocked flattens the record for diagnostics. r.mu held.
func (r *Runtime) strandedLocked() []StrandedFrame {
	var out []StrandedFrame
	for _, list := range r.stranded {
		out = append(out, list...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Space != out[j].Space {
			return out[i].Space < out[j].Space
		}
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}
