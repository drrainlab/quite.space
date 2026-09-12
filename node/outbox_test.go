package node

// LT-3: the sender's own lane. A word leaves while the cycle is busy, a
// failed push retries on the outbox's clock, and a failed background
// cycle re-arms sooner than the next tick.

import (
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
