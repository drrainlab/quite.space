// The connection classifier, and the one case that cost a phone its inbound
// sync for the life of its process.
//
// FOUND BY AR-0c ON REAL HARDWARE, not by reading. A Nothing Phone (1) that
// had been backgrounded came back with `pushed 11 pulled 0` and a permanent
// `lan: connection closed`, and it never recovered — a restart was the only
// cure. The mechanism: isConnFatal decided whether a connection was dead by
// looking for the substring "use of closed", which is how the NET package
// words it, while the lan transport words it "lan: connection closed". The
// two never met, so the pool never retired the corpse and handed it back to
// every caller forever.
//
// The lesson is not "add another substring". It is that a sibling package's
// errors must be matched by IDENTITY, because a string is not a contract and
// nothing fails when it drifts. lan.ErrConnClosed is now a sentinel and this
// test is what keeps it one.
package node

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/drrainlab/quiet_places/transports/lan"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestADeadConnectionIsFatalHoweverItIsWorded(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"the lan transport's own sentinel", lan.ErrConnClosed, true},
		{"the same sentinel, wrapped", fmt.Errorf("relay put: %w", lan.ErrConnClosed), true},
		{"the net package's wording", errors.New("use of closed network connection"), true},
		{"a reply that never came", errors.New("timed out waiting for reply"), true},
		{"end of stream", io.EOF, true},

		// A relay that ANSWERED is not a broken connection, and must never
		// poison the pool or the health ladder — that distinction is the whole
		// reason isConnFatal exists rather than "err != nil".
		{"a relay refusal", relay.ErrRelay{Reason: "rate limited"}, false},
		{"a fact about content", errors.New("node: no projection yet"), false},
		{"nothing at all", nil, false},
	} {
		if got := isConnFatal(c.err); got != c.want {
			t.Errorf("%s: isConnFatal(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}
