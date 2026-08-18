package relay_test

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// TestARoundTripEndsWhenTheRelayGoesAway.
//
// A relay that stops must HANG UP, so a request in flight fails now rather
// than after its own deadline has run out over a socket that is merely
// quiet. The client already watches for a closed connection on every poll;
// what this pins is the other half of the contract — that stopping the
// server actually closes the connections it accepted. It did not: serve
// returned on stop and left the socket to the garbage collector, the peer
// never saw a FIN, and every request sat out its full deadline. Measured
// at 5.00s for a probe before the fix, 0.22s after.
//
// In the suite that was Runtime.Close waiting ten seconds per node behind
// a relayPool lane still inside such a request; in production it is what
// a graceful relay restart does to every client still attached.
func TestARoundTripEndsWhenTheRelayGoesAway(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	c, err := relay.DialClient("127.0.0.1:" + itoa(port))
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer c.Close()

	// A live probe first, so the connection is proven and the handshake is
	// behind us: what follows measures the death, not the dial.
	if _, err := c.Probe(make([]byte, 16)); err != nil {
		srv.Close()
		t.Fatalf("probe against a live relay: %v", err)
	}

	srv.Close()
	// The closed socket takes a moment to reach the reader loop; from the
	// outside that moment is invisible, so allow it and then measure.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	_, err = c.Probe(make([]byte, 16)) // asks for 5s if nobody is looking
	took := time.Since(start)
	if err == nil {
		t.Fatal("a probe of a dead relay succeeded")
	}
	if took > time.Second {
		t.Fatalf("a request over a dead connection took %v to fail — it sat out the deadline instead of noticing", took)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("the failure was reported as a timeout: %v — the connection was closed, and the error should say so", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
