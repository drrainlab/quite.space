// Backpressure at the transport boundary.
//
// Capabilities.MaxPayload answers "how BIG may one packet be". Nothing in the
// contract answered "how MUCH will you take right now", and on a carrier with
// a rate rather than merely a size, those are different questions with
// different answers.
//
// The cost of not asking, measured on real hardware: LongFast spends about
// two seconds of airtime per packet, so a burst of fifteen fragments handed
// over at once is half a minute of transmission offered in an instant. The
// firmware queues what it can and discards the rest — silently, as far as a
// client that does not read its queue reports is concerned. With no
// retransmission, and with the rule that losing one fragment loses the whole
// message, a five-fragment message then completes roughly never. Twenty
// minutes of a two-node radio test produced not one delivery.
//
// Three decisions shape what follows.
//
// IT IS AN OPTIONAL INTERFACE, not a field on Capabilities. A TCP socket has
// no honest answer to "how many packets can you take": the kernel buffers,
// the window slides, and any number we invented would be a lie of the kind
// this package exists to avoid. An endpoint that cannot say stays silent, and
// silence means unmetered — exactly the behaviour every carrier had before.
//
// IT COUNTS PACKETS, NOT BYTES. The thing that overflows on these carriers is
// a queue of packets, and a queue of packets is what the carrier can report.
// Airtime is a consequence of packet size, which the sender already knows
// from MaxPayload; modelling it here would duplicate, and eventually
// contradict, what the radio itself is telling us.
//
// IT IS ASKED BEFORE A MESSAGE, NEVER DURING ONE. A message half-handed to a
// radio is worse than one not started: the fragments already sent spent real
// airtime and can never be assembled, because the rest will not follow. So a
// sender asks for room for the WHOLE message and either commits or waits.
package transports

import "time"

// CreditUnlimited is the answer from a carrier with no meaningful bound —
// a socket, a loopback, an in-memory harness.
const CreditUnlimited = -1

// Credit is what a carrier will accept right now.
type Credit struct {
	// Packets is how many more packets may be handed over before the
	// carrier begins discarding. CreditUnlimited means there is no bound
	// worth reporting. Zero means "nothing right now", and is a normal,
	// frequent answer on a radio — not an error.
	Packets int

	// RetryAfter is how long to wait before asking again when Packets is
	// zero. It exists so a sender waits the time the CARRIER knows about
	// rather than a interval the sender guessed. Zero means the sender may
	// use its own cadence.
	RetryAfter time.Duration

	// Known is false when the carrier tracks a queue but has not yet heard
	// from it — a radio between attaching and its first status report. It
	// separates "I can take nothing" from "I cannot say", which are
	// different instructions to a sender and must not be confused.
	Known bool
}

// Unlimited returns whether this credit imposes no bound.
func (c Credit) Unlimited() bool { return c.Packets == CreditUnlimited }

// Allows reports whether n packets may be handed over now.
//
// An UNKNOWN credit allows the send. A carrier that has not yet reported
// must not stall traffic forever, and the first message is how most carriers
// come to report anything at all.
func (c Credit) Allows(n int) bool {
	if c.Unlimited() || !c.Known {
		return true
	}
	return c.Packets >= n
}

// Pacer is implemented by endpoints whose carrier meters what it accepts.
//
// Implement it only where the number means something the carrier actually
// said. A guess here becomes a stall or a flood, and both are worse than the
// honest absence of the method.
type Pacer interface {
	// Credit reports what may be handed over right now. It must not block.
	Credit() Credit
}

// CreditOf asks an endpoint what it will take, for callers that hold the
// plain Endpoint interface. An endpoint that is not a Pacer is unmetered.
func CreditOf(ep Endpoint) Credit {
	if p, ok := ep.(Pacer); ok {
		return p.Credit()
	}
	return Credit{Packets: CreditUnlimited, Known: true}
}
