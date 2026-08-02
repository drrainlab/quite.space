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
type Session struct {
	carrier RadioDatagram
	key     *TransferKey
	lim     Limits
	rx      *Receiver

	// stats are what a run reports, and they are counted rather than
	// estimated: an attempt is one call to Send, a completion is one COMMIT
	// heard back.
	attempted int
	completed int
	givenUp   int
	framesOut int
	refused   int
}

// Options configure a session. Zero values mean the defaults.
type Options struct {
	Limits Limits
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
	return &Session{carrier: carrier, key: key, lim: lim,
		rx: NewReceiver(lim)}, nil
}

// ErrGaveUp is a transfer that ran out of repair rounds.
//
// It is a real outcome, not a timeout to be swallowed: the peer is not
// answering, and the message did NOT arrive. Reporting it as anything softer
// is how "handed to the transport" came to be mistaken for delivery.
var ErrGaveUp = errors.New("radiotransfer: the peer stopped answering before " +
	"the message was complete")

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

// Send delivers one whole message, and returns only when the receiver has
// said it assembled — or when the rounds run out.
//
// dst may be nil for a broadcast. The DATA frames go to dst; the receiver's
// SACK comes back addressed, which is what keeps a group from answering all
// at once.
func (s *Session) Send(ctx context.Context, dst RadioAddress, msg []byte) error {
	o, err := NewOutbound(msg, s.carrier.MTU(), s.lim, s.key)
	if err != nil {
		return err
	}
	s.attempted++

	for {
		pending := o.Pending()
		if len(pending) == 0 && o.Complete() {
			// Everything acknowledged, but acknowledged is not delivered:
			// only a COMMIT says the message assembled and its digest
			// matched. Wait for it rather than claiming success from a SACK.
			done, err := s.awaitCommit(ctx, o)
			if err != nil {
				return err
			}
			if done {
				s.completed++
				return nil
			}
		}
		for _, i := range pending {
			if err := s.sendFrame(ctx, dst, o.Frame(i)); err != nil {
				return err
			}
		}
		done, err := s.awaitCommit(ctx, o)
		if err != nil {
			return err
		}
		if done {
			s.completed++
			return nil
		}
		if !o.NextRound() {
			s.givenUp++
			return fmt.Errorf("%w: %d of %d fragments still unacknowledged after "+
				"%d rounds", ErrGaveUp, len(o.Pending()), o.Count(), o.Rounds())
		}
	}
}

// awaitCommit listens until the transfer commits, the peer cancels, or the
// window's ack timeout expires.
func (s *Session) awaitCommit(ctx context.Context, o *Outbound) (bool, error) {
	deadline := time.Now().Add(s.lim.AckTimeout)
	for time.Now().Before(deadline) {
		rctx, cancel := context.WithDeadline(ctx, deadline)
		_, raw, err := s.carrier.Receive(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil // the window timed out; the caller repairs
		}
		f, err := Decode(raw, s.key)
		if err != nil {
			continue // not ours, or not authentic: neither is our business
		}
		if f.Transfer != o.ID {
			continue
		}
		switch f.Kind {
		case KindSACK:
			o.NoteSACK(f)
			if o.Complete() {
				// Every fragment is acknowledged. Keep listening for the
				// COMMIT rather than returning: an acknowledged fragment is
				// not an assembled message.
				continue
			}
			return false, nil // holes reported; repair them now
		case KindCommit:
			if f.Digest != o.digest {
				return false, fmt.Errorf("radiotransfer: the peer committed a "+
					"different message under transfer %s", o.ID.Short())
			}
			return true, nil
		case KindCancel:
			return false, &ErrRefusedByPeer{Reason: f.Reason}
		}
	}
	return false, nil
}

// sendFrame offers one frame, waiting out a carrier that is full.
//
// Credit is consulted as a HINT and the result of Send is what decides. A
// carrier can fill between the two — another goroutine, the firmware's own
// traffic, a neighbour's rebroadcast — so treating Credit as a reservation
// would be trusting a snapshot of somebody else's queue.
func (s *Session) sendFrame(ctx context.Context, dst RadioAddress, f *Frame) error {
	b, err := f.Encode(s.key)
	if err != nil {
		return err
	}
	for {
		c := s.carrier.Credit()
		if c.Allows(1) {
			err := s.carrier.Send(ctx, dst, b)
			if err == nil {
				s.framesOut++
				return nil
			}
			if !errors.Is(err, ErrCarrierFull) {
				return err
			}
			s.refused++
		}
		if err := sleep(ctx, pace(c, s.lim.SendFloor)); err != nil {
			return err
		}
	}
}

// Deliver folds one received frame in and returns a message when one
// completes. Frames that are not ours, or not authentic, are ignored — on a
// shared channel most traffic belongs to somebody else.
func (s *Session) Deliver(ctx context.Context, src RadioAddress, raw []byte) (*Delivered, error) {
	f, err := Decode(raw, s.key)
	if err != nil {
		return nil, nil
	}
	now := time.Now()
	peer := string(src)
	switch f.Kind {
	case KindData:
		got, reply, err := s.rx.Accept(peer, f, now)
		if reply != nil {
			if b, e := reply.Encode(s.key); e == nil {
				_ = s.carrier.Send(ctx, src, b)
			}
		}
		if errors.Is(err, ErrAlreadyDelivered) {
			return nil, nil
		}
		return got, err
	case KindSACK:
		// Somebody else asking for the same window. Ours is suppressed, so a
		// group does not answer in chorus.
		s.rx.NoteOverheard(f)
	case KindCancel:
		s.rx.Cancel(f.Transfer)
	}
	return nil, nil
}

// PumpSACKs sends any SACK whose delay has elapsed. A caller drives this on
// its own cadence; nothing here starts a goroutine on somebody's behalf.
func (s *Session) PumpSACKs(ctx context.Context, dst RadioAddress) {
	for _, f := range s.rx.DueSACKs(time.Now()) {
		if b, err := f.Encode(s.key); err == nil {
			_ = s.carrier.Send(ctx, dst, b)
		}
	}
}

// Stats is what a session did, in the units that answer the question this
// layer was built for.
type Stats struct {
	Attempted int
	Completed int
	GaveUp    int
	FramesOut int
	Refused   int
}

// CompleteTransferRate is the headline. Packet delivery is a property of the
// carrier; this is a property of the system, and it is the number a decision
// should rest on.
func (st Stats) CompleteTransferRate() float64 {
	if st.Attempted == 0 {
		return 0
	}
	return float64(st.Completed) / float64(st.Attempted)
}

func (s *Session) Stats() Stats {
	return Stats{Attempted: s.attempted, Completed: s.completed,
		GaveUp: s.givenUp, FramesOut: s.framesOut, Refused: s.refused}
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
