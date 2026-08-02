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

// Endpoint is a transports.Endpoint backed by Radio Transfer.
type Endpoint struct {
	s   *Session
	dst RadioAddress

	out    chan []byte
	stop   chan struct{}
	closed sync.Once

	mu    sync.Mutex
	inbox [][]byte
	// dropped counts messages the outbound queue could not take. It is a
	// counter rather than a silent discard because "the queue was full" and
	// "the radio lost it" are different problems with different fixes, and
	// only one of them is the carrier's.
	dropped int
	failed  int
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
	select {
	case e.out <- append([]byte(nil), msg...):
		return nil
	default:
		e.mu.Lock()
		e.dropped++
		e.mu.Unlock()
		return ErrCarrierFull
	}
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
		// carry, because that is refused before any airtime is spent.
		MaxPayload: e.s.lim.MaxMessageBytes,
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
				e.mu.Lock()
				e.inbox = append(e.inbox, got.Message)
				e.mu.Unlock()
			}
		}
		// SACKs are pushed from here rather than from a timer of their own:
		// this loop already runs whenever anything happens on the segment,
		// and a receiver with nothing to acknowledge has nothing to do.
		e.s.PumpSACKs(ctx, src)
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
		select {
		case <-e.stop:
			return
		case msg := <-e.out:
			if err := e.s.Send(ctx, e.dst, msg); err != nil {
				if ctx.Err() != nil {
					return
				}
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
}
