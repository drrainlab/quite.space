package meshtastic

import (
	"testing"
	"time"
)

// The backoff schedule is arithmetic, and arithmetic deserves a test that
// does not depend on wall-clock timing.
func TestBackoffGrowsIsCappedAndOnlyResetsOnAStableLink(t *testing.T) {
	b := Backoff{Min: time.Second, Max: 30 * time.Second, Stable: time.Minute}

	var seen []time.Duration
	for range 8 {
		seen = append(seen, b.Next())
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("backoff went backwards: %v", seen)
		}
		if seen[i] > 30*time.Second {
			t.Fatalf("backoff exceeded its cap: %v", seen)
		}
	}
	if seen[0] > 2*time.Second {
		t.Errorf("the first retry waits %v — a radio that blinked should "+
			"come back quickly", seen[0])
	}
	if seen[len(seen)-1] < 10*time.Second {
		t.Errorf("backoff barely grew: %v", seen)
	}

	// A link that connected and died again immediately is not evidence of
	// recovery. Resetting on it turns a radio stuck in a reboot loop into a
	// device that redials as fast as it can, which on a Pi with a flaky USB
	// port is exactly the failure this is here to avoid.
	before := b.Next()
	b.Succeeded(2 * time.Second)
	if after := b.Next(); after < before {
		t.Errorf("a link that lasted 2s reset the backoff: %v then %v", before, after)
	}
	b.Succeeded(5 * time.Minute)
	if after := b.Next(); after > 2*time.Second {
		t.Errorf("a link that held for five minutes did not reset the backoff: %v", after)
	}
}

func fastBackoff() Backoff {
	return Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond,
		Stable: time.Minute}
}

// The invariant everything above this type depends on: a radio being
// replaced is NOT a closed link. Report it closed and the node's pump
// goroutine exits — which is precisely the failure supervision exists to
// prevent, reintroduced one layer up.
func TestAMissingRadioIsNotAClosedLink(t *testing.T) {
	hub, err := StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	s, err := Supervise("tcp:"+hub.Addr(), Options{}, fastBackoff())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub.DropAll()
	// Catch the window where there is no device behind the link.
	deadline := time.Now().Add(5 * time.Second)
	sawGap := false
	for time.Now().Before(deadline) {
		st := s.Status()
		if st.Reconnecting {
			sawGap = true
			if closed, _ := s.Closed(); closed {
				t.Fatal("a link with no device behind it reported itself closed")
			}
			// Sending into a gap must fail rather than pretend.
			if err := s.Send([]byte("hello")); err == nil {
				t.Fatal("a send with no radio attached reported success")
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !sawGap {
		t.Skip("the reconnect was too fast to observe the gap")
	}

	// And only Close ends the link.
	if closed, _ := s.Closed(); closed {
		t.Fatal("the link ended without anyone closing it")
	}
	s.Close()
	if closed, _ := s.Closed(); !closed {
		t.Fatal("Close did not end the link")
	}
}

// Counters describe the LINK a person is watching, not whichever device
// happens to be behind it. Resetting them on every reconnect would hide
// exactly the flapping the status exists to reveal.
func TestCountersSurviveAReconnect(t *testing.T) {
	hub, err := StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	s, err := Supervise("tcp:"+hub.Addr(), Options{}, fastBackoff())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for range 3 {
		if err := s.Send([]byte("before")); err != nil {
			t.Fatal(err)
		}
	}
	before := s.Status().TX
	if before != 3 {
		t.Fatalf("TX = %d before the drop, want 3", before)
	}

	hub.DropAll()
	deadline := time.Now().Add(5 * time.Second)
	for s.Status().Reconnects == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the link never came back")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Send([]byte("after")); err != nil {
		t.Fatalf("send after reconnect: %v", err)
	}
	if got := s.Status().TX; got != 4 {
		t.Errorf("TX = %d after a reconnect, want 4: the counter was reset "+
			"and a flapping link would look idle", got)
	}
}

// A target that was never reachable is the caller's error, not something to
// retry forever behind their back.
func TestSuperviseReportsTheFirstDialFailure(t *testing.T) {
	if _, err := Supervise("tcp:127.0.0.1:1", Options{}, fastBackoff()); err == nil {
		t.Fatal("a radio that is not there reported success")
	}
	if _, err := Supervise("carrier-pigeon:/dev/bird", Options{}, fastBackoff()); err == nil {
		t.Fatal("an unknown target form reported success")
	}
}
