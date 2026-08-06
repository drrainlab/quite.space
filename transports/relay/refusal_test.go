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
package relay

import (
	"errors"
	"strconv"
	"testing"
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

	c, err := DialClient(addrOf(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	cap1 := make([]byte, CapLen)
	if _, err := c.Collect([][]byte{cap1}); err != nil {
		t.Fatalf("the first collect should be within budget: %v", err)
	}

	_, err = c.Collect([][]byte{cap1})
	if err == nil {
		t.Fatal("the second collect should have been refused")
	}
	var re ErrRelay
	if !errors.As(err, &re) {
		t.Fatalf("a refusal must arrive as ErrRelay, got %T: %v", err, err)
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

	c, err := DialClient(addrOf(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	caps := make([][]byte, DefaultLimits().CollectMaxHints+1)
	for i := range caps {
		caps[i] = make([]byte, CapLen)
	}
	_, err = c.Collect(caps)
	if err == nil {
		t.Fatal("a request past the hint ceiling should have been refused")
	}
	var re ErrRelay
	if !errors.As(err, &re) {
		t.Fatalf("got %T: %v", err, err)
	}
	if re.Kind() != RefusalRequestShape {
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
	re := ErrRelay{Reason: "something a newer relay says"}
	if re.Kind() != RefusalUnknown {
		t.Fatalf("classified as %v — an unrecognised reason must stay unknown", re.Kind())
	}
	if re.Throttled() {
		t.Fatal("an unknown refusal is not a promise that waiting helps")
	}
}
