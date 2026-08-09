// What a node does while ITS OWN network is away, and how fast it comes back.
//
// MEASURED ON A PHONE: Wi-Fi off for a minute, then on — and the screen went
// on saying the wrong thing for another one or two. Two separate rules were
// adding up, and both were reasonable about the wrong subject.
package node

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"
)

// The relay pool's breaker protects a relay that is struggling and a battery
// that is being spent on round trips. Neither is happening when the operating
// system refuses to send: that dial fails instantly, locally, with no packet.
func TestOurOwnNetworkBeingDownIsNotTheRelaysHealth(t *testing.T) {
	p := &relayPool{peers: map[string]*relayPeer{}}
	pe := p.peer("198.51.100.7:7411")

	// Ten of them — well past poolUnhealthyAfter, and past the point where
	// the ladder reaches its 45-to-120-second windows.
	down := &net.OpError{Op: "dial", Net: "tcp",
		Err: &osSyscallError{Err: syscall.ENETUNREACH}}
	for i := 0; i < 10; i++ {
		p.noteFailure(pe, down)
	}

	pe.mu.Lock()
	failures, wait := pe.failures, time.Until(pe.backoffUntil)
	pe.mu.Unlock()

	if failures != 0 {
		t.Fatalf("a local outage advanced the relay's failure ladder to %d — "+
			"the relay was never asked anything", failures)
	}
	if wait > 5*time.Second {
		t.Fatalf("after the network comes back the node waits %v before it "+
			"will even try; this is the minute somebody watched", wait)
	}
	// And the word matters: "offline" sends somebody to check a server when
	// the answer is their own Wi-Fi.
	if h := p.health("198.51.100.7:7411"); h != "no network here" {
		t.Fatalf("the diagnostics call it %q; the relay was never asked", h)
	}
}

// And the breaker still does its job for the thing it is for.
func TestARelayThatRefusesStillEarnsItsCooldown(t *testing.T) {
	p := &relayPool{peers: map[string]*relayPeer{}}
	pe := p.peer("198.51.100.8:7411")

	refused := &net.OpError{Op: "dial", Net: "tcp",
		Err: &osSyscallError{Err: syscall.ECONNREFUSED}}
	for i := 0; i < poolUnhealthyAfter; i++ {
		p.noteFailure(pe, refused)
	}
	pe.mu.Lock()
	failures, wait := pe.failures, time.Until(pe.backoffUntil)
	pe.mu.Unlock()
	if failures < poolUnhealthyAfter {
		t.Fatalf("a refusing relay did not advance the ladder: %d", failures)
	}
	if wait < 20*time.Second {
		t.Fatalf("a refusing relay bought only %v of cooldown", wait)
	}
}

func TestTheClassifierReadsWhatThePlatformActuallySays(t *testing.T) {
	for _, tc := range []struct {
		what string
		err  error
		ours bool
	}{
		{"errno through the chain", &net.OpError{Op: "dial",
			Err: &osSyscallError{Err: syscall.ENETUNREACH}}, true},
		{"host unreachable", &net.OpError{Op: "dial",
			Err: &osSyscallError{Err: syscall.EHOSTUNREACH}}, true},
		{"a platform that only gives us words",
			errors.New("dial tcp 1.2.3.4:7411: connect: network is unreachable"), true},
		{"no route", errors.New("dial tcp: no route to host"), true},
		{"the relay refused", &net.OpError{Op: "dial",
			Err: &osSyscallError{Err: syscall.ECONNREFUSED}}, false},
		{"a timeout", errors.New("dial tcp 1.2.3.4:7411: i/o timeout"), false},
		{"a TLS complaint", errors.New("tls: bad certificate"), false},
	} {
		if got := ourNetworkIsDown(tc.err); got != tc.ours {
			t.Errorf("%s: ourNetworkIsDown = %v, want %v (%v)",
				tc.what, got, tc.ours, tc.err)
		}
	}
}

var _ = fmt.Sprintf

// osSyscallError is os.SyscallError in the shape these tests need; the real
// one wants a syscall name it never reads back.
type osSyscallError struct{ Err error }

func (e *osSyscallError) Error() string { return e.Err.Error() }
func (e *osSyscallError) Unwrap() error { return e.Err }
