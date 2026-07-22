package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// waitJoin polls a newcomer's join state until it reaches want or times out.
func waitJoin(t *testing.T, rt *Runtime, reqID string, want JoinState) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		state, space := rt.JoinStatus(reqID)
		if state == want {
			return space
		}
		time.Sleep(150 * time.Millisecond)
	}
	state, _ := rt.JoinStatus(reqID)
	t.Fatalf("join never reached %q (stuck at %q)", want, state)
	return ""
}

// TestPassLifecycle is the UI-2 acceptance path: Alice mints a Space Pass,
// Bob requests entry through the rendezvous relay, Alice's device confirms
// automatically, and Bob ends up a real member who can read messages posted
// AFTER acceptance (history starts at acceptance — ADR-012).
func TestPassLifecycle(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Back Room")
	if err != nil {
		t.Fatal(err)
	}
	// A message posted BEFORE Bob joins must never become readable to him.
	if _, err := alice.Say(tid, "secret said before Bob arrived"); err != nil {
		t.Fatal(err)
	}

	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	if info.Link == "" {
		t.Fatal("pass link is empty")
	}

	// Bob requests entry. Nothing is opened yet — pending has no access.
	reqID, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bob.Space(tid); ok {
		t.Fatal("Bob has the space while still only pending — pass leaked access")
	}

	// Alice's device confirms automatically over the rendezvous.
	space := waitJoin(t, bob, reqID, JoinReady)
	if space != tid.Hex() {
		t.Fatalf("joined the wrong space: %s", space)
	}
	if _, ok := bob.Space(tid); !ok {
		t.Fatal("Bob ready but space not open")
	}

	// Bob is now a real member on Alice's side.
	sA, _ := alice.Space(tid)
	if _, ok := sA.Members()[bob.Device.ID]; !ok {
		t.Fatal("Bob's device is not in the space membership")
	}

	// The canonical membership event is in Alice's log.
	if !hasMemberAdded(sA) {
		t.Fatal("no membership.member_added.v1 event in the log")
	}

	// History starts at acceptance: Alice posts AFTER the join, pushes to the
	// relay, Bob pulls and can read it (proves Bob holds the working epoch).
	if _, err := alice.Say(tid, "welcome Bob"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	sB, _ := bob.Space(tid)
	if sB == nil {
		t.Fatal("Bob has no replica")
	}
	var welcomed, leaked bool
	for _, m := range sB.State.Messages() {
		if m.Text == "welcome Bob" {
			welcomed = true
		}
		if m.Text == "secret said before Bob arrived" {
			leaked = true
		}
	}
	if !welcomed {
		t.Fatal("Bob cannot read the post-acceptance message — epoch not granted")
	}
	if leaked {
		t.Fatal("Bob read a pre-acceptance message — history did not start at acceptance")
	}
}

// TestPassSingleUse checks that a one-use pass admits exactly one device: a
// second request for the same pass never gets confirmed.
func TestPassSingleUse(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()

	tid, err := alice.CreateSpace("One Seat")
	if err != nil {
		t.Fatal(err)
	}
	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}

	reqB, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqB, JoinReady)

	// Carol tries the same one-use pass after it is spent.
	reqC, err := carol.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ := carol.JoinStatus(reqC); state == JoinReady {
			t.Fatal("a one-use pass admitted a second device")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, ok := carol.Space(tid); ok {
		t.Fatal("Carol opened the space off a spent pass")
	}
}

func hasMemberAdded(s *terminals.Space) bool {
	found := false
	s.Log.Replay(func(a eventlog.Applied) error {
		if a.Env.Schema == schemas.MemberAdded {
			found = true
		}
		return nil
	})
	return found
}
