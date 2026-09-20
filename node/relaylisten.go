package node

// EN-2 — THE RELAY TELLS US, so the radio can sleep.
//
// Wave 1 taught the node whether anybody is looking and stretched the
// idle heartbeat to a minute. This wave removes the reason the heartbeat
// existed: a dedicated connection per pull ingress PARKS the hints this
// device would otherwise poll — its per-space inboxes, its identity
// mailbox, its knock mailbox — and the relay says when something lands.
// Between arrivals the connection carries one ping every ListenPing
// (12 minutes), which is the difference between a modem that lives in
// its high-power state and one that visits it.
//
// The division of labour is deliberate and small: a notification NEVER
// carries content and never replaces the drain. It is a doorbell — the
// answer to it is kickRelaySync, and the existing sync cycle fetches
// through the existing capability discipline (PH-1 stands: parking takes
// hints, draining still takes capabilities). If listening fails — an old
// relay, a dead route, a mid-life disconnect — nothing is lost but
// latency: the polling loop is still underneath, at the background
// cadence, and the listener retries with backoff. Push is an
// optimisation of the poll, never a replacement for its honesty.
//
// While at least one listener is parked and healthy, the BACKGROUND poll
// stretches further still (listenedMultiplier): the doorbell covers
// arrivals, and the slow poll remains as the safety net that catches
// whatever a lost notification missed.

import (
	"errors"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	// listenedMultiplier is the background heartbeat with a parked
	// listener vouching for arrivals: 90 shipped cadences = three minutes.
	// It was ten (300): the net under a doorbell that could die in silence
	// (LT-2 — a carrier NAT drops the mapping, the socket stays "open", the
	// ping goes into the kernel buffer without error). Three minutes is
	// still a tenth of the always-on bill by the energy survey's own
	// arithmetic, and it bounds the worst case at three, not ten, while
	// the pong deadline (relay.PongTimeout) catches most deaths sooner.
	listenedMultiplier = 90
	// listenRetrySoon is the first retry after a session that WAS parked
	// ended — a relay restart, a dead socket. That is not a flapping route
	// and must not pay the ladder below: a deploy of three relays used to
	// cost every phone one to sixteen minutes of deafness per relay.
	listenRetrySoon       = 5 * time.Second
	listenRetrySoonJitter = 10 * time.Second
	// listenPingCellular is the parked keepalive on a carrier network:
	// their NAT tables are cut at five minutes on the common floor, so the
	// ping goes at four. Wi-Fi keeps relay.ListenPing (twelve).
	listenPingCellular = 4 * time.Minute
	// listenRetryMin/Max bound the reconnect backoff. The floor keeps a
	// flapping route from turning the listener into a dialer; the ceiling
	// keeps a long outage from parking the feature forever.
	listenRetryMin = time.Minute
	listenRetryMax = 16 * time.Minute
	// listenUnsupported is how long to leave an ingress alone after its
	// relay answered "unknown message type": an old relay stays old for
	// hours, not minutes, and polling covers the gap.
	listenUnsupported = 30 * time.Minute
	// maxListeners bounds the parked connections a device holds open.
	// Ingress lists grow (the pre-T4 fence keeps history); radios do not.
	maxListeners = 3
)

// relayListenLoop manages one parked connection per pull ingress. Started
// once at Open; lives until the runtime stops.
func (r *Runtime) relayListenLoop() {
	defer r.wg.Done()
	type state struct {
		stop chan struct{}
		done chan struct{}
	}
	states := map[string]*state{}
	tick := time.NewTicker(cadence * 5)
	defer tick.Stop()
	for {
		select {
		case <-r.stop:
			for _, st := range states {
				close(st.stop)
			}
			return
		case <-tick.C:
		}
		addrs := r.listenIngresses()
		// Reap listeners whose ingress left the list.
		keep := map[string]bool{}
		for _, a := range addrs {
			keep[a] = true
		}
		for a, st := range states {
			select {
			case <-st.done:
				// The goroutine ended; decide when to try again.
				delete(states, a)
			default:
				if !keep[a] {
					close(st.stop)
					delete(states, a)
				}
			}
		}
		for _, a := range addrs {
			if _, running := states[a]; running {
				continue
			}
			if len(states) >= maxListeners {
				break
			}
			if until, bad := r.listenBackoff(a); bad && time.Now().Before(until) {
				continue
			}
			st := &state{stop: make(chan struct{}), done: make(chan struct{})}
			states[a] = st
			r.wg.Add(1)
			go r.runListener(a, st.stop, st.done)
		}
	}
}

// listenIngresses is the same set the pull half of the sync reads — the
// armed relay first, then every once-advertised ingress — bounded and
// deterministic so the maxListeners cut is stable rather than a lottery.
// LOCK ORDER: rs.mu is taken FIRST, on its own, before r.mu — never
// nested inside it. relaySyncOnce holds rs.mu and takes r.mu inside its
// loop; nesting them here in the opposite order was a real AB-BA
// deadlock, caught by a -race run that froze three goroutines solid.
func (r *Runtime) listenIngresses() []string {
	seen := map[string]bool{}
	var out []string
	r.mu.Lock()
	rs := r.relaySync
	r.mu.Unlock()
	if rs != nil {
		rs.mu.Lock()
		if rs.addr != "" {
			seen[rs.addr] = true
			out = append(out, rs.addr)
		}
		rs.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var rest []string
	for _, ing := range r.ks.SelfIngress {
		if ing.Endpoint == "" || seen[ing.Endpoint] {
			continue
		}
		seen[ing.Endpoint] = true
		rest = append(rest, ing.Endpoint)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// listenHints is everything this device would poll at one relay: the
// per-recipient inbox of every relay-permitted space (current and previous
// bucket — the same pair the drain collects), the identity mailbox and the
// knock mailbox. Hints, never capabilities: a parked hint can wake us and
// nothing else.
func (r *Runtime) listenHints(addr string) [][]byte {
	now := uint64(time.Now().Unix())
	b := relay.Bucket(now)
	self := r.Device.ID
	var hints [][]byte
	add := func(h []byte) { hints = append(hints, h) }
	for _, tid := range r.relayMailboxSpaces() {
		if !r.TransportAllowed(TransportRelay, tid) {
			continue
		}
		add(relay.HintFor(tid, self, b))
		if b > 0 {
			add(relay.HintFor(tid, self, b-1))
		}
	}
	add(relay.HintIdentityPlane(self, b))
	add(relay.HintKnock(self, b))
	// THE DOOR. A standing invite is a mailbox on its rendezvous relay, and
	// nothing was listening at it: with the app in a pocket a guest's
	// request waited for the background poll — three minutes behind a
	// parked listener — so a person shared a link, locked their phone, and
	// the guest sat at "waiting for the owner" until the owner happened to
	// open the app (the owner's own report, 2026-09-20). The relay already
	// rings for any mailbox a listener names; the door is now one of them.
	// Only on the relay the pass lives on, and never past the relay's own
	// limit on a park — an owner with a hundred standing links keeps the
	// poll for the rest rather than losing the whole park to a refusal.
	for _, h := range r.passDoorHints(addr, passDoorHintsMax) {
		add(h)
	}
	return hints
}

// passDoorHintsMax bounds how many invite doors ride one park. The relay's
// default ceiling is generous and unknown from here; spaces come first.
const passDoorHintsMax = 16

// passDoorHints are the request mailboxes of this node's live passes on addr.
func (r *Runtime) passDoorHints(addr string, max int) [][]byte {
	if r.passes == nil {
		return nil
	}
	now := uint64(time.Now().Unix())
	r.passes.mu.Lock()
	defer r.passes.mu.Unlock()
	var out [][]byte
	for _, rec := range r.passes.byID {
		if len(out) >= max {
			break
		}
		if rec.revoked || rec.relay != addr || rec.pass == nil {
			continue
		}
		if rec.pass.ExpiresAt != 0 && rec.pass.ExpiresAt < now {
			continue
		}
		out = append(out, terminals.ReqHint(rec.pass.Rendezvous))
	}
	return out
}

// runListener parks one connection at one ingress and holds it until it
// dies, the hints rotate, or the loop is stopped. One connection, one
// job: the listening lane shares nothing with the pool's request lanes,
// because notifications arrive unasked and a concurrent round trip would
// read them as its reply.
func (r *Runtime) runListener(addr string, stop, done chan struct{}) {
	defer r.wg.Done()
	defer close(done)
	client, err := r.dialRelay(addr)
	if err != nil {
		r.noteListenFailure(addr, false)
		return
	}
	defer client.Close()
	if r.cellular.Load() {
		client.PingEvery = listenPingCellular
	}
	// A park that the relay just took is proof it is reachable: the pool's
	// breaker for this address, if it was counting a network blip against
	// the relay, lets go — and the loops try now rather than after their
	// own ladders (relaypool.go: reachable).
	client.OnParked = func() { r.relayReachable(addr) }
	r.listenMu.Lock()
	if r.listenSessions == nil {
		r.listenSessions = map[string]*relay.Client{}
	}
	r.listenSessions[addr] = client
	r.listenMu.Unlock()
	defer func() {
		r.listenMu.Lock()
		if r.listenSessions[addr] == client {
			delete(r.listenSessions, addr)
		}
		r.listenMu.Unlock()
	}()
	hints := r.listenHints(addr)
	if len(hints) == 0 {
		r.noteListenFailure(addr, false)
		return
	}
	// Re-park before the bucket rotates so the fresh hints are already
	// listening when writers move to them. Listen replaces the set, so
	// the cheapest correct protocol is: end this session at the boundary
	// and let the manager restart it with the new bucket's hints.
	untilRotate := time.Until(nextBucketRotation())
	rotate := time.NewTimer(untilRotate)
	defer rotate.Stop()
	sessionStop := make(chan struct{})
	epoch := r.listenEpoch()
	go func() {
		select {
		case <-stop:
		case <-r.stop:
		case <-rotate.C:
		case <-epoch:
			// Somebody changed what a park should carry — the doorbell
			// switch, most immediately. The session ends and the manager
			// re-parks within a tick with the new truth; waiting out a
			// bucket rotation would leave an OFF switch registered for
			// hours, which is not what "off" means.
		}
		close(sessionStop)
	}()
	r.markListening(addr, true)
	defer r.markListening(addr, false)
	notify := func([]byte) {
		// The doorbell. Content never rides a notification; the node
		// answers it through the capability discipline it already has.
		//
		// THE MAIL FIRST, ON ITS OWN LANE. The ring used to do nothing but
		// kick the sync cycle — and the cycle collects personal mail LAST,
		// after draining public ingress, collecting reply boxes on other
		// relays, publishing projections and fetching the outbox of every
		// public space this person reads. Measured on the owner's phone,
		// locked, in a dozen public spaces: 26 seconds from the ring to the
		// notification, all of it spent on things nobody had rung about.
		// The same reasoning that gave a said word its own outbox lane
		// (LT-3) applies to a heard one.
		//
		// A LANE OF ITS OWN, AND THAT WAS MEASURED TWICE. The obvious
		// alternative — the pull first on the sync loop's own goroutine —
		// was built and put on the same phone: 18 to 30 seconds again,
		// because a ring also earns a full cycle, the cycle takes twenty
		// seconds there, and the next message of a conversation rings into
		// a loop that is busy. A conversation is a burst; only a lane that
		// the cycle cannot occupy answers each ring.
		r.doorbellPull(addr)
		r.kickPassPoll()
		r.kickRelaySync()
	}
	// EN-3: the park carries the out-of-band endpoint when the person
	// turned that switch on, and carries an explicit CLEAR when they did
	// not — an off switch that only removes on transition would leave a
	// stale registration standing on the relay that last saw it on.
	if ep := r.GetSettings().PushEndpoint; ep != "" {
		err = client.ListenPush(hints, ep, sessionStop, notify)
	} else {
		err = client.ListenPushClear(hints, sessionStop, notify)
	}
	if err == nil {
		// A clean end: rotation or shutdown. The manager re-parks.
		r.noteListenSuccess(addr)
		return
	}
	var re relay.ErrRelay
	if errors.As(err, &re) && strings.Contains(re.Reason, "unknown message") {
		// An old relay. It will stay old for hours; polling covers it.
		r.noteListenFailure(addr, true)
		return
	}
	if client.ListenStats().ParkedAt.IsZero() {
		// Never parked: the park itself was refused or the dial died —
		// a route problem, which earns the ladder.
		r.noteListenFailure(addr, false)
		return
	}
	// A session that WAS parked ended: the relay restarted, the socket
	// died (relay.ErrListenSilent), the network moved. Ask again soon.
	// The doorbell is the whole latency story, and the kick brings the
	// poll forward so whatever landed while the park was dead is
	// collected now rather than at the next cadence.
	r.noteListenRetrySoon(addr)
	r.kickRelaySync()
}

// doorbellPull collects this device's own mail from the relay that just
// rang, now, without waiting for the sync cycle to get to it. Coalesced: a
// burst of rings while a pull is in flight becomes exactly one more pull
// after it — never a queue, never two at once on the same lane.
func (r *Runtime) doorbellPull(addr string) {
	// ONLY WHILE THE RELAY IS SWITCHED ON. A park outlives the setting by a
	// manager tick, and a ring in that window used to be harmless: it kicked
	// a loop that was no longer there. A pull of its own is not harmless —
	// it would go on draining a mailbox the person had just turned off
	// (t6: dave's copy vanished from the relay he had left).
	if !r.relaySyncArmed() {
		return
	}
	if !r.doorbellBusy.CompareAndSwap(false, true) {
		r.doorbellAgain.Store(true)
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.doorbellBusy.Store(false)
		for {
			r.doorbellAgain.Store(false)
			if r.stopped() {
				return
			}
			// An error is not reported from here: the cycle that the same
			// ring kicked pulls again and owns the status line. The RECEIPT
			// stays with the cycle too — sending it from this lane was tried
			// and put receipts into the mailboxes of peers reached over the
			// LAN (t6_lan_offload_test caught it); a sender therefore still
			// sees "relayed" for up to one cycle after the other phone has
			// already shown the message.
			_, _ = r.PullFromRelay(addr)
			if !r.doorbellAgain.Load() {
				return
			}
		}
	}()
}

// relaySyncArmed reports whether the background relay loop is running —
// the person's own relay setting, as the loop itself reads it.
func (r *Runtime) relaySyncArmed() bool {
	r.mu.Lock()
	rs := r.relaySync
	r.mu.Unlock()
	if rs == nil {
		return false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.stop != nil && rs.addr != ""
}

// kickPassPoll brings the invite-door poll forward (see ensurePassPolling).
func (r *Runtime) kickPassPoll() {
	select {
	case r.passKick <- struct{}{}:
	default:
	}
}

// ---- bookkeeping the manager and the heartbeat read ----

// markListening keeps the healthy-listener count the heartbeat consults:
// while it is positive, the background poll stretches to
// listenedMultiplier — the doorbell covers arrivals.
func (r *Runtime) markListening(addr string, on bool) {
	if on {
		r.listenParked.Add(1)
	} else {
		r.listenParked.Add(-1)
	}
	_ = addr
}

// relayListenHealthy reports whether at least one parked listener stands.
func (r *Runtime) relayListenHealthy() bool { return r.listenParked.Load() > 0 }

func (r *Runtime) noteListenSuccess(addr string) {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	delete(r.listenRetry, addr)
}

// noteListenRetrySoon schedules the next park in listenRetrySoon plus
// jitter and forgets the ladder: a restart is not a flap.
func (r *Runtime) noteListenRetrySoon(addr string) {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	if r.listenRetry == nil {
		r.listenRetry = map[string]listenRetryState{}
	}
	jitter := time.Duration(rand.Int64N(int64(listenRetrySoonJitter)))
	r.listenRetry[addr] = listenRetryState{at: time.Now().Add(listenRetrySoon + jitter)}
}

// SetNetwork is the shell's second honest bit (LT-2), beside foreground:
// whether this device is on a carrier network, and which network it is
// on. Cellular shortens the parked keepalive to what carrier NATs
// tolerate; a CHANGE of network ends every parked session at once — the
// old sockets belong to a path that no longer exists — and kicks the
// sync so the new park and the catch-up start now. A shell that never
// calls it keeps the Wi-Fi assumptions.
func (r *Runtime) SetNetwork(cellular bool, network string) {
	r.cellular.Store(cellular)
	r.listenMu.Lock()
	changed := network != r.lastNetwork
	r.lastNetwork = network
	r.listenMu.Unlock()
	if changed {
		r.BounceListeners()
		r.kickRelaySync()
	}
}

// ListenerDiag is one parked session as the diagnostics show it.
type ListenerDiag struct {
	Addr         string `json:"addr"`
	ParkedForS   int64  `json:"parked_for_s"`
	LastPongAgoS int64  `json:"last_pong_ago_s"` // -1 = no pong yet
	Pings        int    `json:"pings"`
	Pongs        int    `json:"pongs"`
	Notifies     int    `json:"notifies"`
	Wakes        int    `json:"wakes"`
	PingEveryS   int64  `json:"ping_every_s"`
}

// listenerDiags snapshots every live parked session.
func (r *Runtime) listenerDiags() []ListenerDiag {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	var out []ListenerDiag
	now := time.Now()
	for addr, c := range r.listenSessions {
		st := c.ListenStats()
		if st.ParkedAt.IsZero() {
			continue
		}
		d := ListenerDiag{Addr: addr, ParkedForS: int64(now.Sub(st.ParkedAt) / time.Second),
			LastPongAgoS: -1, Pings: st.Pings, Pongs: st.Pongs, Notifies: st.Notifies, Wakes: st.Wakes}
		if !st.LastPong.IsZero() {
			d.LastPongAgoS = int64(now.Sub(st.LastPong) / time.Second)
		}
		every := c.PingEvery
		if every <= 0 {
			every = relay.ListenPing
		}
		d.PingEveryS = int64(every / time.Second)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

func (r *Runtime) noteListenFailure(addr string, unsupported bool) {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	if r.listenRetry == nil {
		r.listenRetry = map[string]listenRetryState{}
	}
	st := r.listenRetry[addr]
	if unsupported {
		st.at = time.Now().Add(listenUnsupported)
	} else {
		wait := listenRetryMin << st.failures
		if wait > listenRetryMax {
			wait = listenRetryMax
		}
		st.at = time.Now().Add(wait)
		if st.failures < 8 {
			st.failures++
		}
	}
	r.listenRetry[addr] = st
}

// listenBackoff answers "leave this ingress alone until when?".
func (r *Runtime) listenBackoff(addr string) (time.Time, bool) {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	st, bad := r.listenRetry[addr]
	return st.at, bad
}

type listenRetryState struct {
	at       time.Time
	failures uint
}

// listenEpoch is the channel the CURRENT listener sessions watch;
// bounceListeners closes it, ending every session so the manager re-parks
// with whatever changed. Lazily created so a runtime that never listens
// never allocates it.
func (r *Runtime) listenEpoch() <-chan struct{} {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	if r.listenEpochCh == nil {
		r.listenEpochCh = make(chan struct{})
	}
	return r.listenEpochCh
}

// BounceListeners ends every parked session so the next park carries the
// current settings — called when the push endpoint changes, because an
// off switch that stays registered until the next bucket rotation is not
// an off switch.
func (r *Runtime) BounceListeners() {
	r.listenMu.Lock()
	defer r.listenMu.Unlock()
	if r.listenEpochCh != nil {
		close(r.listenEpochCh)
		r.listenEpochCh = nil
	}
}

// nextBucketRotation is the wall-clock moment the current hint bucket
// ends, plus a small grace so the writer side has surely moved.
func nextBucketRotation() time.Time {
	const bucketLen = 6 * 3600
	now := time.Now().Unix()
	next := (now/bucketLen + 1) * bucketLen
	return time.Unix(next+30, 0)
}

// relayReachable is the listener's word that addr answered a park just
// now. Read WITHOUT pool(), which would create one: a node that never
// pushed has no breaker to reset.
func (r *Runtime) relayReachable(addr string) {
	if p := r.relayPoolV.Load(); p != nil {
		p.reachable(addr)
	}
	r.kickRelaySync()
}
