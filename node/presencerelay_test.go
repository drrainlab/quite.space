// A status has to reach people.
//
// REPORTED FROM THE BETA STAND: presence appeared on the other side only when
// the sender happened to send a message afterwards. Two people on a relay
// could sit there with each other's status stuck on the sending side, and it
// looked like presence was attached to messages rather than being a thing of
// its own.
//
// It was the custody rule, over-applied. Presence declares NoCustody so that
// no relay HOLDS a stale status; the pusher read that as "never hand it over
// at all", which is a much larger claim and the reason nothing arrived until
// a message dragged it along as a chain link.
package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/trust"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestAStatusArrivesWithoutAMessageBehindIt(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

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
	if _, err := alice.Say(tid, "hello", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sync()

	// Alice sets a status and says NOTHING after it. This is the whole test.
	if err := alice.SetPresence(tid, "around", 300); err != nil {
		t.Fatal(err)
	}
	sync()

	got := presenceSeenBy(t, bob, tid, alice.Self.TerminalID)
	if !got.Known || !got.Current || got.State != "around" {
		t.Fatalf("bob sees alice as %+v — a status only travelled when a "+
			"message was sent behind it", got)
	}
}

// And the relay must not be left holding it once it is stale: that is what
// the custody declaration is actually about, and the reason this could not
// simply be handed over with everything else.
func TestAStaleStatusIsNotLeftSittingOnTheRelay(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
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
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}

	// One second of life. Bob is not listening while it is alive.
	if err := alice.SetPresence(tid, "around", 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)

	// The guarantee is that it is never HANDED OVER. (The item sits in the
	// relay's memory until somebody collects that hint — the store drops
	// expired items in place rather than sweeping them, which is its own
	// pre-existing behaviour and bounded by its quotas.)
	before := srv.Pending()
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if got := presenceSeenBy(t, bob, tid, alice.Self.TerminalID); got.Current {
		t.Fatalf("bob was told alice is %q, from a status that had expired "+
			"before he ever asked", got.State)
	}
	if after := srv.Pending(); after >= before && before > 0 {
		t.Fatalf("the stale item survived a collect: %d before, %d after",
			before, after)
	}
}

func presenceSeenBy(t *testing.T, rt *Runtime, tid id.TerminalID,
	who id.TerminalID) trust.ProjectedPresence {
	t.Helper()
	st, ok := rt.spaceForTest(tid)
	if !ok {
		t.Fatal("no replica of the space")
	}
	return st.Trust.Presence(who, uint64(time.Now().Unix()))
}
