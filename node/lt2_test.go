package node

// LT-2 on the node side: a dead park retries soon and not on the ladder,
// a network change re-parks, and the diagnostics name every parked
// session with its own evidence.

import (
	"testing"
	"time"
)

func TestADeadParkRetriesSoonNotOnTheLadder(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "fay")
	defer rt.Close()
	addr := "203.0.113.7:7411"
	// Two route failures put the ladder at two minutes...
	rt.noteListenFailure(addr, false)
	rt.noteListenFailure(addr, false)
	at, bad := rt.listenBackoff(addr)
	if !bad || time.Until(at) < 90*time.Second {
		t.Fatalf("the ladder should be waiting minutes, got %s", time.Until(at))
	}
	// ...and a session that WAS parked ending resets it to seconds.
	rt.noteListenRetrySoon(addr)
	at, bad = rt.listenBackoff(addr)
	if !bad {
		t.Fatal("retry-soon must schedule, not forget")
	}
	if wait := time.Until(at); wait < 4*time.Second || wait > listenRetrySoon+listenRetrySoonJitter+time.Second {
		t.Fatalf("retry-soon waits %s, want seconds with jitter", wait)
	}
	// And a later failure starts the ladder from the bottom again.
	rt.noteListenFailure(addr, false)
	at, _ = rt.listenBackoff(addr)
	if wait := time.Until(at); wait > listenRetryMin+time.Second {
		t.Fatalf("the ladder did not restart from the bottom: %s", wait)
	}
}

func TestANetworkChangeBouncesTheListeners(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "gil")
	defer rt.Close()
	epoch := rt.listenEpoch()
	rt.SetNetwork(false, "wifi-home")
	select {
	case <-epoch:
	case <-time.After(time.Second):
		t.Fatal("the first network report must end the (network-less) epoch")
	}
	epoch = rt.listenEpoch()
	rt.SetNetwork(false, "wifi-home") // same network: nothing moves
	select {
	case <-epoch:
		t.Fatal("the same network is not a change")
	case <-time.After(100 * time.Millisecond):
	}
	rt.SetNetwork(true, "cell-1")
	select {
	case <-epoch:
	case <-time.After(time.Second):
		t.Fatal("moving to cellular must re-park")
	}
	if !rt.cellular.Load() {
		t.Fatal("the cellular bit was not kept")
	}
}

func TestDiagnosticsNameTheParkedListeners(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	rt := openRuntime(t, t.TempDir(), "hal")
	defer rt.Close()
	setPersonalRelay(t, rt, addr)
	if _, err := rt.CreateSpace("слушаю"); err != nil {
		t.Fatal(err)
	}
	// The listen manager ticks every few seconds; give it a moment.
	deadline := time.Now().Add(25 * time.Second)
	for {
		d := rt.RelayDiagnosticsSnapshot()
		if len(d.Listeners) > 0 {
			l := d.Listeners[0]
			if l.Addr != addr || l.PingEveryS <= 0 {
				t.Fatalf("listener row is not honest: %+v", l)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no parked listener surfaced in the diagnostics")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
