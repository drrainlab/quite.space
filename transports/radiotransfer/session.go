// Driving a transfer over a real carrier: send a window, hear what arrived,
// resend the gaps, stop when the receiver says the message assembled.
//
// The one metric that matters here is COMPLETE TRANSFER RATE, not packet
// delivery. Nine days were spent reading a packet number that could not
// distinguish a carrier problem from a reassembly problem, and this layer
// exists precisely because those are different. A run that moves 99% of
// frames and completes 60% of messages is a failure with a good-looking
// number attached.
package radiotransfer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// There is deliberately NO injectable clock here.
//
// The first version had one, and it produced a bug of exactly the kind this
// project keeps writing down. A fake clock's Now() was used to build a
// context deadline — and a context deadline is measured against the REAL
// clock. The fake time sat in the past, every Receive returned instantly,
// and a transfer burned its entire repair budget in a few milliseconds while
// reporting that the peer had stopped answering.
//
// Waiting is what this layer does; faking it hides the thing under test.
// Durations are parameters instead, so a test uses short ones and waits for
// real milliseconds.

// Session moves whole messages over one carrier within one segment.
//
// EXACTLY ONE goroutine reads the carrier, and it is not this one. The first
// version had Send pull frames out of the carrier while waiting for an
// acknowledgement — which works until something else also needs to read, and
// on a radio something else always does: incoming transfers arrive on the
// same receiver as our own acknowledgements. Two consumers of one Receive
// steal each other's frames, and the loss looks exactly like the carrier
// losing them.
//
// So frames come IN through Deliver, from whoever owns the read loop, and
// Send waits on a channel for the ones addressed to its transfer.
type Session struct {
	tracer  Tracer
	carrier RadioDatagram
	key     *TransferKey
	lim     Limits

	mu sync.Mutex
	rx *Receiver
	// waiting routes control frames to the Send that is expecting them. A
	// transfer id nobody is waiting on is somebody else's traffic.
	waiting map[TransferID]chan *Frame

	// stats are what a run reports, and they are counted rather than
	// estimated: an attempt is one call to Send, a completion is one COMMIT
	// heard back.
	attempted int
	completed int
	givenUp   int
	// unconfirmed is an OUTCOME; the four below are REASONS inside it, so a
	// transfer is counted once in the denominator and once in an explanation.
	unconfirmed          int
	frameBudgetExhausted int
	deadlineExpired      int
	airtimeExhausted     int
	// repairFrames and repairBytes accumulate across transfers, so a session
	// can say what retransmission cost it overall.
	repairFrames int
	repairBytes  int
	framesOut    int
	framesIn     int
	refused      int
}

// Options configure a session. Zero values mean the defaults.
type Options struct {
	Limits Limits
	// Trace, when set, receives a structured record of what this session did.
	// Observation only: nothing about the protocol changes when it is set.
	Trace Tracer
}

// NewSession wraps a carrier.
func NewSession(carrier RadioDatagram, key *TransferKey, o Options) (*Session, error) {
	if carrier == nil || key == nil {
		return nil, errors.New("radiotransfer: a session needs a carrier and a key")
	}
	lim := o.Limits.withDefaults()
	if err := lim.check(); err != nil {
		return nil, err
	}
	return &Session{carrier: carrier, key: key, lim: lim, tracer: o.Trace,
		rx: NewReceiver(lim), waiting: map[TransferID]chan *Frame{}}, nil
}

// ErrGaveUp is a transfer that ran out of repair rounds.
//
// It says the peer stopped answering. It does NOT say the message failed to
// arrive, and an earlier version of this comment claimed it did. A trace on
// two real boards showed a transfer reported as given-up whose every fragment
// the peer was holding, byte-exact, while the sender's own statistics said 0%
// delivered — the confirmation had been lost, not the message. So this error
// now travels inside DeliveryUnconfirmedError, and both predicates answer
// true: errors.Is(err, ErrGaveUp) for anyone who cared about the reason, and
// errors.Is(err, ErrDeliveryUnconfirmed) for the honest question.
var ErrGaveUp = errors.New("radiotransfer: the peer stopped answering before " +
	"the message was confirmed")

// ErrDeliveryUnconfirmed is the honest shape of a failure on a radio.
//
// It means exactly this: at least one frame reached the carrier, the sender
// stopped trying, and WHAT THE PEER HOLDS IS UNKNOWN. It may hold nothing, or
// part of the message, or all of it and be unable to say so. A caller must not
// render it as "they did not receive it", and must not render it as delivered.
//
// It is deliberately distinct from a local failure, where nothing was ever
// handed over and the peer genuinely has nothing — see finishSendError.
var ErrDeliveryUnconfirmed = errors.New("radiotransfer: delivery not confirmed")

// The reasons a repair stopped. Separate sentinels because "the peer went
// quiet", "this cost too many frames" and "this took too long" call for
// different answers, and a month from now nobody will remember which happened.
var (
	ErrRepairFrameBudgetExhausted = errors.New(
		"radiotransfer: the repair frame budget is spent")
	ErrRepairDeadlineExhausted = errors.New(
		"radiotransfer: the repair deadline has passed")
	ErrRepairAirtimeBudgetExhausted = errors.New(
		"radiotransfer: the repair airtime budget is spent")
)

// DeliveryUnconfirmedError carries what was spent and why it stopped.
type DeliveryUnconfirmedError struct {
	Transfer     TransferID
	Cause        error
	RepairFrames int
	RepairBytes  int
	Elapsed      time.Duration
	Pending      int
	Of           int
}

func (e *DeliveryUnconfirmedError) Error() string {
	return fmt.Sprintf("radiotransfer: delivery of transfer %s is unconfirmed "+
		"(%d of %d fragments unacknowledged, %d repair frames, %s): %v — the "+
		"peer may already hold the whole message",
		e.Transfer.Short(), e.Pending, e.Of, e.RepairFrames,
		e.Elapsed.Round(time.Millisecond), e.Cause)
}

// Unwrap returns BOTH, so one error answers two different questions:
// errors.Is(err, ErrDeliveryUnconfirmed) asks what may be claimed about the
// peer, and errors.Is(err, ErrRepairDeadlineExhausted) asks what happened here.
func (e *DeliveryUnconfirmedError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrDeliveryUnconfirmed}
	}
	return []error{ErrDeliveryUnconfirmed, e.Cause}
}

// unconfirmed builds the error from what the transfer actually spent.
func unconfirmed(o *Outbound, cause error, now time.Time) error {
	return &DeliveryUnconfirmedError{
		Transfer: o.ID, Cause: cause,
		RepairFrames: o.RepairFrames(), RepairBytes: o.RepairBytes(),
		Elapsed: o.RepairElapsed(now),
		Pending: len(o.Pending()), Of: o.Count(),
	}
}

// finishSendError decides what a failed send may claim about the peer.
//
// It lives in ONE place on purpose. The rule depends on whether anything ever
// reached the carrier, not on the error's identity — a cancelled context or a
// dead serial link means "never attempted" before the first frame and
// "unknown" after it — and a rule applied at each of a dozen early returns is
// a rule that will be missed at the thirteenth.
func finishSendError(o *Outbound, err error, now time.Time) error {
	switch {
	case err == nil:
		return nil
	case errors.As(err, new(*ErrRefusedByPeer)):
		// A refusal is an ANSWER. The peer spoke and declined; nothing is
		// unknown about it, and calling that unconfirmed would throw away the
		// one case where we know exactly what happened.
		return err
	case !o.AnySubmitted():
		// Nothing was ever handed over, so the peer has nothing and this is a
		// local failure with a local explanation.
		return err
	}
	return unconfirmed(o, err, now)
}

// ErrRefusedByPeer is the receiver declining the transfer, with its reason.
type ErrRefusedByPeer struct{ Reason uint64 }

func (e *ErrRefusedByPeer) Error() string {
	switch e.Reason {
	case ReasonTooLarge:
		return "radiotransfer: the peer will not accept a message this large"
	case ReasonBusy:
		return "radiotransfer: the peer has no room for another transfer right now"
	case ReasonDigest:
		return "radiotransfer: the peer assembled something that was not the message sent"
	}
	return fmt.Sprintf("radiotransfer: the peer cancelled the transfer (reason %d)",
		e.Reason)
}

// carryable reports how many bytes ONE transfer can actually move over this
// carrier: the fragment cap times what a fragment holds at this MTU.
//
// Measured with the same probe the sender uses, so the advertised ceiling and
// the enforced one cannot drift apart. It matters because the layer above sizes
// its batches from what we advertise: with MaxMessageBytes (64 KiB) on the
// wire, kernel/sync's messageBudget takes its unmetered branch and batches up
// to 32 KiB — about 240 fragments at a 200-byte MTU, which
// MaxFragmentsPerTransfer refuses outright. The engine was building messages
// this layer could only reject, and the honest ceiling is simply what a
// transfer can hold.
//
// Deliberately NOT capped by airtime as well. Sixty-four fragments is minutes
// of air, but refusing a message for being slow would make a legitimate large
// one unsendable rather than merely patient — how OFTEN we spend the air is the
// pump's business (node/lan.go's linkCadence), not the message's.
func (s *Session) carryable() int {
	var id TransferID
	for i := range id {
		id[i] = 0xff // worst case for every varint in the header
	}
	var digest [DigestLen]byte
	chunk, err := maxChunk(s.carrier.MTU(), id, digest, s.lim.MaxMessageBytes,
		StreamControl, s.key)
	if err != nil || chunk <= 0 {
		return s.lim.MaxMessageBytes
	}
	return chunk * s.lim.MaxFragmentsPerTransfer
}

// Send delivers one whole message, and returns only when the receiver has
// said it assembled — or when the rounds run out.
//
// dst may be nil for a broadcast. The DATA frames go to dst; the receiver's
// SACK comes back addressed, which is what keeps a group from answering all
// at once.
func (s *Session) Send(ctx context.Context, dst RadioAddress, msg []byte) error {
	return s.SendOn(ctx, StreamSync, dst, msg)
}

// SendOn delivers a message on a named stream. Control traffic — a node
// announcing itself, offering an invite — travels the same radio as sync and
// must not be handed to the sync engine's parser.
func (s *Session) SendOn(ctx context.Context, stream uint64, dst RadioAddress,
	msg []byte) error {
	o, err := NewOutboundOn(stream, msg, s.carrier.MTU(), s.lim, s.key)
	if err != nil {
		return err
	}
	ch := make(chan *Frame, 2*s.lim.Window)
	s.mu.Lock()
	s.waiting[o.ID] = ch
	s.attempted++
	s.mu.Unlock()
	s.trace(TraceEvent{Transfer: o.ID, Event: TraceTransferCreated,
		Fragment: -1, Count: o.Count(), Reason: fmt.Sprintf("%d bytes", len(msg))})
	defer func() {
		s.mu.Lock()
		delete(s.waiting, o.ID)
		s.mu.Unlock()
	}()

	for {
		pending := o.Pending()
		if o.Rounds() == 0 {
			s.trace(TraceEvent{Transfer: o.ID, Event: TraceWindowStarted,
				Fragment: -1, Count: o.Count(), Round: o.Rounds() + 1,
				Pending: pending})
		} else {
			// The field the standard of proof rests on: exactly which
			// fragments a repair round chose, so an excess DATA frame can be
			// traced to the state that asked for it.
			s.trace(TraceEvent{Transfer: o.ID, Event: TraceRetransmit,
				Fragment: -1, Count: o.Count(), Round: o.Rounds() + 1,
				Pending: pending, Reason: "unacknowledged after the last round"})
		}
		for k, i := range pending {
			if k > 0 {
				// The gap between frames is what makes a window arrive. See
				// Limits.FrameGap: back-to-back frames measured ~9% delivery
				// on the same boards where paced ones measured 96-99%.
				if err := sleep(ctx, s.lim.FrameGap); err != nil {
					return finishSendError(o, err, time.Now())
				}
			}
			frame := o.Frame(i)
			// A fragment the carrier already took: offering it again is a
			// RETRANSMISSION, and that is the first of the two ways a repair
			// phase begins.
			isRepair := o.WasSubmitted(i)
			if isRepair {
				now := time.Now()
				o.BeginRepair(now)
				// Checked BEFORE the frame is offered, so the budget is a
				// ceiling rather than something noticed one frame late.
				if err := o.OverBudget(now); err != nil {
					return s.stopUnconfirmed(o, err, now)
				}
			}
			n, err := s.sendFrame(ctx, dst, frame)
			if err != nil {
				s.trace(TraceEvent{Transfer: o.ID, Event: TraceDataTXFailed,
					Fragment: i, Round: o.Rounds() + 1, Reason: err.Error()})
				return finishSendError(o, err, time.Now())
			}
			if isRepair {
				// Only now, because a frame the carrier REFUSED cost nothing
				// and charging it would let a busy radio spend the budget that
				// exists to bound a talkative one.
				o.ChargeRepairFrame(n)
				if m, ok := s.carrier.(AirtimeModel); ok {
					o.ChargeRepairAirtime(m.FrameAirtime(n))
				}
				s.mu.Lock()
				s.repairFrames++
				s.repairBytes += n
				s.mu.Unlock()
			}
			o.MarkSubmitted(i)
			s.trace(TraceEvent{Transfer: o.ID, Event: TraceDataTX,
				Fragment: i, Count: o.Count(), Round: o.Rounds() + 1})
		}

		// The second way a repair phase begins, and it must be stamped HERE —
		// before the wait, not after a failed one. Every fragment is now with
		// the carrier and nothing has confirmed the message; from this moment
		// the transfer is repairing even though there may be nothing left to
		// resend. Starting the clock after awaitCommit returned would leave
		// the whole first AckTimeout outside the deadline, in precisely the
		// case the deadline exists for: every fragment arrived, only the
		// confirmation was lost.
		if o.AllSubmitted() {
			o.BeginRepair(time.Now())
		}

		done, err := s.awaitCommit(ctx, o, ch)
		if err != nil {
			return finishSendError(o, err, time.Now())
		}
		if done {
			s.mu.Lock()
			s.completed++
			s.mu.Unlock()
			s.trace(TraceEvent{Transfer: o.ID, Event: TraceCompleted,
				Fragment: -1, Count: o.Count(), Round: o.Rounds() + 1})
			return nil
		}
		// An absolute limit reached while waiting ends the transfer here
		// rather than after one more window.
		if now := time.Now(); o.OverBudget(now) != nil {
			return s.stopUnconfirmed(o, o.OverBudget(now), now)
		}
		if !o.NextRound() {
			s.trace(TraceEvent{Transfer: o.ID, Event: TraceGaveUp, Fragment: -1,
				Count: o.Count(), Round: o.Rounds(), Pending: o.Pending(),
				Reason: "rounds exhausted"})
			// ErrGaveUp travels as the CAUSE, not as the whole answer: the
			// peer going quiet says nothing about what it already holds.
			return s.stopUnconfirmed(o, ErrGaveUp, time.Now())
		}
	}
}

// stopUnconfirmed ends a transfer that spent its budget, counts it once, and
// says which limit stopped it.
func (s *Session) stopUnconfirmed(o *Outbound, cause error, now time.Time) error {
	s.mu.Lock()
	s.unconfirmed++
	switch {
	case errors.Is(cause, ErrGaveUp):
		s.givenUp++
	case errors.Is(cause, ErrRepairFrameBudgetExhausted):
		s.frameBudgetExhausted++
	case errors.Is(cause, ErrRepairDeadlineExhausted):
		s.deadlineExpired++
	case errors.Is(cause, ErrRepairAirtimeBudgetExhausted):
		s.airtimeExhausted++
	}
	s.mu.Unlock()

	if !errors.Is(cause, ErrGaveUp) {
		// The give-up path already traced itself; the budget paths would
		// otherwise stop with no line saying why.
		s.trace(TraceEvent{Transfer: o.ID, Event: TraceBudgetExhausted,
			Fragment: -1, Count: o.Count(), Round: o.Rounds(),
			Pending: o.Pending(), Reason: cause.Error()})
	}
	return unconfirmed(o, cause, now)
}

// awaitCommit waits for what the peer says about this transfer.
//
// It returns on a COMMIT, on a SACK that leaves holes to repair, or when the
// window's patience runs out. A SACK that acknowledges EVERYTHING does not
// end the wait: acknowledged is not delivered, and only a COMMIT says the
// message assembled and its digest matched.
func (s *Session) awaitCommit(ctx context.Context, o *Outbound, ch <-chan *Frame) (bool, error) {
	// Patience starts at TX DRAIN, not at enqueue, on a carrier that can
	// tell the difference. AckTimeout is "how long may the peer take to
	// answer" — and the peer cannot even have HEARD the window while our own
	// modem is still radiating it. Counting that tail against the peer is
	// how a transfer times out on itself.
	wait := s.lim.AckTimeout
	if m, ok := s.carrier.(AirtimeModel); ok {
		if drain := time.Until(m.EstimatedTxEnd()); drain > 0 {
			wait += drain
		}
	}
	if end := o.RepairDeadline(); !end.IsZero() {
		if left := time.Until(end); left < wait {
			wait = left
		}
		if wait <= 0 {
			return false, nil // the loop head reports the exhausted budget
		}
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			s.trace(TraceEvent{Transfer: o.ID, Event: TraceAckTimeout,
				Fragment: -1, Round: o.Rounds() + 1,
				Reason: "no SACK and no COMMIT within AckTimeout"})
			return false, nil // nobody answered; the caller repairs
		case f := <-ch:
			switch f.Kind {
			case KindSACK:
				txt, have, missing := bitmapText(f.Bitmap, int(f.Count)-int(f.Base))
				o.NoteSACK(f)
				complete := o.Complete()
				reason := "holes reported"
				if complete {
					// A SACK saying the peer holds everything still does not
					// finish the transfer — only a COMMIT does. Traced
					// explicitly, because if this line is followed by an
					// ack_timeout it names the defect on its own.
					reason = "peer holds every fragment; still waiting for COMMIT"
				}
				s.trace(TraceEvent{Transfer: o.ID, Event: TraceSACKRX,
					Fragment: -1, Round: o.Rounds() + 1, Base: int(f.Base),
					Count: int(f.Count), Bitmap: txt, Have: have,
					Missing: missing, Reason: reason})
				if !complete {
					return false, nil // holes reported; repair them now
				}
			case KindCommit:
				s.trace(TraceEvent{Transfer: o.ID, Event: TraceCommitRX,
					Fragment: -1, Round: o.Rounds() + 1})
				if f.Digest != o.digest {
					return false, fmt.Errorf("radiotransfer: the peer committed a "+
						"different message under transfer %s", o.ID.Short())
				}
				return true, nil
			case KindCancel:
				s.trace(TraceEvent{Transfer: o.ID, Event: TraceCancelRX,
					Fragment: -1, Reason: fmt.Sprintf("code %d", f.Reason)})
				return false, &ErrRefusedByPeer{Reason: f.Reason}
			}
		}
	}
}

// sendFrame offers one frame, waiting out a carrier that is full.
//
// IT ALWAYS ATTEMPTS THE SEND. Credit is used only to decide how long to wait
// after a REFUSAL — which is the contract this layer states in datagram.go
// and, in the first version, did not keep: it gated the send on Credit and
// never offered a frame the carrier claimed no room for.
//
// That wedged on real hardware. Meshtastic's credit is the firmware's last
// queue report minus what we have handed over since, so it decays with every
// send and only recovers when the firmware reports again. When those reports
// stopped, credit sat at zero, no frame was ever offered, nothing was refused
// — and a transfer showed 45 frames out, 0 completed, 0 given up, frozen for
// minutes with no error anywhere. A stall with a clean-looking counter is
// exactly the failure this project keeps paying for.
//
// A carrier that genuinely has no room says so by REFUSING, and a refusal is
// counted, paced and retried. Deciding not to ask is how you never find out.
// It returns the ENCODED SIZE, because it is the only place that knows it:
// Frame has no size of its own and a chunk's length is not a frame's length.
// A caller charging a budget must not have to guess.
func (s *Session) sendFrame(ctx context.Context, dst RadioAddress, f *Frame) (int, error) {
	b, err := f.Encode(s.key)
	if err != nil {
		return 0, err
	}
	// Bounded, so a carrier that refuses forever ends the transfer with an
	// error rather than holding the queue behind it for the life of the
	// process.
	deadline := time.Now().Add(s.lim.AckTimeout * time.Duration(s.lim.MaxRounds))
	for {
		// A carrier that can tell time is not allowed to be buried. Waiting
		// HERE — before the offer — is what keeps the queue's length bounded
		// and therefore keeps feedback current; the measured alternative was
		// a ~30-second queue and SACKs that arrived as history.
		if m, ok := s.carrier.(AirtimeModel); ok {
			for {
				backlog := time.Until(m.EstimatedTxEnd())
				if backlog <= s.lim.MaxQueuedAirtime {
					break
				}
				if err := sleep(ctx, min(backlog-s.lim.MaxQueuedAirtime,
					s.lim.SendFloor)); err != nil {
					return 0, err
				}
			}
		}
		err := s.carrier.Send(ctx, dst, b)
		if err == nil {
			s.mu.Lock()
			s.framesOut++
			s.mu.Unlock()
			return len(b), nil
		}
		if !errors.Is(err, ErrCarrierFull) {
			return 0, err
		}
		s.mu.Lock()
		s.refused++
		s.mu.Unlock()
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("%w: the carrier refused every frame for %s",
				ErrCarrierFull, s.lim.AckTimeout*time.Duration(s.lim.MaxRounds))
		}
		if err := sleep(ctx, pace(s.carrier.Credit(), s.lim.SendFloor)); err != nil {
			return 0, err
		}
	}
}

// Deliver folds one frame in, from whoever owns the carrier's read loop.
//
// It returns a message when an incoming transfer completes. Frames that are
// not ours, or not authentic, are ignored without complaint — on a shared
// channel most traffic belongs to somebody else, and saying so about each
// one would fill a log with the neighbourhood.
func (s *Session) Deliver(ctx context.Context, src RadioAddress, raw []byte) (*Delivered, error) {
	f, err := Decode(raw, s.key)
	if err != nil {
		return nil, nil
	}
	s.mu.Lock()
	s.framesIn++
	s.mu.Unlock()
	// A control frame for a transfer WE are sending goes to that Send.
	if f.Kind != KindData {
		s.mu.Lock()
		ch, mine := s.waiting[f.Transfer]
		s.mu.Unlock()
		if mine {
			select {
			case ch <- f:
			default: // the sender is not keeping up; the window will repeat
			}
			return nil, nil
		}
	}
	switch f.Kind {
	case KindData:
		s.mu.Lock()
		got, reply, err := s.rx.Accept(string(src), f, time.Now())
		s.mu.Unlock()
		ev := TraceDataRX
		if errors.Is(err, ErrAlreadyDelivered) {
			ev = TraceDataRXDuplicate
		}
		s.trace(TraceEvent{Transfer: f.Transfer, Event: ev,
			Fragment: int(f.Index), Count: int(f.Count)})
		if reply != nil {
			b, e := reply.Encode(s.key)
			var sendErr error
			if e == nil {
				sendErr = s.carrier.Send(ctx, src, b)
			}
			if reply.Kind == KindCommit {
				// Traced with its outcome because this frame is sent ONCE and
				// is never repeated by anything: if it is lost, the peer holds
				// the whole message and the sender cannot know it.
				reason := "sent once, never repeated"
				if e != nil {
					reason = "encode failed: " + e.Error()
				} else if sendErr != nil {
					reason = "carrier refused: " + sendErr.Error()
				}
				s.trace(TraceEvent{Transfer: f.Transfer, Event: TraceCommitTX,
					Fragment: -1, Count: int(f.Count), Reason: reason})
			}
		}
		if errors.Is(err, ErrAlreadyDelivered) {
			return nil, nil
		}
		return got, err
	case KindSACK:
		// Somebody else asking for the same window. Ours is suppressed, so a
		// group does not answer in chorus.
		s.mu.Lock()
		s.rx.NoteOverheard(f)
		s.mu.Unlock()
	case KindCancel:
		s.mu.Lock()
		s.rx.Cancel(f.Transfer)
		s.mu.Unlock()
	}
	return nil, nil
}

// PumpSACKs sends any SACK whose delay has elapsed. A caller drives this on
// its own cadence; nothing here starts a goroutine on somebody's behalf.
// PumpSACKs sends whatever SACKs have come due, each ADDRESSED to the peer
// whose transfer it answers rather than to whoever last spoke on the segment.
func (s *Session) PumpSACKs(ctx context.Context) {
	s.mu.Lock()
	due := s.rx.DueSACKs(time.Now())
	s.mu.Unlock()
	for _, d := range due {
		txt, have, missing := bitmapText(d.Frame.Bitmap,
			int(d.Frame.Count)-int(d.Frame.Base))
		b, err := d.Frame.Encode(s.key)
		reason := "due"
		if err == nil {
			if e := s.carrier.Send(ctx, RadioAddress(d.Peer), b); e != nil {
				reason = "carrier refused: " + e.Error()
			}
		} else {
			reason = "encode failed: " + err.Error()
		}
		s.trace(TraceEvent{Transfer: d.Frame.Transfer, Event: TraceSACKTX,
			Fragment: -1, Base: int(d.Frame.Base), Count: int(d.Frame.Count),
			Bitmap: txt, Have: have, Missing: missing, Reason: reason})
	}
}

// Stats is what a session did, in the units that answer the question this
// layer was built for.
// Stats separates OUTCOMES from REASONS, because conflating them is how a
// single transfer ends up counted twice in a denominator and nobody can say
// afterwards whether a segment was futile, expensive or slow.
type Stats struct {
	Attempted int
	// Completed is CONFIRMED completion — the sender heard a COMMIT. It is a
	// lower bound on delivery, not a measure of it: on real boards this
	// reported 0% and 80% in runs where 100% of the messages had in fact
	// arrived byte-exact, because the confirmations were what got lost.
	Completed int
	// Unconfirmed is an outcome: the sender stopped and the peer's state is
	// unknown. Every transfer lands in exactly one outcome.
	Unconfirmed int

	// The reasons a transfer became Unconfirmed. These SUM INTO Unconfirmed;
	// they are not outcomes of their own.
	GaveUp               int
	FrameBudgetExhausted int
	DeadlineExpired      int
	AirtimeExhausted     int

	// RepairDataFrames and RepairDataBytes are what retransmission cost. The
	// first transmission is not counted, and neither is control traffic.
	RepairDataFrames int
	RepairDataBytes  int

	FramesOut int
	Refused   int
	// FramesIn counts authentic frames from the segment, and Inbound the
	// transfers part-assembled here right now. Without them a stalled
	// transfer cannot be told from a deaf one — the sender's counters look
	// identical either way, which is the ambiguity this whole layer exists
	// to remove.
	FramesIn    int
	Inbound     int
	InboundHave int
}

// CompleteTransferRate is the headline, and what it measures is CONFIRMED
// completion. Packet delivery is a property of the carrier; this is a property
// of the system, and it is the number a decision should rest on — as a LOWER
// BOUND. A confirmation lost on the way back reads here as a failed transfer,
// which is the safe direction to be wrong in and still a direction.
//
// Refusals are excluded on purpose: the peer answered and declined, so the
// transport worked. Counting that as a delivery failure would show a healthy
// segment as a broken one.
//
// (The honest name is ConfirmedTransferRate. Renaming is a follow-up rather
// than something smuggled into a safety fix.)
func (st Stats) CompleteTransferRate() float64 {
	if st.Attempted == 0 {
		return 0
	}
	return float64(st.Completed) / float64(st.Attempted)
}

func (s *Session) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, b := s.rx.Inflight()
	return Stats{Attempted: s.attempted, Completed: s.completed,
		Unconfirmed:          s.unconfirmed,
		GaveUp:               s.givenUp,
		FrameBudgetExhausted: s.frameBudgetExhausted,
		DeadlineExpired:      s.deadlineExpired,
		AirtimeExhausted:     s.airtimeExhausted,
		RepairDataFrames:     s.repairFrames, RepairDataBytes: s.repairBytes,
		FramesOut: s.framesOut, Refused: s.refused,
		FramesIn: s.framesIn, Inbound: n, InboundHave: b}
}

// sleep waits, or gives up when the caller does.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
