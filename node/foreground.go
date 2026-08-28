package node

// FOREGROUND IS A FACT THE RADIO PAYS FOR, so the node is told about it.
//
// The energy survey that forced this file counted the idle bill: three to
// eight relay round trips every two seconds, around the clock, from a
// phone in a pocket. The cost is not the bytes — it is the CELLULAR RADIO
// STATE MACHINE: after any packet the modem holds its high-power state
// for a carrier-tuned tail of ~10 seconds, so a packet every two seconds
// keeps it there forever. A phone doing nothing spent like a phone
// streaming.
//
// The fix is one honest bit from the shell: is a person looking? While
// they are, the heartbeat stays at the shipped two seconds — reading a
// live conversation must not go stale. The moment they are not, the sync
// stretches to backgroundCadence, which lets the radio spend most of each
// minute in idle. Anything URGENT still moves at once in both directions:
// an outgoing message kicks the loop out of turn (syncKick has always
// done that), and coming back to the foreground kicks it too, so the
// catch-up starts before the screen finishes waking.
//
// What this deliberately does NOT do is decide platform policy. Android
// calls it from its activity lifecycle, the desktop from its window-focus
// hooks; a shell that never calls it gets the foreground behaviour it has
// always had.

import "time"

// backgroundCadence is the heartbeat with nobody looking: thirty times
// the shipped cadence (60s as shipped). Chosen from the radio's own
// arithmetic — at one burst a minute the modem's high-power time falls to
// under a tenth of the always-connected bill — and bounded by honesty:
// a message that arrives while the phone sleeps in a pocket may wait up
// to a minute for its notification, and that is the price being paid on
// purpose.
const backgroundMultiplier = 30

// foregrounded reports whether a person is looking. Defaults to TRUE:
// a shell that never says otherwise keeps today's behaviour, and a
// headless node (relay mirror, gateway) is "always watched" by its job.
func (r *Runtime) foregrounded() bool {
	return r.backgrounded.Load() == 0
}

// syncInterval is the relay heartbeat the current attention state earns.
// With a parked listener vouching for arrivals (EN-2), the background
// poll stretches further still: the doorbell covers the news, and the
// slow poll stays only as the net under a lost notification.
func (r *Runtime) syncInterval(base time.Duration) time.Duration {
	if r.foregrounded() {
		return base
	}
	if r.relayListenHealthy() {
		return base * listenedMultiplier
	}
	return base * backgroundMultiplier
}

// SetForeground is the shell's one honest bit. Idempotent; concurrent-safe.
func (r *Runtime) SetForeground(fg bool) {
	var v int64
	if !fg {
		v = 1
	}
	prev := r.backgrounded.Swap(v)
	if prev == v {
		return
	}
	if fg {
		// Coming back is urgent in a way leaving is not: whatever piled
		// up while the radio slept should be on screen before the person
		// finishes lifting the phone. The kick wakes the sync loop out of
		// turn; the loop re-reads its interval on every wake.
		r.kickRelaySync()
	}
}
