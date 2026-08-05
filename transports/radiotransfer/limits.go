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
	// SACKCoalesce is how long a sender keeps LISTENING after the first
	// answer to its current burst, merging whatever else arrives, before
	// deciding what to resend.
	//
	// It exists because the receiver reports per frame while no explicit
	// end-of-burst exists: the first answer describes the window's start,
	// and the trace that forced this showed thirteen fresher reports queued
	// behind it. Deciding on the first is repairing against the oldest
	// possible view. This is INSURANCE, not the fix — the burst protocol's
	// EOB/one-SACK exchange replaces it properly — and it is bounded small
	// so it can never masquerade as patience.
	SACKCoalesce time.Duration
	// DedupTTL is how long a completed transfer id is remembered, so a
	// re-delivery is recognised rather than reassembled a second time.
	DedupTTL time.Duration
	// SendFloor is the shortest wait before offering a frame again to a
	// carrier that said it was full and gave no RetryAfter of its own.
	SendFloor time.Duration
	// FrameGap is the pause between consecutive DATA frames of one window.
	//
	// Measured, not theorised. On the real boards, single frames spaced
	// three seconds apart arrived 96-99% of the time; the same frames
	// offered back-to-back arrived about 9% of the time — the firmware
	// queued all of them happily (queue 14/16 free, refused 0) and the far
	// radio simply did not hear most of what went on the air in a burst.
	// Bursts of fragments are precisely what the original nine-day failure
	// was made of, and a repair layer that reproduces the burst reproduces
	// the failure with better bookkeeping.
	FrameGap time.Duration

	// BurstAirtime bounds how long ONE burst may occupy the air before the
	// sender falls silent and listens.
	//
	// It is the burst's size expressed in the only unit that transfers
	// between carriers: three frames on this PHY and six on a faster one are
	// the same courtesy. The trade is stated rather than implied — capping a
	// burst costs a few percent of useful airtime and buys a link where no
	// side is deaf for a minute, a loss that spoils one burst rather than a
	// window, and room for the answer this protocol now explicitly turns
	// around for. Only meaningful on a carrier with an AirtimeModel; without
	// one, a burst is the window.
	BurstAirtime time.Duration
	// PollRetries is how many short POLLs a silent response slot costs
	// before any DATA is retransmitted. A POLL is one small frame; the
	// alternative it replaces was resending the whole burst to provoke the
	// same answer.
	PollRetries int

	// MaxQueuedAirtime bounds how much UNTRANSMITTED air may sit in the
	// carrier's queue before this layer stops offering frames and waits.
	//
	// It is the fix for the measured failure mode: a sender that hands the
	// modem frames faster than the modem can radiate them builds a queue that
	// delays every answer by the queue's whole length — feedback becomes
	// history, and repair against history is amplification. FrameGap alone
	// cannot express this, because one interval means nothing across
	// different spreading factors, bandwidths and payload sizes.
	//
	// Only enforced on a carrier that implements AirtimeModel; inert
	// otherwise, because without a model of airtime "queued airtime" is not
	// a number anyone honestly has.
	MaxQueuedAirtime time.Duration

	// Repair bounds what one transfer may spend AFTER its first pass, and
	// nothing in it is ever restored by progress. See RepairBudget.
	Repair RepairBudget
}

// RepairBudget makes a transfer finite by construction.
//
// MaxRounds already bounds FUTILITY, and correctly: NextRound spends a round
// only when the window did not advance, so a long message is not mistaken for
// a failing one. What it does not bound is COST, and on a real link the two
// came apart badly. Feedback arrives late enough to be a history rather than a
// report; every stale SACK acknowledges one or two more fragments; each of
// those looks like progress and resets the round budget; and the loop runs on.
// Measured on two Heltec v3: fifteen windows against a MaxRounds of six,
// ninety-four duplicate frames pressed on a peer that already held the whole
// message, one transfer spending eight and a half minutes.
//
// So there is a second budget, and its rule is absolute:
//
//	No SACK, however useful, returns airtime that has already been spent.
//
// This is a SAFETY floor, not a tuning knob. It does not make a transfer
// efficient — that is the burst protocol's job. It makes a pathology bounded,
// so that a defect in some future scheduler cannot capture the air.
type RepairBudget struct {
	// MaxFrames bounds RETRANSMITTED DATA frames — a fragment the carrier
	// accepted once already. It deliberately does not count the first
	// transmission, or SACK, or anything else on the control stream: cheap
	// control traffic must not eat a budget that exists to bound expensive
	// data amplification.
	//
	// Zero derives a default from Window; negative is refused.
	MaxFrames int

	// MaxDuration bounds the wall clock from the START OF THE REPAIR PHASE,
	// not from the start of the transfer — otherwise a long first pass would
	// pre-spend the budget meant to bound its repair.
	//
	// It is not redundant with MaxFrames, and the case that proves it is the
	// one this was written for: when every fragment has been submitted and
	// only the final confirmation was lost, there is nothing left to resend,
	// the pending set is empty, and the loop spins sending NO FRAMES AT ALL.
	// A frame budget can never fire there.
	//
	// Zero derives a default; negative is refused.
	MaxDuration time.Duration

	// MaxAirtime is the honest unit for a radio — twenty short frames and
	// twenty long ones are not the same cost — and it is switched OFF here,
	// on purpose.
	//
	// Pricing a frame needs a carrier that can say what it actually spent,
	// and today none can: Send means "the modem accepted the bytes", which is
	// exactly the confusion the next gate exists to fix. A number called
	// airtime that nobody can trust would be worse than a coarse but honest
	// frame count.
	//
	// ZERO MEANS DISABLED, not "use a default" — the only field here where
	// that is so, because it is the only one that has a meaningful off. A
	// carrier that can price its own transmission turns it on explicitly.
	MaxAirtime time.Duration
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

		SACKDelay: 1500 * time.Millisecond,
		// Must exceed Window×FrameGap, or the sender times out while its own
		// window is still leaving the antenna.
		AckTimeout: 45 * time.Second,
		DedupTTL:   10 * time.Minute,
		SendFloor:  500 * time.Millisecond,
		FrameGap:   2500 * time.Millisecond,

		// One long frame, roughly: enough to keep the antenna busy, nowhere
		// near enough to bury the feedback path. The measured catastrophe
		// queued ~30 seconds of air.
		MaxQueuedAirtime: 6 * time.Second,

		// Three to four long frames on the measured PHY — the owner's
		// 12-18 s range, taken at the top so a burst is a real burst.
		BurstAirtime: 18 * time.Second,
		PollRetries:  2,
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
	if l.SACKCoalesce < 0 {
		l.SACKCoalesce = 0 // explicitly none, for a protocol with a real EOB
	} else if l.SACKCoalesce == 0 {
		// Derived from SACKDelay, not from a constant: the coalesce window
		// exists to absorb the receiver's own reporting interval, so it
		// scales with it — including in tests that shrink everything.
		l.SACKCoalesce = l.SACKDelay
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
	if l.FrameGap < 0 {
		l.FrameGap = 0
	} else if l.FrameGap == 0 {
		l.FrameGap = d.FrameGap
	}
	if l.MaxQueuedAirtime <= 0 {
		l.MaxQueuedAirtime = d.MaxQueuedAirtime
	}
	if l.BurstAirtime <= 0 {
		l.BurstAirtime = d.BurstAirtime
	}
	if l.PollRetries < 0 {
		l.PollRetries = 0 // explicitly none: straight to DATA retransmission
	} else if l.PollRetries == 0 {
		l.PollRetries = d.PollRetries
	}
	// A caller who lowered the fragment cap and said nothing about the window
	// meant a smaller transfer, not an incoherent budget. Clamping here is
	// what stops that from surfacing as an error about a field they never
	// set — which is how a person ends up debugging the wrong thing.
	if l.Window > l.MaxFragmentsPerTransfer {
		l.Window = l.MaxFragmentsPerTransfer
	}

	// The repair budget is DERIVED from the window, and derived last, because
	// it is only meaningful once Window, FrameGap and AckTimeout have settled.
	// A caller who widened the window meant a bigger transfer, not a budget
	// that no longer fits it.
	//
	// Note the test is `== 0`, not the `<= 0` used everywhere above. Here a
	// negative is a MISTAKE rather than an omission, and quietly replacing it
	// with a default would hide it; check() refuses it by name instead.
	if l.Repair.MaxFrames == 0 {
		// TWICE the caller's own patience, and scaled by MaxRounds on
		// purpose. The first draft derived 3×Window, ignoring MaxRounds — and
		// a caller who set MaxRounds to thirty for a very lossy link had
		// bought patience the budget then silently confiscated: an e2e at 20%
		// loss failed 21 of 25 transfers with ZERO given up, every one killed
		// by a budget tighter than the honest repair path it was meant to
		// backstop. The absolute budget bounds the PATHOLOGY — a round
		// counter reset forever by stale progress — and the pathology is
		// unbounded, so twice the legitimate ceiling still catches it while
		// never preempting an honest lossy repair.
		l.Repair.MaxFrames = 2 * l.MaxRounds * l.Window
	}
	if l.Repair.MaxDuration == 0 {
		// FOUR times what an honestly futile transfer costs, and both numbers
		// in that sentence matter.
		//
		// Times the FUTILE RUN, because this deadline is an outer backstop,
		// not a second opinion: a peer that went quiet must run out of ROUNDS
		// and be reported as such, or ErrGaveUp becomes unreachable and "they
		// stopped answering" collapses into "I hit a ceiling". The first
		// draft used two cycles rather than runs and was tighter than the
		// normal path — caught by TestAPeerThatStopsAnsweringIsReportedAsSuch.
		//
		// FOUR rather than two, because rounds are counted in events and this
		// deadline in wall clock, and wall clock is the one that stretches
		// when the scheduler is starved: under a fully loaded test suite the
		// same package ran six times slower than alone, and a 2× margin let
		// the clock preempt the round budget on transfers that were making
		// honest, slow progress. The margin buys tolerance of starvation, not
		// generosity to the pathology — the pathology has no ceiling at all.
		cycle := time.Duration(l.Window)*l.FrameGap + l.AckTimeout
		l.Repair.MaxDuration = 4 * time.Duration(l.MaxRounds) * cycle
	}
	// MaxAirtime is deliberately NOT filled. Zero means disabled, and until a
	// carrier can price a frame honestly that is the only defensible value.
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

	// A negative budget is a mistake, and it is named rather than clamped: a
	// budget silently repaired is a budget nobody notices was wrong.
	case l.Repair.MaxFrames < 0:
		return fmt.Errorf("radiotransfer: a repair budget of %d frames — leave "+
			"it at zero to take the default", l.Repair.MaxFrames)
	case l.Repair.MaxDuration < 0:
		return fmt.Errorf("radiotransfer: a repair budget of %s — leave it at "+
			"zero to take the default", l.Repair.MaxDuration)
	case l.Repair.MaxAirtime < 0:
		return fmt.Errorf("radiotransfer: a repair airtime budget of %s — leave "+
			"it at zero to leave it disabled", l.Repair.MaxAirtime)
	}
	return nil
}

// EventAirtimeBudget is how much transmission ONE event may cost before this
// carrier declines to carry it at all.
//
// Twenty seconds, and the number comes from the measurement rather than from
// taste. On the RU long-fast profile the boards actually run:
//
//	a short message          340 B    1 frame     3.9 s
//	a reaction               387 B    1 frame     3.9 s
//	a long message           857 B    3 frames   11.7 s
//	an image, 2 KiB preview  2.4 KB   6 frames   23.4 s
//	an image, 40 KiB preview 41 KB   99 frames  385.4 s   ← six and a half
//	                                                        minutes, during
//	                                                        which nothing
//	                                                        else moves
//
// THIRTY seconds, and the number was corrected by hardware rather than
// chosen twice.
//
// The intent is the owner's: a picture small enough to cost seconds rather
// than minutes should travel, because that class is not crippled media — it
// is the thing the emoji work will be built on. Twenty-five was picked to
// admit it, from a prediction of 2586 bytes; a real board then derived 2155
// and refused it anyway. The gap was TxGuard, 700 ms per frame that one
// measurement counted and another did not, so the budget had been set against
// air that costs less than the air actually does.
//
// Priced honestly, a 2 KiB preview is six frames and 27.6 seconds. Thirty
// buys six frames with room to spare, which is what the intent needs; the
// eight-KiB class stays out at twenty-one frames and a minute and a half.
//
// It stays a var so a future profile — or a person who decides differently
// for their own segment — can move it without rebuilding the reasoning.
var EventAirtimeBudget = 30 * time.Second

// eventCeiling turns that budget into bytes for THIS carrier.
//
// It asks the carrier what a frame costs and how much of a frame is ours to
// fill, so a faster profile raises the ceiling by itself. A carrier that
// cannot price its own air gets no ceiling at all, which is the honest
// answer: refusing on a guess would be worse than carrying.
func (s *Session) eventCeiling() int {
	model, ok := s.carrier.(AirtimeModel)
	if !ok {
		return 0
	}
	frame := model.FrameAirtime(s.carrier.MTU())
	if frame <= 0 {
		return 0
	}
	// Room in ONE frame, measured the way the sender measures it — header
	// and MAC probed rather than assumed, worst case for every varint. This
	// is the same call carryable() makes; what differs is that carryable
	// multiplies by the fragment limit to answer "how much can a transfer
	// hold", and the question here is "how much fits in the time we allow".
	var wid TransferID
	for i := range wid {
		wid[i] = 0xff
	}
	var digest [DigestLen]byte
	per, err := maxChunk(s.carrier.MTU(), wid, digest, s.lim.MaxMessageBytes,
		StreamControl, s.key)
	if err != nil || per <= 0 {
		return 0
	}
	frames := int(EventAirtimeBudget / frame)
	if frames < 1 {
		frames = 1 // always enough for one, or the carrier carries nothing
	}
	return frames * per
}
