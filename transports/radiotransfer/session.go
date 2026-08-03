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
	framesOut int
	framesIn  int
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
		rx: NewReceiver(lim), waiting: map[TransferID]chan *Frame{}}, nil
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
	defer func() {
		s.mu.Lock()
		delete(s.waiting, o.ID)
		s.mu.Unlock()
	}()

	for {
		for k, i := range o.Pending() {
			if k > 0 {
				// The gap between frames is what makes a window arrive. See
				// Limits.FrameGap: back-to-back frames measured ~9% delivery
				// on the same boards where paced ones measured 96-99%.
				if err := sleep(ctx, s.lim.FrameGap); err != nil {
					return err
				}
			}
			if err := s.sendFrame(ctx, dst, o.Frame(i)); err != nil {
				return err
			}
		}
		done, err := s.awaitCommit(ctx, o, ch)
		if err != nil {
			return err
		}
		if done {
			s.mu.Lock()
			s.completed++
			s.mu.Unlock()
			return nil
		}
		if !o.NextRound() {
			s.mu.Lock()
			s.givenUp++
			s.mu.Unlock()
			return fmt.Errorf("%w: %d of %d fragments still unacknowledged after "+
				"%d rounds", ErrGaveUp, len(o.Pending()), o.Count(), o.Rounds())
		}
	}
}

// awaitCommit waits for what the peer says about this transfer.
//
// It returns on a COMMIT, on a SACK that leaves holes to repair, or when the
// window's patience runs out. A SACK that acknowledges EVERYTHING does not
// end the wait: acknowledged is not delivered, and only a COMMIT says the
// message assembled and its digest matched.
func (s *Session) awaitCommit(ctx context.Context, o *Outbound, ch <-chan *Frame) (bool, error) {
	deadline := time.NewTimer(s.lim.AckTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, nil // nobody answered; the caller repairs
		case f := <-ch:
			switch f.Kind {
			case KindSACK:
				o.NoteSACK(f)
				if !o.Complete() {
					return false, nil // holes reported; repair them now
				}
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
func (s *Session) sendFrame(ctx context.Context, dst RadioAddress, f *Frame) error {
	b, err := f.Encode(s.key)
	if err != nil {
		return err
	}
	// Bounded, so a carrier that refuses forever ends the transfer with an
	// error rather than holding the queue behind it for the life of the
	// process.
	deadline := time.Now().Add(s.lim.AckTimeout * time.Duration(s.lim.MaxRounds))
	for {
		err := s.carrier.Send(ctx, dst, b)
		if err == nil {
			s.mu.Lock()
			s.framesOut++
			s.mu.Unlock()
			return nil
		}
		if !errors.Is(err, ErrCarrierFull) {
			return err
		}
		s.mu.Lock()
		s.refused++
		s.mu.Unlock()
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: the carrier refused every frame for %s",
				ErrCarrierFull, s.lim.AckTimeout*time.Duration(s.lim.MaxRounds))
		}
		if err := sleep(ctx, pace(s.carrier.Credit(), s.lim.SendFloor)); err != nil {
			return err
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
		if b, err := d.Frame.Encode(s.key); err == nil {
			_ = s.carrier.Send(ctx, RadioAddress(d.Peer), b)
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
	// FramesIn counts authentic frames from the segment, and Inbound the
	// transfers part-assembled here right now. Without them a stalled
	// transfer cannot be told from a deaf one — the sender's counters look
	// identical either way, which is the ambiguity this whole layer exists
	// to remove.
	FramesIn    int
	Inbound     int
	InboundHave int
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
	s.mu.Lock()
	defer s.mu.Unlock()
	n, b := s.rx.Inflight()
	return Stats{Attempted: s.attempted, Completed: s.completed,
		GaveUp: s.givenUp, FramesOut: s.framesOut, Refused: s.refused,
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
