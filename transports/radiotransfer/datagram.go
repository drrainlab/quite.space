// Quiet Radio Transfer: delivering a whole EVENT over a radio that only
// carries small, lossy, unordered frames.
//
// # Why this layer exists
//
// Meshtastic moves single frames well. Measured on two real boards two metres
// apart, on a band shared with eleven foreign nodes: 96% of single
// self-contained packets arrived with the stock rebroadcast mode, 99% with
// LOCAL_ONLY, and the firmware refused nothing. Losses were scattered, never
// consecutive — collisions, not a broken link.
//
// A Quiet event is not a single frame. Split into five, a 96% carrier
// delivers the whole message about eight times in ten, and nothing above
// notices which fragment went missing or asks for it again. There was no
// retransmission anywhere: the sync engine offered a message, the carrier
// dropped one piece of it, and the remaining four pieces had already spent
// their airtime for nothing.
//
// So this layer owns delivery of a whole message, and nothing else does.
//
// # Why it is not tied to Meshtastic
//
// Meshtastic becomes ONE carrier rather than "the radio". The contract below
// is the entire surface this layer needs, and a later RNode driver satisfies
// the same four methods — which makes "RNode is a second small driver" a
// checkable claim instead of an aspiration.
package radiotransfer

import (
	"context"
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/transports"
)

// RadioAddress names a peer on the carrier. It is OPAQUE here: Meshtastic
// node numbers, RNode addresses and whatever comes next have nothing in
// common worth modelling, and this layer never needs to interpret one — only
// to hand it back to the carrier it came from.
//
// A nil address means broadcast.
type RadioAddress []byte

// RadioDatagram is what Radio Transfer needs from a carrier.
//
// transports.Endpoint (Send/Poll/Capabilities) is deliberately not used: it
// has no frame boundaries in its type, no MTU worth the name, and no way to
// address one peer rather than the segment. All three are load-bearing here —
// a repair request must go to the node that is missing something, not to
// everybody.
type RadioDatagram interface {
	// MTU is defined exactly, because two carriers measuring it at different
	// layers will disagree only at a boundary size, which is the hardest
	// possible place to notice:
	//
	//	MTU = the maximum number of Radio Transfer frame bytes that Send
	//	      accepts atomically — EXCLUDING carrier framing, INCLUDING the
	//	      whole Radio Transfer header and MAC.
	//
	// It must not change while a transfer is in flight; a carrier whose MTU
	// varies reports its smallest.
	MTU() int

	// Send hands one frame to the carrier. A nil dst is a broadcast.
	//
	// It returns ErrCarrierFull when the carrier will not take it now. That
	// is a normal answer on a radio, not a failure of the transfer.
	Send(ctx context.Context, dst RadioAddress, frame []byte) error

	// Receive returns the next frame, blocking until one arrives or ctx ends.
	Receive(ctx context.Context) (src RadioAddress, frame []byte, err error)

	// Credit is a HINT, never a reservation.
	//
	// It can go stale between the read and the Send — another goroutine, the
	// firmware's own traffic, a neighbour's rebroadcast. So:
	//
	//	Credit is advisory.
	//	Send may still return ErrCarrierFull.
	//	RetryAfter is the earliest useful retry time, not a promise of room.
	//
	// This layer PACES with Credit and takes its CORRECTNESS from the result
	// of Send — never from a snapshot of somebody else's queue.
	Credit() transports.Credit
}

// ErrCarrierFull is the carrier declining a frame right now. It is returned
// by Send, and it is data rather than an error condition: the sender waits
// and offers the same frame again.
var ErrCarrierFull = errors.New("radiotransfer: the carrier will not take a frame right now")

// pace works out how long to wait before offering a frame again.
//
// The carrier's own RetryAfter is preferred over any interval invented here:
// it is the only number in the system derived from what the radio actually
// knows. Where the carrier says nothing, a floor keeps a busy loop from
// spending the CPU that the radio is not spending on airtime.
func pace(c transports.Credit, floor time.Duration) time.Duration {
	if c.RetryAfter > 0 {
		return c.RetryAfter
	}
	return floor
}
