package node

// AN-2 — THE KEYLESS WATCH.
//
// Measured on a handset: Android kills the process and brings the app's
// service back in seconds over a CLOSED node. With the passphrase sealed
// behind a code — the person's own choice, and the right one — nothing can
// reopen it, and the phone hears nothing until somebody happens to open the
// app. The owner's question: is there a secure way to learn only THAT
// something arrived, revealing no content and copying no passphrase?
//
// There is, and the relay protocol already had the shape of it. A mailbox is
// ADDRESSED by a 16-byte hint and DRAINED by a capability (PH-1): "a parked
// hint can wake us and nothing else". A hint is derived from ids and a
// six-hour bucket number, not from any key — so while the node is open it can
// write down the hints of its own mailboxes for the days ahead, and a watcher
// holding only that list can park at the relay and learn one bit: mail.
//
// WHAT THE LIST IS, SAID PLAINLY (the owner's review, 2026-09-20): not
// harmless hashes but a bounded right to WATCH. Whoever takes the file can
// learn, for as long as it is valid, that something arrived for this device
// — never what, never from whom, and they can neither read nor drain it
// (that needs the capability, which is never written here). So it is kept in
// the app's private storage, out of backups, it expires, and its past is
// trimmed.
//
// WHAT THE RELAY LEARNS: nothing it does not learn from the ordinary
// listener, PROVIDED the watcher parks only the epochs it needs — the current
// bucket, the previous one (late delivery), and the next only in the minutes
// before a rollover. Parking the whole week at once would hand the relay the
// linkage between this device's future addresses; this does not.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

const (
	watchPlanVersion = 1
	// WatchPlanDays is how far ahead a plan reaches. After that the watch has
	// no addresses left and says so; opening the app writes a new plan.
	WatchPlanDays = 7
	bucketSeconds = 6 * 3600
	// watchNextBucketLead: how long before a rollover the next epoch's hints
	// join the park, so a Put addressed by a sender whose clock is a little
	// ahead is not missed.
	watchNextBucketLead = 10 * time.Minute
	watchRetryMin       = 5 * time.Second
	watchRetryMax       = 15 * time.Minute
)

// WatchEndpoint is one relay the device listens at, with everything needed to
// trust it again — the watcher has no keystore and no relay state to consult.
type WatchEndpoint struct {
	Addr string `json:"addr"`
	// Pins: the SPKI pin set in force when the plan was written. Empty means
	// the endpoint was dialled without one then too (official local-lan or
	// loopback) — never "trust anything".
	Pins []string `json:"pins,omitempty"`
}

// WatchPlan is the whole of what the watcher may know.
type WatchPlan struct {
	Version   int             `json:"v"`
	MadeAt    int64           `json:"made_at"`
	ExpiresAt int64           `json:"expires_at"`
	Endpoints []WatchEndpoint `json:"endpoints"`
	// Buckets: bucket number → the hex hints of this device's mailboxes in
	// that epoch. Hints only; a capability never appears in this file.
	Buckets map[string][]string `json:"buckets"`
}

// listenHintsAt is listenHints for ONE epoch.
func (r *Runtime) listenHintsAt(b uint64) [][]byte {
	self := r.Device.ID
	var hints [][]byte
	for _, tid := range r.relayMailboxSpaces() {
		if !r.TransportAllowed(TransportRelay, tid) {
			continue
		}
		hints = append(hints, relay.HintFor(tid, self, b))
	}
	hints = append(hints, relay.HintIdentityPlane(self, b), relay.HintKnock(self, b))
	return hints
}

// BuildWatchPlan writes down where this device will be listening for the
// next `days` days. Zero or negative means WatchPlanDays.
func (r *Runtime) BuildWatchPlan(days int) WatchPlan {
	if days <= 0 {
		days = WatchPlanDays
	}
	now := time.Now()
	p := WatchPlan{
		Version: watchPlanVersion, MadeAt: now.Unix(),
		ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour).Unix(),
		Buckets:   map[string][]string{},
	}
	for _, addr := range r.listenIngresses() {
		ep := WatchEndpoint{Addr: addr}
		official := false
		for _, d := range BuiltinRelayRegistry().Relays {
			if d.Endpoint == addr {
				ep.Pins = append([]string(nil), d.SPKIPins...)
				official = true
				break
			}
		}
		if !official && !loopbackAddr(addr) {
			pin, ok := r.loadRelayState().TrustedPin(addr)
			if !ok {
				continue // never confirmed: the watcher must not be the one to trust it
			}
			ep.Pins = []string{pin}
		}
		p.Endpoints = append(p.Endpoints, ep)
	}
	first := relay.Bucket(uint64(now.Unix()))
	if first > 0 {
		first-- // the previous epoch is still being delivered into
	}
	last := relay.Bucket(uint64(p.ExpiresAt))
	for b := first; b <= last; b++ {
		var hs []string
		for _, h := range r.listenHintsAt(b) {
			hs = append(hs, hex.EncodeToString(h))
		}
		p.Buckets[strconv.FormatUint(b, 10)] = hs
	}
	return p
}

// WriteWatchPlan stores a plan atomically with owner-only permissions.
func WriteWatchPlan(path string, p WatchPlan) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadWatchPlan loads a plan and drops the epochs that are already over, so
// the past does not linger on disk or in memory.
func ReadWatchPlan(path string, now time.Time) (WatchPlan, error) {
	var p WatchPlan
	raw, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.Version != watchPlanVersion {
		return p, errors.New("node: watch plan from another version")
	}
	cur := relay.Bucket(uint64(now.Unix()))
	for k := range p.Buckets {
		b, err := strconv.ParseUint(k, 10, 64)
		if err != nil || b+1 < cur {
			delete(p.Buckets, k)
		}
	}
	return p, nil
}

// hintsAt is what to park NOW: the current epoch, the previous one, and the
// next only when a rollover is close. ok is false when the plan has nothing
// for the current epoch — it has run out.
func (p WatchPlan) hintsAt(now time.Time) (hints [][]byte, ok bool) {
	if now.Unix() >= p.ExpiresAt {
		return nil, false
	}
	cur := relay.Bucket(uint64(now.Unix()))
	want := []uint64{cur}
	if cur > 0 {
		want = append(want, cur-1)
	}
	if untilNextBucket(now) <= watchNextBucketLead {
		want = append(want, cur+1)
	}
	seen := map[string]bool{}
	for i, b := range want {
		hs, have := p.Buckets[strconv.FormatUint(b, 10)]
		if i == 0 && !have {
			return nil, false
		}
		for _, h := range hs {
			if seen[h] {
				continue
			}
			raw, err := hex.DecodeString(h)
			if err != nil || len(raw) != relay.HintLen {
				continue
			}
			seen[h] = true
			hints = append(hints, raw)
		}
	}
	sort.Slice(hints, func(i, j int) bool { return string(hints[i]) < string(hints[j]) })
	return hints, len(hints) > 0
}

func untilNextBucket(now time.Time) time.Duration {
	s := now.Unix()
	return time.Duration(bucketSeconds-(s%bucketSeconds)) * time.Second
}

// WatchEvents is how the watcher speaks. Every callback may be nil.
type WatchEvents struct {
	// Mail: something is waiting or has just arrived. Called at most once per
	// session of trouble — the caller shows ONE line, not one per message.
	Mail func()
	// Expired: the plan has no addresses for now. The watch ends.
	Expired func()
	// State is a short word for diagnostics: "parked", "retrying", "expired".
	State func(addr, state string)
}

// RunWatch parks at every endpoint of the plan until stop closes or the plan
// runs out. It holds no key, reads no mailbox, acknowledges nothing and
// deletes nothing: it can be woken, and that is all it can do.
func RunWatch(p WatchPlan, ev WatchEvents, stop <-chan struct{}) {
	if len(p.Endpoints) == 0 {
		if ev.Expired != nil {
			ev.Expired()
		}
		return
	}
	done := make(chan struct{}, len(p.Endpoints))
	expired := make(chan struct{}, len(p.Endpoints))
	for _, ep := range p.Endpoints {
		go func(ep WatchEndpoint) {
			defer func() { done <- struct{}{} }()
			if watchEndpoint(p, ep, ev, stop) {
				expired <- struct{}{}
			}
		}(ep)
	}
	for range p.Endpoints {
		<-done
	}
	select {
	case <-expired:
		if ev.Expired != nil {
			ev.Expired()
		}
	default:
	}
}

// watchEndpoint reports true when it ended because the plan ran out.
func watchEndpoint(p WatchPlan, ep WatchEndpoint, ev WatchEvents, stop <-chan struct{}) bool {
	say := func(s string) {
		if ev.State != nil {
			ev.State(ep.Addr, s)
		}
	}
	wait := watchRetryMin
	for {
		select {
		case <-stop:
			return false
		default:
		}
		hints, ok := p.hintsAt(time.Now())
		if !ok {
			say("expired")
			return true
		}
		// A session lives until the set of epochs to park changes: the
		// moment the next bucket joins, or the rollover itself.
		life := untilNextBucket(time.Now())
		if life > watchNextBucketLead {
			life -= watchNextBucketLead
		}
		sessionStop := make(chan struct{})
		timer := time.AfterFunc(life+time.Second, func() { close(sessionStop) })
		go func() {
			select {
			case <-stop:
				timer.Stop()
				select {
				case <-sessionStop:
				default:
					close(sessionStop)
				}
			case <-sessionStop:
			}
		}()
		parked := watchSession(ep, hints, sessionStop, ev, say)
		timer.Stop()
		select {
		case <-sessionStop:
		default:
			close(sessionStop)
		}
		select {
		case <-stop:
			return false
		default:
		}
		if parked {
			wait = watchRetryMin // it worked; whatever ended it is not a pattern yet
		} else if wait < watchRetryMax {
			wait *= 2
			if wait > watchRetryMax {
				wait = watchRetryMax
			}
		}
		say("retrying")
		// ±25 %: a relay restart must not bring every watcher back in step.
		d := wait - wait/4 + time.Duration(rand.Int63n(int64(wait/2)+1))
		select {
		case <-stop:
			return false
		case <-time.After(d):
		}
	}
}

// watchSession runs one park and reports whether the relay ever took it.
func watchSession(ep WatchEndpoint, hints [][]byte, stop <-chan struct{},
	ev WatchEvents, say func(string)) (parked bool) {
	var c *relay.Client
	var err error
	if len(ep.Pins) > 0 {
		c, err = relay.DialClientPinned(ep.Addr, pinVerifier(ep.Addr, ep.Pins))
	} else {
		c, err = relay.DialClient(ep.Addr)
	}
	if err != nil {
		return false
	}
	defer c.Close()
	c.OnParked = func() { parked = true; say("parked") }
	_ = c.Listen(hints, stop, func([]byte) {
		if ev.Mail != nil {
			ev.Mail()
		}
	})
	return parked
}
