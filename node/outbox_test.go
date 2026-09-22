package node

// LT-3: the sender's own lane. A word leaves while the cycle is busy, a
// failed push retries on the outbox's clock, and a failed background
// cycle re-arms sooner than the next tick.

import (
	"fmt"
	"github.com/drrainlab/quiet_places/protocol/id"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAWordLeavesWhileTheCycleIsBusy(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()

	// Hold alice's cycle busy — the state of a node reading a catalog of
	// public spaces on a slow uplink. Released before Close (defer order).
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	rs := alice.relaySync
	rs.mu.Lock()
	rs.beforeCycle = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	rs.mu.Unlock()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("alice's cycle never came round")
	}

	// The word is said while the cycle is stuck. It must reach bob, whose
	// own loop reads on its cadence — without alice's cycle ever finishing.
	if _, err := alice.Say(tid, "мимо очереди", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for countMsg(t, bob, tid, "мимо очереди") < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the word waited for the busy cycle — the outbox did not carry it")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestAFailedPushRetriesOnTheOutboxClock(t *testing.T) {
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, 5 * time.Second}, {1, 5 * time.Second}, {2, 10 * time.Second},
		{3, 20 * time.Second}, {4, 40 * time.Second}, {5, 60 * time.Second},
		{40, 60 * time.Second},
	}
	for _, c := range cases {
		if got := outboxBackoff(c.streak); got != c.want {
			t.Fatalf("outboxBackoff(%d) = %s, want %s", c.streak, got, c.want)
		}
	}
}

func TestAFailedBackgroundCycleDoesNotWaitForTheNextTick(t *testing.T) {
	every := 180 * time.Second
	cases := []struct {
		streak int
		fg     bool
		want   time.Duration
	}{
		{0, false, every},            // nothing failed: the cadence
		{1, false, 15 * time.Second}, // first retry in fifteen seconds
		{2, false, 30 * time.Second}, // then thirty
		{3, false, 60 * time.Second}, // then a minute
		{6, false, every},            // never beyond the cadence
		{3, true, 180 * time.Second}, // foreground: the cadence already is the retry
	}
	for _, c := range cases {
		if got := syncWaitAfter(every, c.streak, c.fg); got != c.want {
			t.Fatalf("syncWaitAfter(%s, %d, %v) = %s, want %s", every, c.streak, c.fg, got, c.want)
		}
	}
	if got := syncWaitAfter(2*time.Second, 3, true); got != 2*time.Second {
		t.Fatalf("a foreground failure must keep the two-second tick, got %s", got)
	}
}

// A relay's acceptance is a fact about the wire, and facts survive the
// process: before the watermark every restart put the whole history back
// on the dot, which the owner read as a node that could not send.
func TestWhatARelayTookStaysTakenAcrossARestart(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	dir := t.TempDir()
	alice := openRuntime(t, dir, "alice")
	setPersonalRelay(t, alice, addr)
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)
	tid, err := alice.CreateSpace("рестарт")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 2, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addr, "bob": addr}
	deadline := time.Now().Add(30 * time.Second)
	if _, err := bob.Say(tid, "я тут", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	for countMsg(t, alice, tid, "я тут") < 1 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("the pair never converged")
		}
	}
	// Bob stops reading: no delivery receipt can come home, so the only
	// rung alice can stand on is the relay's own acceptance.
	bob.applyRelaySync("", 0)

	if _, err := alice.Say(tid, "переживёт рестарт", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	alice.relaySyncOnce(addr)
	if got := deliveryOf(t, alice, tid, "переживёт рестарт"); got != "relayed" {
		t.Fatalf("before the restart the word shows %q, want relayed", got)
	}
	alice.Close()
	alice = openRuntime(t, dir, "alice")
	defer alice.Close()
	if got := deliveryOf(t, alice, tid, "переживёт рестарт"); got != "relayed" {
		t.Fatalf("after the restart the word shows %q — the relay's acceptance was forgotten", got)
	}
}

// laneOpen reports whether a pool lane to addr currently holds a socket.
func laneOpen(rt *Runtime, addr string, which string) bool {
	pe := rt.pool().peer(addr)
	var lane *relayLane
	switch which {
	case "bulk":
		lane = &pe.bulk
	case "outbox":
		lane = &pe.outbox
	default:
		lane = &pe.control
	}
	lane.mu.Lock()
	defer lane.mu.Unlock()
	return lane.client != nil
}

// closeLane drops a lane's socket so the test can see whether it reopens.
func closeLane(rt *Runtime, addr string, which string) {
	pe := rt.pool().peer(addr)
	lane := &pe.control
	if which == "bulk" {
		lane = &pe.bulk
	}
	lane.mu.Lock()
	if lane.client != nil {
		lane.client.Close()
		lane.client = nil
	}
	lane.mu.Unlock()
}

// growHistory says n words of about 1 KiB each and waits until bob has them.
func growHistory(t *testing.T, alice, bob *Runtime, tid id.TerminalID, n int) {
	t.Helper()
	word := strings.Repeat("история ", 128) // ~1 KiB of UTF-8
	for i := 0; i < n; i++ {
		if _, err := alice.Say(tid, fmt.Sprintf("%d %s", i, word), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 60*time.Second, "bob never caught up with the history", func() bool {
		return countMsg(t, bob, tid, "99 история") >= 1 || countMsg(t, bob, tid, fmt.Sprintf("%d история", n-1)) >= 1
	})
}

// LT-4 S1. A space whose history is past the bulk threshold still sends a
// word on the EXPRESS lane: the lane is chosen by the delta a recipient
// needs, not by the size of the whole log. (In a live process; a fresh
// process still owes each peer its history once — on bulk — until S5.)
func TestTheOutboxSendsTheDeltaOnTheExpressLane(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	growHistory(t, alice, bob, tid, 80) // ~80 KiB > bulkThreshold

	// The cycle is held shut; the bulk lane is closed so its reopening
	// would be visible.
	release := make(chan struct{})
	defer close(release)
	rs := alice.relaySync
	rs.mu.Lock()
	rs.beforeCycle = func() { <-release }
	rs.mu.Unlock()
	closeLane(alice, addr, "bulk")

	start := time.Now()
	if _, err := alice.Say(tid, "одно слово", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "the word did not arrive", func() bool {
		return countMsg(t, bob, tid, "одно слово") >= 1
	})
	took := time.Since(start)
	if laneOpen(alice, addr, "bulk") {
		t.Fatal("a 400-byte word opened the bulk lane — the lane was chosen by the log, not the delta")
	}
	if !laneOpen(alice, addr, "outbox") {
		t.Fatal("the word did not ride the outbox lane")
	}
	if took > 2*time.Second {
		t.Fatalf("the word took %v", took)
	}
	t.Logf("word on the express lane in %v with %d KiB of history behind it", took, 80)
}

// LT-4 S1. A recipient whose cursor is unknown (first contact, or a book
// lost) gets the history on the bulk lane in its own goroutine, while the
// word still leaves for everybody else on the express lane.
func TestAFirstContactDoesNotHoldTheExpressLane(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	// Bob signs no receipts: without the signed floor a forgotten cursor
	// really does owe him the whole history.
	off := false
	if err := bob.SetSettings(Settings{Relay: addr, DeliveryReceipts: &off}); err != nil {
		t.Fatal(err)
	}
	growHistory(t, alice, bob, tid, 80)

	release := make(chan struct{})
	defer close(release)
	rs := alice.relaySync
	rs.mu.Lock()
	rs.beforeCycle = func() { <-release }
	rs.mu.Unlock()
	// Bob's cursor is forgotten: the next push owes him everything.
	alice.resetOffers()
	closeLane(alice, addr, "bulk")

	start := time.Now()
	if _, err := alice.Say(tid, "ещё слово", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 15*time.Second, "the word did not arrive with the history", func() bool {
		return countMsg(t, bob, tid, "ещё слово") >= 1
	})
	if !laneOpen(alice, addr, "bulk") {
		t.Fatal("a full history did not ride the bulk lane")
	}
	t.Logf("history and word in %v; bulk lane open: %v", time.Since(start), laneOpen(alice, addr, "bulk"))
}

// LT-4 S3. The cycle collects this device's own mail FIRST, then pushes,
// then reads the public world; the historical ingresses come after the
// reading. Pinned by the phase probe rather than inferred from timing.
func TestThePersonalPullRunsBeforeThePush(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	var mu sync.Mutex
	var phases []string
	rs := alice.relaySync
	rs.mu.Lock()
	rs.phaseProbe = func(p string) { mu.Lock(); phases = append(phases, p); mu.Unlock() }
	rs.mu.Unlock()
	if _, err := alice.Say(tid, "порядок", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	alice.relaySyncOnce(addr)
	mu.Lock()
	got := append([]string(nil), phases...)
	mu.Unlock()
	// The probe may have seen earlier background cycles; take the last four.
	if len(got) < 4 {
		t.Fatalf("phases = %v", got)
	}
	last := got[len(got)-4:]
	want := []string{"pull", "push", "public", "historical"}
	for i := range want {
		if last[i] != want[i] {
			t.Fatalf("cycle order = %v, want %v", last, want)
		}
	}
}
