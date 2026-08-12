// RR-2: the relay connection pool — lanes, health, backoff — and the
// batched ingress drain over a real relay.
package node

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func TestPoolReusesTheConnection(t *testing.T) {
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

	for i := 0; i < 5; i++ {
		c, release, err := p.Control(addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := c.Time(); err != nil {
			t.Fatal(err)
		}
		release(nil)
	}
	if dials != 1 {
		t.Fatalf("5 operations cost %d dials — the pool is not pooling", dials)
	}
}

func TestLanesAreIndependentConnections(t *testing.T) {
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

	// Hold the CONTROL lane and use the BULK lane concurrently — the bulk
	// op must not wait for control's release.
	cc, creleaseC, err := p.Control(addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = cc
	done := make(chan error, 1)
	go func() {
		bc, releaseB, err := p.Bulk(addr)
		if err != nil {
			done <- err
			return
		}
		_, _, err = bc.Time()
		releaseB(err)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bulk lane blocked behind a held control lane")
	}
	creleaseC(nil)
	if dials != 2 {
		t.Fatalf("expected exactly 2 connections (one per lane), got %d", dials)
	}
}

func TestPoolBackoffScheduleIsBoundedAndJittered(t *testing.T) {
	// Deterministic rng at the extremes.
	lo := func() float64 { return 0 }
	hi := func() float64 { return 0.999999 }
	windows := [][2]time.Duration{
		{0, 5 * time.Second},
		{5 * time.Second, 15 * time.Second},
		{15 * time.Second, 45 * time.Second},
		{45 * time.Second, 120 * time.Second},
		{45 * time.Second, 120 * time.Second}, // clamped past the ladder
	}
	for attempt, w := range windows {
		if got := poolBackoff(attempt, lo); got < w[0] || got > w[1] {
			t.Fatalf("attempt %d lo: %v outside [%v,%v]", attempt, got, w[0], w[1])
		}
		if got := poolBackoff(attempt, hi); got < w[0] || got > w[1] {
			t.Fatalf("attempt %d hi: %v outside [%v,%v]", attempt, got, w[0], w[1])
		}
	}
}

func TestConsecutiveFailuresCoolTheRelayDown(t *testing.T) {
	p := newRelayPool(func(a string) (*relay.Client, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	defer p.closeAll()

	for i := 0; i < poolUnhealthyAfter; i++ {
		if _, _, err := p.Control("198.51.100.1:7411"); err == nil {
			t.Fatal("a refused dial succeeded?")
		}
	}
	if h := p.health("198.51.100.1:7411"); h != "offline" {
		t.Fatalf("after %d failures health is %q, want offline", poolUnhealthyAfter, h)
	}
	if _, _, err := p.Control("198.51.100.1:7411"); !errors.Is(err, errRelayCoolingDown) {
		t.Fatalf("a cooling relay was dialed anyway: %v", err)
	}
}

func TestUntrustedIsALatchNotACooldown(t *testing.T) {
	p := newRelayPool(func(a string) (*relay.Client, error) {
		return nil, ErrRelayUntrusted{Endpoint: a, Got: "EVIL"}
	})
	defer p.closeAll()

	_, _, err := p.Control("relay.example.org:7411")
	var untrusted ErrRelayUntrusted
	if !errors.As(err, &untrusted) {
		t.Fatalf("expected untrusted, got %v", err)
	}
	// No amount of waiting retries it: the latch holds until reset.
	if h := p.health("relay.example.org:7411"); h != "untrusted" {
		t.Fatalf("health %q, want untrusted", h)
	}
	if _, _, err := p.Control("relay.example.org:7411"); err == nil {
		t.Fatal("an untrusted relay was re-dialed automatically")
	}
	p.resetTrust("relay.example.org:7411")
	if h := p.health("relay.example.org:7411"); h != "healthy" {
		t.Fatalf("after explicit reset health is %q", h)
	}
}

func TestAppLevelErrorsDoNotPoisonTheConnection(t *testing.T) {
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

	c, release, err := p.Control(addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c
	// Release with a routine node-level sentinel: the connection survives
	// and health does not move.
	release(ErrNoProjection)
	if h := p.health(addr); h != "healthy" {
		t.Fatalf("a routine sentinel moved health to %q", h)
	}
	c2, release2, err := p.Control(addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c2.Time(); err != nil {
		t.Fatal(err)
	}
	release2(nil)
	if dials != 1 {
		t.Fatalf("a routine sentinel cost a redial (%d dials)", dials)
	}
}
