// Coming back from a suspend: the relay must not spend the first minute
// after a phone wakes serving out a sentence it passed while asleep.
//
// THE BUG THESE PIN. A device that suspends stops Go's monotonic clock, and
// every deadline in the pool is monotonic — so `backoffUntil`, set thirty
// seconds before the screen went off, is still thirty seconds away when the
// screen comes back on, however long the night was. On top of that the pooled
// TCP connections died in silence, so the first attempts after waking fail and
// charge the breaker again. Measured on a phone as about a minute of "no
// relay" after unlocking, with nothing wrong with the network.
package node

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func TestDeviceSleptOnlyWhenTheClocksDisagree(t *testing.T) {
	cases := []struct {
		what string
		wall time.Duration
		mono time.Duration
		want bool
	}{
		{"an ordinary tick", 2 * time.Second, 2 * time.Second, false},
		// A loaded machine is late on both clocks equally. Reacting to that
		// would clear the breaker every time the phone was merely busy.
		{"a very late tick, both clocks agree", 90 * time.Second, 90 * time.Second, false},
		{"a small skew, under the threshold", 9 * time.Second, 2 * time.Second, false},
		{"a short sleep", 2*time.Minute + 2*time.Second, 2 * time.Second, true},
		{"overnight", 9 * time.Hour, 2 * time.Second, true},
		// The wall clock stepping BACKWARD (an NTP correction) must not read
		// as a sleep — that direction says nothing about suspension.
		{"the wall clock was set back", -time.Hour, 2 * time.Second, false},
	}
	for _, c := range cases {
		if got := deviceSlept(c.wall, c.mono); got != c.want {
			t.Errorf("%s: deviceSlept(wall=%v, mono=%v) = %v, want %v",
				c.what, c.wall, c.mono, got, c.want)
		}
	}
}

func TestWakeDropsTheSentenceAndTheDeadSocket(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	dials := 0
	p := newRelayPool(func(a string) (*relay.Client, error) {
		dials++
		return relay.DialClient(a)
	})
	defer p.closeAll()

	// One good operation, so there is a live pooled connection to lose.
	c, release, err := p.Control(addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Time(); err != nil {
		t.Fatal(err)
	}
	release(nil)
	if dials != 1 {
		t.Fatalf("setup: wanted one dial, got %d", dials)
	}

	// Now put the endpoint where a suspend leaves it: charged and waiting.
	pe := p.peer(addr)
	pe.mu.Lock()
	pe.failures = poolUnhealthyAfter
	pe.attempt = 4
	pe.backoffUntil = time.Now().Add(2 * time.Minute)
	pe.localOutage = true
	pe.mu.Unlock()

	if _, _, err := p.Control(addr); err == nil {
		t.Fatal("a charged endpoint should refuse before dialling — the setup is not testing anything")
	}

	p.wake()

	pe.mu.Lock()
	failures, attempt, outage := pe.failures, pe.attempt, pe.localOutage
	waiting := !pe.backoffUntil.IsZero()
	pe.mu.Unlock()
	if failures != 0 || attempt != 0 || waiting || outage {
		t.Errorf("wake left the sentence standing: failures=%d attempt=%d waiting=%v localOutage=%v",
			failures, attempt, waiting, outage)
	}

	// And the socket is gone, so the next operation redials rather than
	// writing into a connection that only looks open.
	c2, release2, err := p.Control(addr)
	if err != nil {
		t.Fatalf("after waking, the relay must be tried again: %v", err)
	}
	if _, _, err := c2.Time(); err != nil {
		t.Fatal(err)
	}
	release2(nil)
	if dials != 2 {
		t.Errorf("wanted a fresh dial after waking, got %d dials in total", dials)
	}
}

// A pin mismatch is a fact about the relay's IDENTITY. Sleeping did not change
// it and waking must not clear it — this is the one thing wake leaves alone.
func TestWakeDoesNotForgiveAnUntrustedRelay(t *testing.T) {
	p := newRelayPool(func(a string) (*relay.Client, error) {
		return nil, errors.New("should not be dialled")
	})
	defer p.closeAll()

	pe := p.peer("198.51.100.7:7411")
	pe.mu.Lock()
	pe.untrusted = true
	pe.failures = 9
	pe.backoffUntil = time.Now().Add(time.Hour)
	pe.mu.Unlock()

	p.wake()

	pe.mu.Lock()
	untrusted, failures := pe.untrusted, pe.failures
	pe.mu.Unlock()
	if !untrusted {
		t.Error("waking cleared a pin mismatch — identity is not a timing fact")
	}
	if failures == 0 {
		t.Error("an untrusted endpoint was rehabilitated by a sleep")
	}
}
