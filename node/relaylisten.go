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
	"sort"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	// listenedMultiplier is the background heartbeat with a parked
	// listener vouching for arrivals: 300 shipped cadences = ten minutes.
	// Not longer, deliberately — the poll is the safety net for lost
	// notifications, and a net with ten-minute holes is still a net.
	listenedMultiplier = 300
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
func (r *Runtime) listenIngresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	if rs := r.relaySync; rs != nil {
		rs.mu.Lock()
		if rs.addr != "" {
			seen[rs.addr] = true
			out = append(out, rs.addr)
		}
		rs.mu.Unlock()
	}
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
func (r *Runtime) listenHints() [][]byte {
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
	return hints
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
	hints := r.listenHints()
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
		// The doorbell. Content never rides a notification; the sync
		// cycle answers it through the capability discipline it already
		// has, and the 1-deep kick channel coalesces a burst for free.
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
	r.noteListenFailure(addr, false)
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
