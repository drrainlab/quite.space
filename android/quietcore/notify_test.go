// AR-1b.2's Go half: what crosses into Java, and what must not.
//
// node/notify_test.go already proves the core's structural invariant — a
// journal replayed at open cannot notify, because there is nowhere to notify
// to. Position alone cannot break that here: the replays happen INSIDE
// node.Open, so no arrangement of calls in this file can arm before them.
//
// What CAN break it at this boundary is the tempting design: a queue that
// holds candidates while the plane is disarmed and flushes them when a host
// arrives. That is exactly the shape a "don't lose anything" instinct
// produces, it looks like reliability, and it turns a first run over a large
// journal into one notification per event ever written. So the first test
// arms BEFORE Start — the ordinary Android ordering, and the one a buffering
// plane would fail — and the queue here drops rather than accumulates.
package quietcore

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/node"
)

// recordingSink stands in for the Java side. It is deliberately slow-safe:
// the pump calls it on a Go goroutine, so the test's own reads are locked.
type recordingSink struct {
	mu        sync.Mutex
	got       []*Candidate
	forgotten []string
	hold      chan struct{} // when non-nil, OnCandidate blocks until closed
}

func (s *recordingSink) OnSpaceForgotten(spaceID string) {
	s.mu.Lock()
	s.forgotten = append(s.forgotten, spaceID)
	s.mu.Unlock()
}

func (s *recordingSink) OnCandidate(c *Candidate) {
	if s.hold != nil {
		<-s.hold
	}
	s.mu.Lock()
	s.got = append(s.got, c)
	s.mu.Unlock()
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func (s *recordingSink) snapshot() []*Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Candidate, len(s.got))
	copy(out, s.got)
	return out
}

// eventually polls rather than sleeps a fixed time: the pump is asynchronous
// by design, and a test that sleeps long enough to be safe is a test nobody
// runs.
func eventually(t *testing.T, want func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if want() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func TestArmingAfterOpenMeansHistoryNeverCrossesTheBoundary(t *testing.T) {
	dir := t.TempDir()

	// Seeded through node directly: the binding has no Say, and reaching for
	// one would widen a surface whose narrowness is the point.
	const history = 200
	seed, err := node.Open(dir, []byte("ar1b-binding-passphrase"), "author")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := seed.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < history; i++ {
		if _, err := seed.Say(tid, fmt.Sprintf("historical %d", i), node.SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	seed.Close()

	sink := &recordingSink{}

	// Armed BEFORE Start — the ordinary Android case, where the host installs
	// its sink at Application scope and the core opens later. This is the
	// ordering that would break a filter-based defence: the sink exists for
	// the whole replay, and only the arming POSITION keeps history out.
	ArmNotifications(sink)
	t.Cleanup(func() {
		DisarmNotifications()
		_ = Stop()
	})

	if err := Start(dir, "ar1b-binding-passphrase", "author", false); err != nil {
		t.Fatal(err)
	}

	// The pump is asynchronous, so "nothing arrived" needs a window in which
	// it could have. Asserting immediately would pass against a plane that
	// notifies history one millisecond later.
	time.Sleep(150 * time.Millisecond)
	if n := sink.count(); n != 0 {
		t.Fatalf("replaying %d historical events produced %d notifications, want 0", history, n)
	}

	// And the other half of the claim, without which "no notifications" is
	// also satisfied by a plane that is simply broken.
	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	if r == nil {
		t.Fatal("runtime is not alive after Start")
	}
	if _, err := r.Say(tid, "the new one", node.SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool { return sink.count() == 1 }) {
		t.Fatalf("after one new event: %d candidates, want exactly 1", sink.count())
	}

	got := sink.snapshot()[0]
	if got.SpaceID != tid.Hex() {
		t.Fatalf("space id %q, want %q", got.SpaceID, tid.Hex())
	}
	if !got.AuthoredLocally {
		t.Fatal("our own event did not arrive marked AuthoredLocally — the host would announce the person to themselves")
	}
	if got.EventID == "" || got.Device == "" || got.Schema == "" {
		t.Fatalf("candidate is missing fields a host needs: %+v", got)
	}
}

func TestDisarmingStopsCandidatesAndStartReArmsTheSameSink(t *testing.T) {
	dir := t.TempDir()
	sink := &recordingSink{}
	ArmNotifications(sink)
	t.Cleanup(func() {
		DisarmNotifications()
		_ = Stop()
	})

	if err := Start(dir, "ar1b-disarm-passphrase", "author", false); err != nil {
		t.Fatal(err)
	}
	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	tid, err := r.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Say(tid, "armed", node.SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool { return sink.count() >= 1 }) {
		t.Fatal("armed sink received nothing")
	}
	before := sink.count()

	// A refused permission is an ordinary state: the core must stop producing
	// rather than have the host drop candidates it cannot render.
	DisarmNotifications()
	if _, err := r.Say(tid, "disarmed", node.SayOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := sink.count(); n != before {
		t.Fatalf("disarmed sink received %d more candidates", n-before)
	}

	// Re-arming while the core is already open must reach the LIVE runtime.
	// Nothing else exercises that path: the ordinary case arms before Start.
	ArmNotifications(sink)
	if _, err := r.Say(tid, "re-armed", node.SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool { return sink.count() > before }) {
		t.Fatal("re-arming a running core did not reach the live runtime")
	}
}

func TestASlowHostIsDroppedAndCountedRatherThanStallingTheCore(t *testing.T) {
	dir := t.TempDir()
	hold := make(chan struct{})
	sink := &recordingSink{hold: hold}
	ArmNotifications(sink)
	t.Cleanup(func() {
		close(hold)
		DisarmNotifications()
		_ = Stop()
	})

	dropped0 := notifyDropped.Load()

	if err := Start(dir, "ar1b-slow-passphrase", "author", false); err != nil {
		t.Fatal(err)
	}
	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	tid, err := r.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	// One more than the queue can hold, with the host wedged on the first —
	// so the queue fills and the emit path is forced to choose between waiting
	// and dropping. Waiting is the wrong answer: this runs inside the absorb
	// path, and a phone whose notification host is stuck must still sync.
	//
	// The assertion is on TIME, because that is the actual claim. Say returns
	// promptly or it does not.
	deadline := time.Now().Add(20 * time.Second)
	for i := 0; i < notifyQueueDepth+32; i++ {
		start := time.Now()
		if _, err := r.Say(tid, fmt.Sprintf("burst %d", i), node.SayOptions{}); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("Say %d blocked %v behind a wedged notification host", i, d)
		}
		if time.Now().After(deadline) {
			t.Fatal("the burst did not finish — the core is waiting on the host")
		}
	}

	if !eventually(t, func() bool { return notifyDropped.Load() > dropped0 }) {
		t.Fatal("the queue overflowed without counting a single drop — 'notifications went quiet' would be unanswerable")
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(Status()), &m); err != nil {
		t.Fatal(err)
	}
	if armed, _ := m["notify_armed"].(bool); !armed {
		t.Fatal("status does not report the plane as armed")
	}
	if _, ok := m["notify_dropped"]; !ok {
		t.Fatal("status carries no notify_dropped — the drop is invisible where it matters")
	}
}

// SD-0 across the boundary: forgetting a space reaches the host, IN ORDER,
// behind the candidates that were already queued for it.
//
// THE ORDER IS THE WHOLE REASON IT SHARES THE QUEUE. A host that cancelled a
// notification and then received a candidate for the same space would put the
// conversation straight back on a screen somebody had just cleared it from —
// and from outside it would look exactly like the deletion never worked.
func TestForgettingASpaceReachesTheHostAfterItsCandidates(t *testing.T) {
	dir := t.TempDir()
	sink := &recordingSink{hold: make(chan struct{})}
	ArmNotifications(sink)
	defer ArmNotifications(nil)

	if err := Start(dir, "ar1b-binding-passphrase", "author", false); err != nil {
		t.Fatal(err)
	}
	defer Stop()

	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	if r == nil {
		t.Fatal("no runtime after Start")
	}
	tid, err := r.CreateSpace("a room")
	if err != nil {
		t.Fatal(err)
	}
	// A second device's event is what produces a candidate; our own does not.
	if _, err := r.Say(tid, "one line", node.SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// The deletion queues behind whatever is already in flight.
	done := make(chan error, 1)
	go func() { done <- r.DeleteSpace(tid) }()
	time.Sleep(300 * time.Millisecond)

	sink.mu.Lock()
	early := len(sink.forgotten)
	sink.mu.Unlock()
	if early != 0 {
		t.Fatal("the deletion overtook a candidate still being handled")
	}

	close(sink.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		ok := len(sink.forgotten) == 1 && sink.forgotten[0] == tid.Hex()
		sink.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	t.Fatalf("the host was never told the space is gone: %v", sink.forgotten)
}
