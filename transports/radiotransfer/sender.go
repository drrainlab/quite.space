// The sending half: cutting a message into frames, and resending only what
// did not arrive.
//
// The behaviour this replaces had no retransmission at all. A message was
// split, the pieces were handed to the radio, and if one was lost the other
// four had already spent their airtime for nothing — nobody noticed which
// piece was missing, and nobody asked for it again. On a carrier measured at
// 96-99% per frame, that turns a five-fragment message into one that arrives
// four times in five at best, and never recovers the fifth.
//
// Selective repeat is the whole idea: the receiver says which fragments of a
// window it has, and the sender resends the GAPS. Resending the window is the
// obvious alternative and it is much worse here — on a carrier where one
// packet costs two seconds, resending seven fragments to recover one is
// fourteen seconds of airtime spent on six that already arrived.
package radiotransfer

import (
	"errors"
	"fmt"
)

// Outbound is one message being sent.
type Outbound struct {
	ID     TransferID
	chunks [][]byte
	total  int
	digest [DigestLen]byte
	// acked marks fragments the receiver has reported holding.
	acked []bool
	// round counts send-and-repair passes, bounded by Limits.MaxRounds so a
	// transfer to a peer that has gone away ends rather than retrying until
	// the process does.
	round int
	// lastBase is how far the window had advanced when the budget was last
	// reset, so progress can be told from repetition.
	lastBase int
	stream   uint64
	lim      Limits
}

// ErrTooLarge is a message that cannot be sent within the limits.
//
// Refused at the SENDER, before any airtime is spent. Starting a transfer
// that cannot finish is the worst of both: the airtime is gone and the
// message still has not arrived.
var ErrTooLarge = errors.New("radiotransfer: the message does not fit the transfer limits")

// NewOutbound cuts a message into fragments sized for the carrier.
func NewOutbound(msg []byte, mtu int, lim Limits, key *TransferKey) (*Outbound, error) {
	return NewOutboundOn(StreamSync, msg, mtu, lim, key)
}

// NewOutboundOn cuts a message for a named stream.
func NewOutboundOn(stream uint64, msg []byte, mtu int, lim Limits, key *TransferKey) (*Outbound, error) {
	lim = lim.withDefaults()
	if err := lim.check(); err != nil {
		return nil, err
	}
	if len(msg) == 0 {
		return nil, errors.New("radiotransfer: nothing to send")
	}
	if len(msg) > lim.MaxMessageBytes {
		return nil, fmt.Errorf("%w: %d bytes, the limit is %d",
			ErrTooLarge, len(msg), lim.MaxMessageBytes)
	}
	id, err := NewTransferID()
	if err != nil {
		return nil, err
	}
	digest := MessageDigest(msg)
	// The probe carries the STREAM too. A control frame is two bytes longer
	// than a sync one, and measuring the smaller shape would put every control
	// fragment over the MTU — on a carrier that enforces it, that reads as a
	// busy radio rather than as a frame this build built wrong.
	chunkSize, err := maxChunk(mtu, id, digest, len(msg), stream, key)
	if err != nil {
		return nil, err
	}
	count := (len(msg) + chunkSize - 1) / chunkSize
	if count > lim.MaxFragmentsPerTransfer {
		return nil, fmt.Errorf("%w: %d bytes needs %d fragments of %d, the limit "+
			"is %d — a carrier this small should be handed smaller messages, not "+
			"a transfer that cannot finish",
			ErrTooLarge, len(msg), count, chunkSize, lim.MaxFragmentsPerTransfer)
	}
	o := &Outbound{ID: id, total: len(msg), digest: digest, stream: stream,
		acked: make([]bool, count), lim: lim}
	for i := range count {
		start := i * chunkSize
		o.chunks = append(o.chunks, msg[start:min(start+chunkSize, len(msg))])
	}
	return o, nil
}

// Count is how many fragments the message became.
func (o *Outbound) Count() int { return len(o.chunks) }

// Frame renders one fragment.
func (o *Outbound) Frame(i int) *Frame {
	return &Frame{
		Kind: KindData, Transfer: o.ID,
		Index: uint64(i), Count: uint64(len(o.chunks)),
		Total: uint64(o.total), Digest: o.digest, Chunk: o.chunks[i],
		Stream: o.stream,
	}
}

// Pending returns the fragments of the current window that have not been
// reported as arrived, in order.
//
// The window advances only when everything below it is acknowledged. A
// receiver holding fragment 9 while 3 is missing has not helped: the message
// assembles from all of them or from none.
func (o *Outbound) Pending() []int {
	base := o.base()
	var out []int
	for i := base; i < len(o.chunks) && i < base+o.lim.Window; i++ {
		if !o.acked[i] {
			out = append(out, i)
		}
	}
	return out
}

// base is the first unacknowledged fragment.
func (o *Outbound) base() int {
	for i, a := range o.acked {
		if !a {
			return i
		}
	}
	return len(o.chunks)
}

// Complete reports whether every fragment has been acknowledged. It is NOT
// the same as delivered: only a COMMIT says the message assembled and its
// digest matched, and only that is reported upward.
func (o *Outbound) Complete() bool { return o.base() == len(o.chunks) }

// NoteSACK folds a receiver's report in.
//
// A SACK for a window we have moved past is ignored rather than treated as
// contradiction: on a lossy carrier an old SACK can arrive after a new one,
// and un-acknowledging a fragment because a stale frame did not mention it
// would resend work that already succeeded.
func (o *Outbound) NoteSACK(f *Frame) {
	if f.Kind != KindSACK || f.Transfer != o.ID {
		return
	}
	base := int(f.Base)
	for i := 0; i < o.lim.Window && base+i < len(o.chunks); i++ {
		if HasBit(f.Bitmap, i) {
			o.acked[base+i] = true
		}
	}
}

// NextRound spends a REPAIR from the budget, and reports whether there is any
// left.
//
// A round is only spent when the window did NOT advance. Counting every
// send-and-wait cycle instead — which is what this did first — makes the
// budget a limit on MESSAGE LENGTH rather than on futility: a forty-fragment
// message needs ten windows before any repair happens, so on a carrier losing
// nothing at all it would run out and report that the peer had stopped
// answering. Observed exactly that way, at 5% loss, 131 frames spent.
//
// Progress resets the budget, so the meaning stays what the error says it is:
// the peer is not answering.
func (o *Outbound) NextRound() bool {
	if base := o.base(); base > o.lastBase {
		o.lastBase, o.round = base, 0
		return true
	}
	o.round++
	return o.round <= o.lim.MaxRounds
}

// Rounds is how many passes this transfer has taken, for a metric that can
// say whether a segment is healthy or merely eventually successful.
func (o *Outbound) Rounds() int { return o.round }

// maxChunk works out the largest chunk that still fits the carrier's MTU.
//
// Measured rather than calculated. The header's size depends on the values in
// it — CBOR varints grow with magnitude, and the chunk's own length prefix
// grows with the chunk — so an arithmetic overhead constant is a number that
// is right until a message crosses 255 bytes. It is computed against the
// LARGEST values this transfer will actually carry, so no later fragment can
// encode bigger than the first.
func maxChunk(mtu int, id TransferID, digest [DigestLen]byte, total int,
	stream uint64, key *TransferKey) (int, error) {
	if mtu < minFrame {
		return 0, fmt.Errorf("radiotransfer: a carrier MTU of %d cannot hold a "+
			"frame header; the minimum this layer can work with is %d", mtu, minFrame)
	}
	probe := func(n int) (int, error) {
		f := &Frame{
			Kind: KindData, Transfer: id,
			// Worst case for every varint: the last index, the full count,
			// the whole size.
			Index: uint64(MaxFragments - 1), Count: MaxFragments,
			Total: uint64(total), Digest: digest,
			Chunk: make([]byte, n), Stream: stream,
		}
		b, err := f.Encode(key)
		if err != nil {
			return 0, err
		}
		return len(b), nil
	}
	size := mtu
	for range 4 {
		n, err := probe(size)
		if err != nil {
			return 0, err
		}
		if n <= mtu {
			break
		}
		size -= n - mtu
		if size <= 0 {
			return 0, fmt.Errorf("radiotransfer: a carrier MTU of %d leaves no "+
				"room for payload after the frame header", mtu)
		}
	}
	// Confirm rather than assume: the loop above converges in two passes, and
	// an off-by-one here would put a frame one byte over the MTU on every
	// carrier that enforces it strictly.
	n, err := probe(size)
	if err != nil {
		return 0, err
	}
	if n > mtu {
		return 0, fmt.Errorf("radiotransfer: could not fit a fragment into an "+
			"MTU of %d", mtu)
	}
	return size, nil
}

// minFrame is the smallest MTU worth attempting: below it the header alone
// does not fit, and every message would fail for a reason that has nothing to
// do with the message.
const minFrame = 64
