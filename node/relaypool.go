// The relay connection pool (RR-2). Before this file every relay
// operation dialed fresh: an ECDSA keygen plus a TLS handshake, up to
// ~eight times every two seconds on the sync path. The pool keeps up to
// TWO persistent connections per relay:
//
//	control lane — probes, collects, small puts: everything latency-bound
//	bulk lane    — fetches and large puts, opened LAZILY: everything
//	               throughput-bound
//
// Two lanes because the wire is strictly serial (no request ids): one
// mutex-ordered connection would let a large Fetch block a join decision
// behind it. Two is still two handshakes total, not dozens per minute.
//
// Health is per address: consecutive failures mark a relay unhealthy for
// a cooldown; reconnects follow a full-jitter backoff so a dead relay's
// return is not a thundering herd. A pin mismatch (ErrRelayUntrusted) is
// NOT a health state — it is never auto-retried; only an explicit
// re-trust clears it.
package node

import (
	"errors"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	poolUnhealthyAfter = 3                // consecutive failures
	poolCooldown       = 30 * time.Second // unhealthy hold before a retry
	poolIdleClose      = 5 * time.Minute
	// localOutageRetry is the wait after a dial that never left the machine.
	// Short, because such a dial costs nothing and the only thing standing
	// between the person and a working relay is asking again.
	localOutageRetry = 2 * time.Second
)

// poolBackoff is the full-jitter reconnect schedule: the nth consecutive
// dial failure waits a uniformly random duration inside its window.
func poolBackoff(attempt int, rng func() float64) time.Duration {
	windows := [...][2]time.Duration{
		{0, 5 * time.Second},
		{5 * time.Second, 15 * time.Second},
		{15 * time.Second, 45 * time.Second},
		{45 * time.Second, 120 * time.Second},
	}
	w := windows[min(attempt, len(windows)-1)]
	return w[0] + time.Duration(rng()*float64(w[1]-w[0]))
}

// relayLane is one persistent connection, serialized by its mutex.
type relayLane struct {
	mu       sync.Mutex
	client   *relay.Client
	lastUsed time.Time
}

// relayPeer is one relay's pool entry.
type relayPeer struct {
	// localOutage: the last dial never left this machine. Kept apart from
	// `failures` because it says something about US, and the difference
	// decides both how soon to try again and what the diagnostics call it.
	localOutage bool

	addr    string
	control relayLane
	bulk    relayLane

	mu           sync.Mutex
	failures     int       // consecutive, resets after recovery
	backoffUntil time.Time // no dial attempts before this
	attempt      int       // backoff ladder position
	untrusted    bool      // pin mismatch: no auto-retry, ever
	// successStreak gates RECOVERY (RR-6): a relay that failed does not
	// read healthy again on one lucky round trip — two consecutive
	// successes clear the ladder.
	successStreak int
}

// relayPool is the runtime's connection pool.
type relayPool struct {
	dial  func(string) (*relay.Client, error)
	mu    sync.Mutex
	peers map[string]*relayPeer
	stop  chan struct{}
	once  sync.Once
}

func newRelayPool(dial func(string) (*relay.Client, error)) *relayPool {
	return &relayPool{dial: dial, peers: map[string]*relayPeer{}, stop: make(chan struct{})}
}

// pool returns the runtime's connection pool, created on first use; its
// janitor lives until Runtime.Close (closeAll closes p.stop).
// The pointer is atomic because Close reads it WITHOUT going through the
// Once — it must not create a pool merely to shut one down, and it cannot
// claim the Once either (a later pool() would then get nil). sync.Once
// orders the field only for callers of Do, so a plain field here raced a
// shutdown against a sync goroutine's first use; the pool created inside
// that window kept its janitor running past Close.
func (r *Runtime) pool() *relayPool {
	r.poolOnce.Do(func() {
		p := newRelayPool(r.dialRelay)
		r.relayPoolV.Store(p)
		go p.janitor()
	})
	return r.relayPoolV.Load()
}

// withRelayControl runs fn with the pooled control-lane client — probes,
// collects, small puts. The lane is serialized; fn's error decides the
// connection's fate (protocol-level refusals keep it, transport errors
// drop it and advance the health ladder).
func (r *Runtime) withRelayControl(addr string, fn func(*relay.Client) error) error {
	client, release, err := r.pool().Control(addr)
	if err != nil {
		return err
	}
	opErr := fn(client)
	release(opErr)
	return opErr
}

// withRelayBulk is the throughput lane: fetches and large puts. Opened
// lazily — a node that never moves big items never opens it.
func (r *Runtime) withRelayBulk(addr string, fn func(*relay.Client) error) error {
	client, release, err := r.pool().Bulk(addr)
	if err != nil {
		return err
	}
	opErr := fn(client)
	release(opErr)
	return opErr
}

// bulkThreshold: bodies at or above this ride the bulk lane so a large
// transfer cannot head-of-line-block a join decision on the serial wire.
const bulkThreshold = 64 << 10

func (p *relayPool) peer(addr string) *relayPeer {
	p.mu.Lock()
	defer p.mu.Unlock()
	pe, ok := p.peers[addr]
	if !ok {
		pe = &relayPeer{addr: addr}
		p.peers[addr] = pe
	}
	return pe
}

// Health words for diagnostics (RR-6 grows the ladder).
func (p *relayPool) health(addr string) string {
	pe := p.peer(addr)
	pe.mu.Lock()
	defer pe.mu.Unlock()
	switch {
	case pe.untrusted:
		return "untrusted"
	case pe.localOutage:
		// NOT "offline". The relay may be perfectly well; this device could
		// not send a packet. Saying the wrong one sends somebody to check a
		// server when the answer is their own Wi-Fi.
		return "no network here"
	case time.Now().Before(pe.backoffUntil):
		return "offline"
	case pe.failures > 0:
		return "degraded"
	}
	return "healthy"
}

var errRelayCoolingDown = errors.New("node: relay is cooling down after failures")

// acquire hands the caller the lane's client under the lane mutex.
// release(err) MUST be called exactly once: err == nil marks success;
// a non-nil err drops the connection and advances the failure ladder.
func (p *relayPool) acquire(pe *relayPeer, lane *relayLane) (*relay.Client, func(error), error) {
	pe.mu.Lock()
	if pe.untrusted {
		pe.mu.Unlock()
		return nil, nil, ErrRelayUntrusted{Endpoint: pe.addr}
	}
	if time.Now().Before(pe.backoffUntil) {
		pe.mu.Unlock()
		return nil, nil, errRelayCoolingDown
	}
	pe.mu.Unlock()

	lane.mu.Lock()
	if lane.client == nil {
		c, err := p.dial(pe.addr)
		if err != nil {
			lane.mu.Unlock()
			p.noteFailure(pe, err)
			return nil, nil, err
		}
		lane.client = c
	}
	lane.lastUsed = time.Now()
	client := lane.client
	release := func(err error) {
		switch {
		case err == nil:
			pe.mu.Lock()
			pe.localOutage = false
			pe.successStreak++
			// Recovery needs TWO consecutive successes (RR-6) — one lucky
			// round trip after a failure run proves nothing.
			if pe.failures == 0 || pe.successStreak >= 2 {
				pe.failures, pe.attempt = 0, 0
			}
			pe.mu.Unlock()
		case isConnFatal(err):
			// Connection-level trouble: drop it so the next acquire
			// redials, and advance the health ladder.
			lane.client.Close()
			lane.client = nil
			p.noteFailure(pe, err)
		default:
			// Application-level outcome over a working connection — a
			// relay refusal, a routine "no projection yet", a verification
			// error. Keep the connection, move no health counter.
		}
		lane.lastUsed = time.Now()
		lane.mu.Unlock()
	}
	return client, release, nil
}

// Control and Bulk are the two lanes' entry points.
func (p *relayPool) Control(addr string) (*relay.Client, func(error), error) {
	pe := p.peer(addr)
	return p.acquire(pe, &pe.control)
}
func (p *relayPool) Bulk(addr string) (*relay.Client, func(error), error) {
	pe := p.peer(addr)
	return p.acquire(pe, &pe.bulk)
}

// ourNetworkIsDown says the dial never left this machine: the OS refused to
// send. ENETUNREACH and friends are what a phone with Wi-Fi off produces, and
// they are a fact about US, not about the relay.
func ourNetworkIsDown(err error) bool {
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.ENETDOWN) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	// Not every platform surfaces the errno through the error chain, and the
	// cost of being wrong here is small in one direction only: a retry that
	// fails instantly costs nothing, while a missed classification costs the
	// minute this exists to remove.
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "network is unreachable") ||
		strings.Contains(s, "no route to host") ||
		strings.Contains(s, "network is down")
}

func (p *relayPool) noteFailure(pe *relayPeer, err error) {
	var untrusted ErrRelayUntrusted
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.successStreak = 0
	if errors.As(err, &untrusted) {
		pe.untrusted = true // cleared only by an explicit re-trust
		return
	}
	// A LOCAL OUTAGE IS NOT THE RELAY'S HEALTH. The breaker below exists to
	// stop a node hammering a relay that is struggling, and to stop it
	// spending a battery on round trips that will not land. Neither applies
	// when the operating system refuses to send at all: the dial fails
	// instantly, locally, without a packet.
	//
	// Charging those to the relay is what turned "Wi-Fi was off for a
	// minute" into "the relay is unreachable for another two": three quick
	// failures bought a thirty-second hold, and the ladder after it runs to
	// two minutes. Measured on a phone, watching a screen say the wrong
	// thing long after the network came back.
	if ourNetworkIsDown(err) {
		pe.localOutage = true
		pe.backoffUntil = maxTime(pe.backoffUntil,
			time.Now().Add(localOutageRetry))
		return
	}
	pe.localOutage = false
	pe.failures++
	if pe.failures >= poolUnhealthyAfter {
		pe.backoffUntil = time.Now().Add(poolCooldown)
	}
	pe.backoffUntil = maxTime(pe.backoffUntil,
		time.Now().Add(poolBackoff(pe.attempt, rand.Float64)))
	pe.attempt++
}

// resetTrust clears the untrusted latch — called after an explicit
// re-confirmation, never automatically.
func (p *relayPool) resetTrust(addr string) {
	pe := p.peer(addr)
	pe.mu.Lock()
	pe.untrusted, pe.failures, pe.attempt = false, 0, 0
	pe.backoffUntil = time.Time{}
	pe.mu.Unlock()
}

// closeAll shuts every connection (runtime shutdown).
func (p *relayPool) closeAll() {
	p.once.Do(func() { close(p.stop) })
	p.mu.Lock()
	peers := make([]*relayPeer, 0, len(p.peers))
	for _, pe := range p.peers {
		peers = append(peers, pe)
	}
	p.mu.Unlock()
	for _, pe := range peers {
		for _, lane := range []*relayLane{&pe.control, &pe.bulk} {
			lane.mu.Lock()
			if lane.client != nil {
				lane.client.Close()
				lane.client = nil
			}
			lane.mu.Unlock()
		}
	}
}

// janitor closes idle lanes; started with the runtime's sync machinery.
func (p *relayPool) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.mu.Lock()
			peers := make([]*relayPeer, 0, len(p.peers))
			for _, pe := range p.peers {
				peers = append(peers, pe)
			}
			p.mu.Unlock()
			for _, pe := range peers {
				for _, lane := range []*relayLane{&pe.control, &pe.bulk} {
					lane.mu.Lock()
					if lane.client != nil && time.Since(lane.lastUsed) > poolIdleClose {
						lane.client.Close()
						lane.client = nil
					}
					lane.mu.Unlock()
				}
			}
		}
	}
}

// isConnFatal: only errors that mean the CONNECTION itself is broken —
// network failures, reply timeouts, a closed conn. A relay refusal
// (ErrRelay: rate limit, bad cap, oversize) means the relay ANSWERED,
// and node-level sentinels (no projection yet, verification failures)
// are facts about content — none of those may poison the connection or
// the health ladder.
func isConnFatal(err error) bool {
	if err == nil {
		return false
	}
	var re relay.ErrRelay
	if errors.As(err, &re) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	// The transport's OWN sentinel, matched by identity rather than by text.
	//
	// This line is the fix for the defect AR-0c found on a phone: the string
	// test below looks for "use of closed", which is the net package's wording,
	// and the lan transport says "lan: connection closed". They never matched,
	// so a dead connection was never fatal, never retired, and handed back for
	// the life of the process — the node kept pushing and pulled NOTHING until
	// it was restarted. Matching a sibling package's error by substring is what
	// made that possible, so the sentinel goes first and the strings stay only
	// for errors nobody owns.
	if errors.Is(err, lan.ErrConnClosed) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "timed out waiting for reply") ||
		strings.Contains(s, "use of closed")
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
