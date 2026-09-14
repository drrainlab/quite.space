package node

// A relay that just took a park is reachable, whatever the pool counted
// before: the breaker lets go, the error that named nothing names the
// relay, and the loops are kicked.

import (
	"errors"
	"strings"
	"testing"
)

func TestAParkLetsTheBreakerGo(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	rt := openRuntime(t, t.TempDir(), "ivy")
	defer rt.Close()
	p := rt.pool()
	pe := p.peer(addr)
	// Three timeouts on dead sockets after a network blip — charged to
	// the relay, as they were on the owner's Mac.
	for i := 0; i < 3; i++ {
		p.noteFailure(pe, errors.New("relay: timed out waiting for reply"))
	}
	_, _, err := p.Control(addr)
	if !errors.Is(err, errRelayCoolingDown) {
		t.Fatalf("after three failures the pool should be cooling, got %v", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Fatalf("the cooling error does not name the relay: %v", err)
	}
	// The listener parks — a fresh handshake the relay acknowledged.
	rt.relayReachable(addr)
	c, release, err := p.Control(addr)
	if err != nil {
		t.Fatalf("a relay that just took a park is still refused: %v", err)
	}
	release(nil)
	if c == nil {
		t.Fatal("no client came back")
	}
	select {
	case <-rt.syncKick:
	default:
		t.Fatal("the loops were not kicked to try now")
	}
}
