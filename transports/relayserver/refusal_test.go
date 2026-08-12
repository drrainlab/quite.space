// RR — what a relay's refusal MEANS, asserted against the server that sends it.
//
// "The relay said no" covers three situations calling for three different
// responses: wait, fix the request, or look at the connection. Getting that
// wrong is not theoretical here — a rate limit misread as a dead socket makes
// the pool throw away a healthy connection and dial again, which is more load
// on the relay that was already asking for less.
//
// The tests drive the REAL server rather than constructing errors by hand, so
// a reason that changes wording fails here instead of silently changing a
// policy somewhere else.
package relayserver

import (
	"errors"
	"github.com/drrainlab/quiet_places/transports/relay"
	"strconv"
	"testing"
	"time"
)

func addrOf(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }

func TestARateLimitIsAScheduleNotABrokenConnection(t *testing.T) {
	limits := DefaultLimits()
	limits.CollectRatePerMin = 1
	srv, port, err := StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	c, err := relay.DialClient(addrOf(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	cap1 := make([]byte, relay.CapLen)
	if _, err := c.Collect([][]byte{cap1}); err != nil {
		t.Fatalf("the first collect should be within budget: %v", err)
	}

	_, err = c.Collect([][]byte{cap1})
	if err == nil {
		t.Fatal("the second collect should have been refused")
	}
	var re relay.ErrRelay
	if !errors.As(err, &re) {
		t.Fatalf("a refusal must arrive as relay.ErrRelay, got %T: %v", err, err)
	}
	if !re.Throttled() {
		t.Fatalf("reason %q classified as %v, want throttled — a rate limit is "+
			"a schedule, and treating it as a dead socket makes the pool dial "+
			"again, which is more load on the relay that just asked for less",
			re.Reason, re.Kind())
	}
}

func TestTooManyHintsIsARequestShapeProblemNotSomethingToRetry(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	c, err := relay.DialClient(addrOf(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	caps := make([][]byte, DefaultLimits().CollectMaxHints+1)
	for i := range caps {
		caps[i] = make([]byte, relay.CapLen)
	}
	_, err = c.Collect(caps)
	if err == nil {
		t.Fatal("a request past the hint ceiling should have been refused")
	}
	var re relay.ErrRelay
	if !errors.As(err, &re) {
		t.Fatalf("got %T: %v", err, err)
	}
	if re.Kind() != relay.RefusalRequestShape {
		t.Fatalf("reason %q classified as %v, want a request-shape problem — "+
			"asking again identically will fail identically, so it belongs in a "+
			"diagnostic rather than in a retry loop", re.Reason, re.Kind())
	}
	if re.Throttled() {
		t.Fatal("a malformed request is not something to wait out")
	}
}

// A relay from another version refusing for a reason this build does not know
// must not be forced into one of the two categories: guessing would either
// retry forever against a refusal that will never change, or discard a healthy
// connection over a message nobody here wrote.
func TestAnUnknownRefusalIsNotGuessedAt(t *testing.T) {
	re := relay.ErrRelay{Reason: "something a newer relay says"}
	if re.Kind() != relay.RefusalUnknown {
		t.Fatalf("classified as %v — an unrecognised reason must stay unknown", re.Kind())
	}
	if re.Throttled() {
		t.Fatal("an unknown refusal is not a promise that waiting helps")
	}
}

// A REFUSAL THAT WAITING WILL FIX SAYS HOW LONG TO WAIT.
//
// Without it a client has to guess, and both guesses are wrong: too eager is
// more load on a relay that just asked for less, and too patient is somebody's
// messages sitting on a relay for no reason.
//
// A DURATION, not a deadline. The two clocks have never been assumed to agree
// anywhere else in this protocol, and a timestamp would make a client with a
// skewed clock either hammer a quiet relay or sleep for hours.
func TestARateLimitSaysHowLongToWait(t *testing.T) {
	limits := DefaultLimits()
	limits.CollectRatePerMin = 1
	srv, port, err := StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	c, err := relay.DialClient(addrOf(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	cap1 := make([]byte, relay.CapLen)
	if _, err := c.Collect([][]byte{cap1}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Collect([][]byte{cap1})

	var re relay.ErrRelay
	if !errors.As(err, &re) {
		t.Fatalf("got %T: %v", err, err)
	}
	if re.RetryAfter <= 0 {
		t.Fatal("a rate limit with no wait leaves the client guessing, and both " +
			"guesses are wrong")
	}
	if re.RetryAfter > time.Minute {
		t.Fatalf("retry-after %v is longer than the limiter's own window — a "+
			"client would sit out a budget that had already refilled", re.RetryAfter)
	}
	// Never "try again now": a zero wait is the same hammering the limit
	// exists to stop.
	if re.RetryAfter < time.Second {
		t.Fatalf("retry-after %v is effectively immediate", re.RetryAfter)
	}
}

// A refusal that waiting will NOT fix must not carry a wait: it would send a
// caller to sleep and then fail identically, forever.
func TestARequestShapeRefusalCarriesNoWait(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	c, err := relay.DialClient(addrOf(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	caps := make([][]byte, DefaultLimits().CollectMaxHints+1)
	for i := range caps {
		caps[i] = make([]byte, relay.CapLen)
	}
	_, err = c.Collect(caps)

	var re relay.ErrRelay
	if !errors.As(err, &re) {
		t.Fatalf("got %T: %v", err, err)
	}
	if re.RetryAfter != 0 {
		t.Fatalf("a request-shape refusal carried a wait of %v — sleeping and "+
			"asking again identically fails identically", re.RetryAfter)
	}
}
