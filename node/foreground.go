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
	// A TRANSFER IN FLIGHT IS ATTENTION OF ITS OWN. A holder that just
	// answered a want knows the next ask is coming — a photo is several
	// rounds of ask, answer, collect — and a phone in a pocket should not
	// price each round at the background minute (three, under a parked
	// doorbell, when the doorbell is what the OS froze). While an answer
	// went out within servingWindow the cadence is servingCadence: radio
	// spent only while somebody's picture is actually crossing.
	if until := r.servingUntil.Load(); until > 0 && time.Now().UnixNano() < until {
		if servingCadence > base {
			return servingCadence
		}
		return base
	}
	// A PUBLISHER OF PUBLIC SPACES IS NOT ONLY LISTENING. Contributions
	// and media wants for an owned public space arrive through the
	// ingress shards, which are drained by the cycle and not covered by
	// the parked doorbell; ten minutes between drains would be ten
	// minutes for a stranger's comment to appear. Such a node keeps the
	// plain background minute.
	if r.relayListenHealthy() && !r.publishesPublic() {
		return base * listenedMultiplier
	}
	return base * backgroundMultiplier
}

// publishesPublic reports whether this node owns any public space.
func (r *Runtime) publishesPublic() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for tid, st := range r.spaces {
		if st == nil {
			continue
		}
		if meta, ok := r.ks.Spaces[tid]; ok && meta.Owned && st.space.Policy().IsPublic() {
			return true
		}
	}
	return false
}

// attentionWindow is how long one use of the local API counts as
// "somebody is looking" for a shell that infers attention that way.
// A variable so a test can watch the window close.
var attentionWindow = 30 * time.Second

// EnableAttentionFromAPI is for shells with no window of their own — the
// CLI `terminal ui` with or without a browser, a headless node. The
// default "always watched" was right for a relay mirror and wrong for a
// catalog publisher that nobody has looked at for a fortnight: it kept
// the two-second heartbeat around the clock. With this on, attention is a
// FACT again — the local API was used within attentionWindow — and the
// node starts in the background until somebody does.
//
// A shell WITH a window may ask for it too (the desktop does): attention
// is then the window being the one worked in OR the interface having
// asked the node something within attentionWindow. The window on a
// second monitor, read while an editor has the focus, polls every ten
// seconds and stays live; the window hidden in the tray polls nothing
// and the node goes to the background a window later. Before this the
// desktop counted only the focus edge, and a person who clicked into
// another application put their own node on the background minute.
func (r *Runtime) EnableAttentionFromAPI() {
	if r.attentionFromAPI.Swap(true) {
		return
	}
	r.attnShellAway.Store(true)
	r.applyAttention()
}

// NoteAttention is called by the API door on every authorised request.
// No-op unless EnableAttentionFromAPI was asked for.
func (r *Runtime) NoteAttention() {
	if !r.attentionFromAPI.Load() {
		return
	}
	r.attMu.Lock()
	if r.attTimer == nil {
		r.attTimer = time.AfterFunc(attentionWindow, func() {
			r.attnAPI.Store(false)
			r.applyAttention()
		})
	} else {
		r.attTimer.Reset(attentionWindow)
	}
	r.attMu.Unlock()
	r.attnAPI.Store(true)
	r.applyAttention()
}

// SetForeground is the shell's one honest bit. Idempotent; concurrent-safe.
func (r *Runtime) SetForeground(fg bool) {
	r.attnShellAway.Store(!fg)
	r.applyAttention()
}

// applyAttention folds the two sources into the one bit the loops read:
// somebody is looking when the shell says so or the API was just used.
func (r *Runtime) applyAttention() {
	fg := !r.attnShellAway.Load() || r.attnAPI.Load()
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

// KickRelaySync is the exported face of the sync kick, for shells whose
// doorbell rang: the ping carried nothing, the drain fetches everything.
func (r *Runtime) KickRelaySync() { r.kickRelaySync() }

// DoorbellRing is what a platform push means to the node: the same three
// things a ring on the parked connection does (relaylisten.go) — the mail
// first, on its own lane, from this node's own relay; the invite doors;
// then the full cycle. Safe with no relay configured.
func (r *Runtime) DoorbellRing() {
	if addr := r.ResolvePersonalRelay(); addr != "" {
		r.doorbellPull(addr)
	}
	r.kickPassPoll()
	r.kickRelaySync()
}

// servingCadence is the heartbeat while this node is answering somebody's
// media; servingWindow is how long one answer keeps it, refreshed by the
// next. Two minutes covers the requester's own background collect.
const (
	servingCadence = 10 * time.Second
	servingWindow  = 2 * time.Minute
)

// noteServing is called when an answer actually left for a relay.
func (r *Runtime) noteServing() {
	r.servingUntil.Store(time.Now().Add(servingWindow).UnixNano())
}
