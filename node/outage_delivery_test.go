package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// The reported scenario, exactly: a post written while the relay was down.
//
// Two nodes that have already spoken to each other, so addressing is not in
// question. The relay dies. Somebody posts anyway — locally that always
// works. The relay comes back on the same address, the author restarts their
// client, and the post must reach the other side without anybody doing
// anything further.
func TestAPostWrittenDuringAnOutageArrivesWhenTheRelayReturns(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	dirA, dirB := t.TempDir(), t.TempDir()
	alice := openRuntime(t, dirA, "alice")
	bob := openRuntime(t, dirB, "bob")

	tid, err := alice.CreateSpace("Quiet Line")
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
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// They have spoken. Both sides know the other's device.
	if _, err := alice.Say(tid, "утро", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForArrival(t, 15*time.Second, "bob hears the first message", func() bool {
		alice.PushToRelay(addr, tid)
		bob.PullFromRelay(addr)
		for _, e := range bobEntries(t, bob, tid) {
			if e == "утро" {
				return true
			}
		}
		return false
	})

	// THE OUTAGE.
	srv.Close()

	if _, err := bob.Say(tid, "написано в темноте", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// One cycle against a dead relay: enough to prove the push is attempted
	// and fails, without paying the pool's backoff ladder in test time.
	bob.PushToRelay(addr, tid)

	// The relay returns on the same address, and bob restarts his client.
	srv2, _, err := relay.StartServer(addr, relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()
	bob.Close()
	bob = openRuntime(t, dirB, "bob")
	defer bob.Close()
	defer alice.Close()

	waitForArrival(t, 30*time.Second, "the post written during the outage reaches alice", func() bool {
		bob.PushToRelay(addr, tid)
		alice.PullFromRelay(addr)
		for _, e := range bobEntries(t, alice, tid) {
			if e == "написано в темноте" {
				return true
			}
		}
		return false
	})
}

func waitForArrival(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", what)
}
