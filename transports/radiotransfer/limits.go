// What this layer will spend, stated as numbers rather than discovered as
// behaviour.
//
// Reassembly is a memory-filling surface, and on a broadcast radio segment it
// is a surface every neighbour can reach: their fragments land in our
// reassembler whether we asked for them or not. Today's kernel/sync
// Reassembler has NO timeout, NO eviction and NO cap — a stream that loses one
// fragment stays in its map for the life of the process. That is survivable
// on a LAN where a stream is ours and completes in milliseconds. It is not
// survivable here.
//
// So the limits are part of v1 rather than a follow-up, and they are one
// struct rather than constants scattered through the code: a person tuning a
// segment for a slow band should be able to see the whole budget at once, and
// a test should be able to shrink all of it.
package radiotransfer

import (
	"fmt"
	"time"
)

// Limits is the whole resource budget of one Radio Transfer session.
type Limits struct {
	// MaxFragmentsPerTransfer bounds one message. Beyond this, a message is
	// refused at the SENDER rather than started and abandoned — spending
	// airtime on something that cannot finish is the worst of both.
	MaxFragmentsPerTransfer int
	// MaxMessageBytes bounds the reassembled message.
	MaxMessageBytes int
	// MaxInflightTransfersPerPeer bounds how many transfers one peer may have
	// part-finished here at once. Without it, one neighbour holds as much of
	// our memory as they care to send.
	MaxInflightTransfersPerPeer int
	// MaxInflightBytesPerSegment bounds all of them together, because a peer
	// count alone does not bound bytes and the peer id is not authenticated
	// at this layer.
	MaxInflightBytesPerSegment int

	// ReassemblyTimeout is how long a part-finished transfer is kept without
	// progress. It is not "how long a transfer may take": every fragment that
	// arrives pushes it forward. Only silence ages a transfer out.
	ReassemblyTimeout time.Duration
	// Window is how many fragments are offered before waiting for a SACK.
	Window int
	// MaxRounds bounds the send-and-repair loop for one transfer.
	MaxRounds int
	// SACKDelay is the base delay before a receiver answers, so several
	// receivers do not answer at the same instant. The actual wait is a random
	// value up to this, and a receiver that overhears an equivalent SACK does
	// not send its own.
	SACKDelay time.Duration
	// AckTimeout is how long a sender waits for a SACK before assuming it was
	// lost and resending the outstanding fragments.
	AckTimeout time.Duration
	// DedupTTL is how long a completed transfer id is remembered, so a
	// re-delivery is recognised rather than reassembled a second time.
	DedupTTL time.Duration
	// SendFloor is the shortest wait before offering a frame again to a
	// carrier that said it was full and gave no RetryAfter of its own.
	SendFloor time.Duration
}

// DefaultLimits are sized for the carrier this was measured on: LoRa
// LONG_FAST, about two seconds of airtime per packet, 99% single-frame
// delivery, and a queue of sixteen in the firmware.
func DefaultLimits() Limits {
	return Limits{
		MaxFragmentsPerTransfer:     MaxFragments,
		MaxMessageBytes:             MaxMessageBytes,
		MaxInflightTransfersPerPeer: 4,
		MaxInflightBytesPerSegment:  512 << 10,

		// Generous against the medium, not against a bug: at two seconds a
		// packet a sixteen-fragment transfer with one repair round is already
		// past a minute, and ageing it out mid-flight would throw away work
		// that was going to finish.
		ReassemblyTimeout: 3 * time.Minute,

		// Eight fragments is about sixteen seconds of airtime before anyone
		// says anything back. Larger wastes more when a SACK is lost; smaller
		// spends more of the transfer waiting for one.
		Window:    8,
		MaxRounds: 6,

		SACKDelay:  1500 * time.Millisecond,
		AckTimeout: 20 * time.Second,
		DedupTTL:   10 * time.Minute,
		SendFloor:  500 * time.Millisecond,
	}
}

// withDefaults fills anything left at zero, so a caller may set one field
// without silently zeroing the rest — the same trap that set tx_enabled to
// false on two real radios.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFragmentsPerTransfer <= 0 {
		l.MaxFragmentsPerTransfer = d.MaxFragmentsPerTransfer
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = d.MaxMessageBytes
	}
	if l.MaxInflightTransfersPerPeer <= 0 {
		l.MaxInflightTransfersPerPeer = d.MaxInflightTransfersPerPeer
	}
	if l.MaxInflightBytesPerSegment <= 0 {
		l.MaxInflightBytesPerSegment = d.MaxInflightBytesPerSegment
	}
	if l.ReassemblyTimeout <= 0 {
		l.ReassemblyTimeout = d.ReassemblyTimeout
	}
	if l.Window <= 0 {
		l.Window = d.Window
	}
	if l.MaxRounds <= 0 {
		l.MaxRounds = d.MaxRounds
	}
	if l.SACKDelay <= 0 {
		l.SACKDelay = d.SACKDelay
	}
	if l.AckTimeout <= 0 {
		l.AckTimeout = d.AckTimeout
	}
	if l.DedupTTL <= 0 {
		l.DedupTTL = d.DedupTTL
	}
	if l.SendFloor <= 0 {
		l.SendFloor = d.SendFloor
	}
	// A caller who lowered the fragment cap and said nothing about the window
	// meant a smaller transfer, not an incoherent budget. Clamping here is
	// what stops that from surfacing as an error about a field they never
	// set — which is how a person ends up debugging the wrong thing.
	if l.Window > l.MaxFragmentsPerTransfer {
		l.Window = l.MaxFragmentsPerTransfer
	}
	return l
}

// check refuses a budget that cannot work, rather than one that merely looks
// unusual. The hard ceilings are the wire's, and a limit above them would
// produce transfers the far side refuses to decode.
func (l Limits) check() error {
	switch {
	case l.MaxFragmentsPerTransfer > MaxFragments:
		return fmt.Errorf("radiotransfer: %d fragments per transfer, but the "+
			"wire caps it at %d — the far side would refuse every frame",
			l.MaxFragmentsPerTransfer, MaxFragments)
	case l.MaxMessageBytes > MaxMessageBytes:
		return fmt.Errorf("radiotransfer: %d message bytes, but the wire caps "+
			"it at %d", l.MaxMessageBytes, MaxMessageBytes)
	case l.Window > l.MaxFragmentsPerTransfer:
		return fmt.Errorf("radiotransfer: a window of %d over transfers of at "+
			"most %d fragments", l.Window, l.MaxFragmentsPerTransfer)
	}
	return nil
}
