// A presence update must not be able to sever a conversation.
//
// WHAT HAPPENED ON REAL DEVICES, and what this pins. A phone and a laptop
// were linked through the internet relay. The first messages crossed in both
// directions; then everything the phone said stopped arriving, permanently,
// with no error on either side — the phone's relay diagnostics said healthy
// and pushed, the laptop's log simply never grew.
//
// The cause is structural, not a race. Presence is emitted as an ordinary
// event in the per-device hash chain (it takes a Sequence and a Previous),
// and it is ALSO declared NoCustody with a custody-expiry header, because
// nobody wants a relay storing stale presence. The relay pusher honours that
// declaration by skipping the frame — so the frame that every later event of
// that device chains to is never sent. The receiver's log holds seq 7, 8, 9…
// in its reorder buffer waiting for the seq 6 that is filtered out of every
// push for the rest of time.
//
// So: one presence update, and that device can never say anything else
// through a relay again.
package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestPresenceDoesNotSeverTheChainThroughARelay(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	bob := openRuntime(t, t.TempDir(), "bob")

	tid, err := alice.CreateSpace("the line")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}

	sync := func() {
		t.Helper()
		if _, _, err := alice.PushToRelay(addr, tid); err != nil {
			t.Fatal(err)
		}
		if _, err := bob.PullFromRelay(addr); err != nil {
			t.Fatal(err)
		}
	}
	says := func(want string) bool {
		t.Helper()
		s, ok := bob.spaceForTest(tid)
		if !ok {
			t.Fatal("bob has no replica of the space")
		}
		for _, m := range s.State.Messages() {
			if m.Text == want {
				return true
			}
		}
		return false
	}

	if _, err := alice.Say(tid, "before", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sync()
	if !says("before") {
		t.Fatal("the ordinary case is broken: bob never got the first message")
	}

	// The one line that used to end the conversation.
	if err := alice.SetPresence(tid, "around", 300); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "after", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sync()

	if !says("after") {
		t.Fatal("a presence update severed the chain: everything alice says " +
			"after it is stuck in bob's reorder buffer, waiting for a frame " +
			"the pusher refuses to send")
	}

	// And it stays severed — this is the part that makes it fatal rather
	// than slow. A second attempt, later, changes nothing.
	time.Sleep(50 * time.Millisecond)
	if _, err := alice.Say(tid, "later still", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sync()
	if !says("later still") {
		t.Fatal("still severed after a later message and another sync")
	}
}

// The other half of the same rule, so the fix is not simply "relay everything".
// A presence update with nothing after it is still withheld: that is the case
// ADR-015 was written for, and it is where all the airtime actually goes.
func TestPresenceAtTheTipIsStillNotRelayed(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	bob := openRuntime(t, t.TempDir(), "bob")
	tid, err := alice.CreateSpace("the line")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "hello", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	before, _, err := alice.PushToRelay(addr, tid)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing follows this one.
	if err := alice.SetPresence(tid, "around", 300); err != nil {
		t.Fatal(err)
	}
	after, _, err := alice.PushToRelay(addr, tid)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("presence at the tip was relayed: %d frames before, %d after "+
			"— the custody declaration is meant to hold while nothing depends "+
			"on the frame", before, after)
	}

	// And the moment something does depend on it, it goes.
	if _, err := alice.Say(tid, "and now this", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	third, _, err := alice.PushToRelay(addr, tid)
	if err != nil {
		t.Fatal(err)
	}
	if third != after+2 {
		t.Fatalf("expected the message AND the presence it chains to: "+
			"%d frames, was %d", third, after)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	s, ok := bob.spaceForTest(tid)
	if !ok {
		t.Fatal("bob has no replica")
	}
	var seen bool
	for _, m := range s.State.Messages() {
		if m.Text == "and now this" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("bob still cannot hear alice")
	}
}
