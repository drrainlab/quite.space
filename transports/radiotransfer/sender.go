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
	"time"
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

	// gen is the burst this transfer is currently sending, stamped into
	// every DATA frame; peerSpokeGen records whether ANY SACK has echoed a
	// generation, which is how a new sender recognises an old receiver and
	// keeps the old behaviour for it.
	gen          uint64
	peerSpokeGen bool
	// freshProgress is set only when a CURRENT-generation SACK advanced the
	// window base. It is the one thing allowed to reset the futility budget:
	// evidence merged from history proves delivery but says nothing about
	// whether the peer is answering NOW.
	freshProgress bool

	// submitted marks fragments the CARRIER has accepted at least once.
	//
	// The name is exact and the exactness is the point. This layer does not
	// know whether a frame left the antenna: a successful Send means the modem
	// took the bytes, and the gap between those two facts is the whole defect
	// the repair budget exists to survive. A field called `sent` would smuggle
	// the same false claim back into the code, so there is no such field.
	//
	// A RETRANSMISSION is therefore a repeat successful submission of the same
	// fragment. That charges conservatively — a frame the carrier queued and
	// never radiated still counts — which for a safety budget is the right
	// direction to be wrong in, and it needs nothing from the carrier.
	submitted []bool

	// repairFrames and repairBytes count only retransmitted DATA, and
	// repairStartedAt is zero until the repair phase begins. NONE of them is
	// ever reset: that is what makes a transfer finite regardless of how much
	// stale progress arrives.
	repairFrames    int
	repairBytes     int
	repairAirtime   time.Duration
	repairStartedAt time.Time
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
		acked: make([]bool, count), submitted: make([]bool, count), lim: lim}
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

// sackClass is what a SACK is entitled to do, decided by its generation.
type sackClass int

const (
	// sackCurrent answers the burst in flight: it may end the response wait,
	// choose a retransmission, and reset the futility budget.
	sackCurrent sackClass = iota
	// sackStale is history. Its acknowledged bits are still evidence of
	// delivery and are merged — but it triggers nothing, resets nothing, and
	// answers nothing. Fourteen of these in a row, each intact and each
	// describing a window a minute gone, are what turned one lost frame into
	// ninety-four duplicates on the real boards.
	sackStale
	// sackFuture claims a burst this sender has not sent: a desync. Its bits
	// are merged as evidence; everything else about it is ignored.
	sackFuture
)

// NextGeneration opens a new burst. Every DATA frame sent from now carries
// its number, and only a SACK echoing it counts as an answer.
func (o *Outbound) NextGeneration() { o.gen++ }

// Generation is the burst currently in flight.
func (o *Outbound) Generation() uint64 { return o.gen }

// NoteSACK folds a receiver's report in and says what the report may do.
//
// The acknowledged bits are merged WHATEVER the class — un-acknowledging a
// fragment because a stale frame did not mention it would resend work that
// already succeeded, and evidence of delivery does not expire. What expires
// is authority: only a SACK echoing the current burst may drive a decision.
//
// A receiver that has never echoed a generation is an older build, and for
// it every SACK stays current — the old behaviour, chosen by evidence rather
// than by configuration.
func (o *Outbound) NoteSACK(f *Frame) sackClass {
	if f.Kind != KindSACK || f.Transfer != o.ID {
		return sackStale
	}
	if f.Generation > 0 {
		o.peerSpokeGen = true
	}
	class := sackCurrent
	switch {
	case !o.peerSpokeGen:
		// legacy peer: current by construction
	case f.Generation == o.gen:
		// the answer to the burst in flight
	case f.Generation > o.gen:
		class = sackFuture
	default:
		class = sackStale
	}

	before := o.base()
	base := int(f.Base)
	for i := 0; i < o.lim.Window && base+i < len(o.chunks); i++ {
		if HasBit(f.Bitmap, i) {
			o.acked[base+i] = true
		}
	}
	if class == sackCurrent && o.base() > before {
		o.freshProgress = true
	}
	return class
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
//
// AND THAT RESET IS WHY A SECOND, ABSOLUTE BUDGET EXISTS. This rule is correct
// for what it measures and unsafe on its own: when feedback arrives a whole
// window late, every stale SACK acknowledges a fragment or two, the base
// advances, and the budget resets again — apparent progress made out of
// history. Measured on two Heltec v3: fifteen windows against a MaxRounds of
// six. Limits.Repair bounds the cost that this bounds the futility of; see
// RepairBudget.
// Only FRESH progress renews it: an advance assembled out of stale SACKs
// proves the peer heard an old burst, not that it is answering this one, and
// letting history renew a liveness budget is the exact loop measured live.
func (o *Outbound) NextRound() bool {
	if o.freshProgress {
		if base := o.base(); base > o.lastBase {
			o.lastBase, o.round, o.freshProgress = base, 0, false
			return true
		}
		o.freshProgress = false
	}
	o.round++
	return o.round <= o.lim.MaxRounds
}

// WasSubmitted reports whether the carrier has already accepted this fragment,
// which is exactly what makes offering it again a RETRANSMISSION.
func (o *Outbound) WasSubmitted(i int) bool {
	return i >= 0 && i < len(o.submitted) && o.submitted[i]
}

// MarkSubmitted records that the carrier accepted this fragment.
func (o *Outbound) MarkSubmitted(i int) {
	if i >= 0 && i < len(o.submitted) {
		o.submitted[i] = true
	}
}

// AllSubmitted reports whether every fragment has been handed over at least
// once. It is the second way a repair phase can begin — see BeginRepair.
func (o *Outbound) AllSubmitted() bool {
	for _, s := range o.submitted {
		if !s {
			return false
		}
	}
	return true
}

// AnyAcked reports whether the peer has EVER acknowledged a fragment of this
// transfer — the evidence that it is participating at all. It gates the poll
// ladder: a POLL asks "what do you hold of this transfer", and a peer that
// never heard frame one answers an unknown-transfer question with silence by
// design. Polling it buys nothing and DELAYS the one thing that would help,
// which is resending the data. Found live: two simultaneous announces
// collided, and each sender then spent PollRetries×AckTimeout politely asking
// a peer that had nothing to be asked about.
func (o *Outbound) AnyAcked() bool {
	for _, a := range o.acked {
		if a {
			return true
		}
	}
	return false
}

// AnySubmitted reports whether ANY fragment reached the carrier.
//
// It is the line between "this was never attempted" and "the peer's state is
// unknown", and that line is not knowable from an error's identity: the same
// broken serial link means a local failure before the first frame and an
// unconfirmed delivery after the eighth.
func (o *Outbound) AnySubmitted() bool {
	for _, s := range o.submitted {
		if s {
			return true
		}
	}
	return false
}

// BeginRepair stamps the start of the repair phase, once.
//
// The caller decides WHEN, and there are exactly two moments, because there are
// two ways a transfer stops making first-time progress:
//
//  1. it is about to resubmit a fragment the carrier already accepted;
//  2. every fragment has been submitted and completion is still unconfirmed.
//
// The second is not a refinement. It is the case measured on real boards —
// every fragment held by the peer, the COMMIT lost — where the pending set is
// empty, NOT ONE further frame is ever sent, and a budget counting frames
// would wait forever for something to count.
//
// It must not be called at transfer creation, or a long first pass would spend
// the budget meant to bound its repair.
func (o *Outbound) BeginRepair(now time.Time) {
	if o.repairStartedAt.IsZero() {
		o.repairStartedAt = now
	}
}

// Repairing reports whether the repair phase has begun.
func (o *Outbound) Repairing() bool { return !o.repairStartedAt.IsZero() }

// ChargeRepairFrame spends one retransmitted DATA frame from the budget.
//
// Called only after the carrier ACCEPTED the frame: a frame it refused cost
// nothing and must not be charged, or a busy radio would exhaust the budget
// that exists to bound a talkative one.
func (o *Outbound) ChargeRepairFrame(size int) {
	o.repairFrames++
	o.repairBytes += size
}

// ChargeRepairAirtime spends estimated air from the budget, when a carrier
// can price it. Like frames and bytes, it is never returned.
func (o *Outbound) ChargeRepairAirtime(d time.Duration) {
	o.repairAirtime += d
}

// RepairFrames and RepairBytes are what the repair phase has spent.
func (o *Outbound) RepairFrames() int            { return o.repairFrames }
func (o *Outbound) RepairBytes() int             { return o.repairBytes }
func (o *Outbound) RepairAirtime() time.Duration { return o.repairAirtime }

// RepairElapsed is how long the repair phase has run, or zero before it began.
func (o *Outbound) RepairElapsed(now time.Time) time.Duration {
	if o.repairStartedAt.IsZero() {
		return 0
	}
	return now.Sub(o.repairStartedAt)
}

// OverBudget reports which absolute limit has been reached, or nil.
//
// Nothing here consults progress, and that is the entire point: a SACK may
// prove a fragment arrived, and it still does not return airtime already spent.
func (o *Outbound) OverBudget(now time.Time) error {
	if o.repairStartedAt.IsZero() {
		return nil // the repair phase has not begun; nothing is being spent
	}
	b := o.lim.Repair
	if b.MaxFrames > 0 && o.repairFrames >= b.MaxFrames {
		return ErrRepairFrameBudgetExhausted
	}
	if b.MaxDuration > 0 && now.Sub(o.repairStartedAt) >= b.MaxDuration {
		return ErrRepairDeadlineExhausted
	}
	// Zero means DISABLED, not unlimited-by-omission: only a carrier that
	// can price a frame turns this dimension on.
	if b.MaxAirtime > 0 && o.repairAirtime >= b.MaxAirtime {
		return ErrRepairAirtimeBudgetExhausted
	}
	return nil
}

// RepairDeadline is when the repair phase runs out, or the zero time when it
// has not begun or is not bounded. It is what keeps a wait from overshooting
// the budget by a whole AckTimeout.
func (o *Outbound) RepairDeadline() time.Time {
	if o.repairStartedAt.IsZero() || o.lim.Repair.MaxDuration <= 0 {
		return time.Time{}
	}
	return o.repairStartedAt.Add(o.lim.Repair.MaxDuration)
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
