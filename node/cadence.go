package node

import "time"

// THE BACKGROUND CADENCE, named once.
//
// A node has several loops that ask "anything new?" on a timer — the relay
// sync, the pass mailbox, a join in flight, a peer's first retry, the LAN
// announce. Every one of them said "2 * time.Second" in its own file, and
// every one meant the same thing: the ordinary heartbeat of a device that
// is neither hurrying nor asleep. Naming it makes that sameness a fact
// rather than a coincidence, and gives the test suite ONE knob.
//
// WHY THE SUITE NEEDS THE KNOB — measured, not supposed. Almost everything
// this package proves is proved by moving something between two nodes and
// looking at the other end, and every hop waits for a tick. A goroutine
// sampler over the 52 slowest tests showed the tests standing in
// waitUntil/waitJoin/waitState for 270 of 482 seconds, while every node
// goroutine in the same snapshots was ASLEEP in a ticker — nothing was
// working, everything was waiting for the next beat. Shrinking the beat is
// therefore the honest fix: the order of events is untouched, the ratios
// between loops are untouched, only the silence between beats is shorter.
//
// Two rules keep this from lying:
//   - the shipped value is a constant, asserted by
//     TestTheShippedCadenceIsTwoSeconds, so lowering it for tests can never
//     quietly lower it for people;
//   - the knob is set once in TestMain, before any goroutine exists, so
//     nothing races it. It is a variable only because Go has no other way
//     to give a package a test-time constant.
//
// What this is NOT: a knob for the give-up deadlines (noSourceAfter,
// fetchIdleGiveUp, the pool's backoff windows). Those were tried and they
// are load-bearing — shortening them broke three fetch tests, because a
// transfer that needs the time simply fails when it is not given. A beat is
// how often you look; a deadline is how long you are willing to wait. Only
// the first belongs here.
const shippedCadence = 2 * time.Second

// cadence is the live value. Production never writes it.
var cadence = shippedCadence

// cadenceTicker is time.NewTicker at the background cadence, or at a
// multiple of it — so a loop that used to tick every "3 * time.Second"
// with the LAN announce says cadenceTicker(1.5) and keeps its ratio to
// everything else.
func cadenceTicker(mul float64) *time.Ticker {
	d := time.Duration(float64(cadence) * mul)
	if d <= 0 {
		d = time.Millisecond
	}
	return time.NewTicker(d)
}
