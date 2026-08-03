// Presenting the transfer layer as an ordinary transport, so nothing above it
// has to know it is there.
//
// The sync engine hands an Endpoint a message and expects the call to return
// promptly; a Radio Transfer takes tens of seconds and answers questions
// while it works. Those cannot be the same call, so this wraps one in the
// other: Send queues, a worker performs transfers one at a time, and a reader
// goroutine owns the carrier and routes every frame that arrives.
//
// It layers the way transports/compact already layers (compact.Wrap over an
// Endpoint), which is the shape node/mesh.go is already built to adopt.
//
// ONE THING IT DELIBERATELY DOES NOT COPY FROM compact: compact advertises
// MaxPayload = 2048 upward, which quietly defeats the one-packet-per-message
// rule kernel/sync was given after the airtime measurements. This advertises
// what a whole transfer can carry, and the engine's own loss-aware batching
// keeps working — but the fragmentation below is ours, sized to the carrier's
// real MTU, and it is repaired when a piece goes missing.
package radiotransfer

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/transports"
)

// ctrlMsg is a control message with its own patience.
//
// A control message is something a person is standing there waiting for, and
// the sync budget is the wrong clock for it: MaxRounds × AckTimeout is 270
// seconds by default, which is longer than the ten minutes a quicklink lives
// divided among the legs it needs. A one-frame PROBE that inherits that budget
// takes four and a half minutes to discover a radio nobody is listening to —
// which is exactly the discovery it exists to make cheap.
type ctrlMsg struct {
	msg    []byte
	within time.Duration // 0 = the session's own budget
}

// Endpoint is a transports.Endpoint backed by Radio Transfer.
type Endpoint struct {
	s   *Session
	dst RadioAddress

	out       chan []byte
	ctrl      chan ctrlMsg
	onControl func(RadioAddress, []byte)
	stop      chan struct{}
	closed    sync.Once

	// curCancel cancels the SYNC transfer in flight, so a control message can
	// take the air instead of queueing behind it. Only sync is ever preempted:
	// yielding a summary costs nothing, because the next one supersedes it.
	curMu     sync.Mutex
	curCancel context.CancelFunc
	preempted bool

	mu    sync.Mutex
	inbox [][]byte
	// dropped counts messages the outbound queue could not take. It is a
	// counter rather than a silent discard because "the queue was full" and
	// "the radio lost it" are different problems with different fixes, and
	// only one of them is the carrier's.
	dropped int
	failed  int
	// yielded counts sync transfers abandoned so a control message could take
	// the air. Neither a failure nor a delivery, and kept apart from both.
	yielded int
}

// EndpointOptions configure the wrapper.
type EndpointOptions struct {
	Options
	// Dst is where transfers are addressed. Nil means broadcast, which is
	// what a segment normally wants.
	Dst RadioAddress
	// Queue bounds messages waiting to be sent. On a carrier that moves one
	// message a minute, an unbounded queue is a memory leak with a schedule.
	Queue int
	// Log, when set, is told about transfers that fail. Nothing here is
	// silent about a message that did not arrive.
	Log *log.Logger
	// OnControl receives messages sent on the control stream, WITH the peer
	// they came from. Nil means this node has no use for them, and they are
	// dropped rather than queued — holding traffic nobody reads is a leak with
	// a schedule.
	//
	// src is the carrier's name for a radio, never for a person: it is what
	// makes an addressed answer possible, and nothing more. Who is holding
	// that radio is a question only a signature answers.
	OnControl func(src RadioAddress, msg []byte)
}

// Wrap turns a carrier into an Endpoint that delivers whole messages.
func Wrap(carrier RadioDatagram, key *TransferKey, o EndpointOptions) (*Endpoint, error) {
	s, err := NewSession(carrier, key, o.Options)
	if err != nil {
		return nil, err
	}
	if o.Queue <= 0 {
		o.Queue = 16
	}
	e := &Endpoint{s: s, dst: o.Dst, out: make(chan []byte, o.Queue),
		ctrl: make(chan ctrlMsg, o.Queue), onControl: o.OnControl,
		stop: make(chan struct{})}
	go e.readLoop()
	go e.sendLoop(o.Log)
	return e, nil
}

// Send queues a message for transfer.
//
// It returns as soon as the message is queued, NOT when it has arrived —
// which is the honest shape for this interface: transports.Endpoint has no
// vocabulary for "delivered", and inventing one here would be the same
// mistake as reporting handed-to-transport as delivery.
func (e *Endpoint) Send(msg []byte) error {
	select {
	case <-e.stop:
		return errors.New("radiotransfer: the endpoint is closed")
	default:
	}
	b := append([]byte(nil), msg...)
	select {
	case e.out <- b:
		return nil
	default:
	}
	// The queue is full, so something has to go — and it must be the OLDEST,
	// not this one.
	//
	// What travels here is the sync engine's summaries, and a summary is a
	// SNAPSHOT: a newer one describes everything an older one did and more.
	// Refusing the newest while holding a queue of stale ones is how a link
	// that is merely slow becomes a link that never converges — on a carrier
	// moving one message every few seconds, the queue is always full, so the
	// message that would have finished the job is always the one refused.
	select {
	case <-e.out:
		e.mu.Lock()
		e.dropped++
		e.mu.Unlock()
	default:
	}
	select {
	case e.out <- b:
		return nil
	default:
		e.mu.Lock()
		e.dropped++
		e.mu.Unlock()
		return ErrCarrierFull
	}
}

// SendControl queues a message on the control stream.
//
// Separate from Send so a node's own traffic about the segment never reaches
// the sync engine's parser, and so a busy sync queue cannot starve an invite
// — the two have their own queues and the worker alternates.
func (e *Endpoint) SendControl(msg []byte) error {
	return e.SendControlWithin(msg, 0)
}

// SendControlWithin queues a control message that must succeed or fail within
// a stated time.
//
// Zero means the session's own budget — right for an invitation, which is
// worth several minutes of trying. Anything a person is watching for an answer
// to should name its own patience: a PROBE asking "are you listening?" is one
// frame, and letting it inherit MaxRounds × AckTimeout would make the cheap
// question cost as much as the expensive one it exists to avoid.
func (e *Endpoint) SendControlWithin(msg []byte, within time.Duration) error {
	select {
	case <-e.stop:
		return errors.New("radiotransfer: the endpoint is closed")
	default:
	}
	select {
	case e.ctrl <- ctrlMsg{msg: append([]byte(nil), msg...), within: within}:
		// A sync transfer already on the air would otherwise hold this behind
		// it for up to MaxRounds × AckTimeout — 270 seconds by default. That is
		// how an invitation reached nobody while the counters read "still
		// going": not lost, not refused, merely queued behind four and a half
		// minutes of background traffic.
		e.preempt()
		return nil
	default:
		return ErrCarrierFull
	}
}

// preempt cancels the SYNC transfer in flight, if there is one.
//
// Only sync. A summary is a SNAPSHOT — abandoning one costs nothing because
// the next one describes everything it did and more — while abandoning a
// control transfer would throw away the very thing somebody is waiting for.
func (e *Endpoint) preempt() {
	e.curMu.Lock()
	defer e.curMu.Unlock()
	if e.curCancel != nil {
		e.preempted = true
		e.curCancel()
	}
}

func (e *Endpoint) armPreempt(c context.CancelFunc) {
	e.curMu.Lock()
	e.curCancel, e.preempted = c, false
	e.curMu.Unlock()
}

// disarmPreempt clears the hook and reports whether it fired, so a transfer
// this endpoint deliberately abandoned is never counted or logged as one the
// radio failed to deliver.
func (e *Endpoint) disarmPreempt() bool {
	e.curMu.Lock()
	defer e.curMu.Unlock()
	e.curCancel = nil
	was := e.preempted
	e.preempted = false
	return was
}

// Poll returns whole messages that have arrived.
func (e *Endpoint) Poll() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.inbox
	e.inbox = nil
	return out
}

// Capabilities describes what this link is, honestly.
func (e *Endpoint) Capabilities() transports.Capabilities {
	return transports.Capabilities{
		// A whole message, not a frame: fragmentation below is ours and is
		// repaired, so the layer above may hand over more than one packet
		// holds. What it must NOT do is hand over more than a transfer can
		// carry, because that is refused before any airtime is spent — which
		// is precisely what advertising MaxMessageBytes did. See carryable().
		MaxPayload: min(e.s.lim.MaxMessageBytes, e.s.carryable()),
		Realtime:   false,
		// AckNone stands. A COMMIT proves the message ASSEMBLED at a peer —
		// which is more than any carrier here could say before — but this
		// interface's Ack vocabulary is about the delivery ladder (ADR-007),
		// and nothing at this layer is entitled to raise a rung on it.
		Ack: transports.AckNone,
	}
}

// Credit passes the carrier's own answer upward, so the engine's backpressure
// keeps working against the thing that actually fills.
func (e *Endpoint) Credit() transports.Credit {
	if len(e.out) >= cap(e.out) {
		return transports.Credit{Packets: 0, Known: true, RetryAfter: time.Second}
	}
	return e.s.carrier.Credit()
}

// Close stops the workers. Idempotent: a link torn down twice is a normal
// shutdown path, not an error.
func (e *Endpoint) Close() error {
	e.closed.Do(func() { close(e.stop) })
	return nil
}

// Stats reports what the transfer layer did, in whole messages.
func (e *Endpoint) Stats() Stats { return e.s.Stats() }

// Queued reports messages waiting and messages refused for want of room.
func (e *Endpoint) Queued() (waiting, dropped, failed int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.out), e.dropped, e.failed
}

// Yielded reports how many sync transfers stepped aside for control traffic.
func (e *Endpoint) Yielded() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.yielded
}

// readLoop is the ONE reader of the carrier. Everything that arrives goes
// through Deliver, which routes it to whichever Send is waiting or into the
// reassembler.
func (e *Endpoint) readLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-e.stop; cancel() }()

	for {
		rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
		src, raw, err := e.s.carrier.Receive(rctx)
		rcancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if got, _ := e.s.Deliver(ctx, src, raw); got != nil {
				switch got.Stream {
				case StreamControl:
					if e.onControl != nil {
						e.onControl(got.From, got.Message)
					}
				default:
					e.mu.Lock()
					e.inbox = append(e.inbox, got.Message)
					e.mu.Unlock()
				}
			}
		}
		// SACKs are pushed from here rather than from a timer of their own:
		// this loop already runs whenever anything happens on the segment,
		// and a receiver with nothing to acknowledge has nothing to do.
		e.s.PumpSACKs(ctx)
	}
}

// sendLoop performs one transfer at a time.
//
// Serial on purpose. Two transfers interleaved on one radio double the time
// each spends exposed to loss without moving either any faster — the carrier
// is the bottleneck, not this queue, and airtime spent on a second transfer
// is airtime the first is not using.
func (e *Endpoint) sendLoop(lg *log.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-e.stop; cancel() }()

	for {
		var msg []byte
		stream := StreamSync
		var within time.Duration
		select {
		case <-e.stop:
			return
		// Control first when both are waiting: an invite is a few hundred
		// bytes and somebody is standing there watching for it, while sync
		// traffic is a background that has all day.
		//
		// Preference at SELECTION time is not enough on its own, which is what
		// preempt() is for — by the time a control message is queued, the loop
		// is usually already inside a sync transfer and will not look here
		// again for minutes.
		case m := <-e.ctrl:
			msg, stream, within = m.msg, StreamControl, m.within
		default:
			select {
			case <-e.stop:
				return
			case m := <-e.ctrl:
				msg, stream, within = m.msg, StreamControl, m.within
			case m := <-e.out:
				msg = m
			}
		}

		sctx, cancel := ctx, context.CancelFunc(func() {})
		if within > 0 {
			sctx, cancel = context.WithTimeout(ctx, within)
		} else if stream == StreamSync {
			sctx, cancel = context.WithCancel(ctx)
		}
		if stream == StreamSync {
			e.armPreempt(cancel)
		}
		err := e.s.SendOn(sctx, stream, e.dst, msg)
		yielded := stream == StreamSync && e.disarmPreempt()
		cancel()

		switch {
		case err == nil:
		case ctx.Err() != nil:
			return
		case yielded:
			// Not a failure and not a delivery: this endpoint took the air back
			// for something a person is waiting on. Counted separately, because
			// folding it into `failed` would make a working preemption look
			// like a broken radio — and this project has already spent two
			// nights on counters that described the wrong thing.
			e.mu.Lock()
			e.yielded++
			e.mu.Unlock()
		default:
			e.mu.Lock()
			e.failed++
			e.mu.Unlock()
			if lg != nil {
				lg.Printf("radio transfer of %d bytes did not arrive: %v",
					len(msg), err)
			}
		}
	}
}
