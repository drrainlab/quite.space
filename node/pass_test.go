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
// automatically, and Bob ends up a real member. The space is
// private_history, so history stays sealed: Bob reads only what came after
// acceptance (ADR-012 invariant 5, amended — see TestPassHistoryPolicy for
// the memory=everything path).
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

	ch := terminals.DefaultCharacter("campfire")
	ch.Memory = "private_history"
	tid, err := alice.CreateSpaceWithCharacter("Back Room", ch)
	if err != nil {
		t.Fatal(err)
	}
	// A message posted BEFORE Bob joins must never become readable to him.
	if _, err := alice.Say(tid, "secret said before Bob arrived", SayOptions{}); err != nil {
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
	if _, ok := bob.spaceForTest(tid); ok {
		t.Fatal("Bob has the space while still only pending — pass leaked access")
	}

	// Alice's device confirms automatically over the rendezvous.
	space := waitJoin(t, bob, reqID, JoinReady)
	if space != tid.Hex() {
		t.Fatalf("joined the wrong space: %s", space)
	}
	if _, ok := bob.spaceForTest(tid); !ok {
		t.Fatal("Bob ready but space not open")
	}

	// Bob is now a real member on Alice's side.
	sA, _ := alice.spaceForTest(tid)
	if _, ok := sA.Members()[bob.Device.ID]; !ok {
		t.Fatal("Bob's device is not in the space membership")
	}

	// The canonical membership event is in Alice's log.
	if !hasMemberAdded(sA) {
		t.Fatal("no membership.member_added.v1 event in the log")
	}

	// History starts at acceptance: Alice posts AFTER the join, pushes to the
	// relay, Bob pulls and can read it (proves Bob holds the working epoch).
	if _, err := alice.Say(tid, "welcome Bob", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	sB, _ := bob.spaceForTest(tid)
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

// TestPassHistoryPolicy: a memory=everything space wraps past epoch keys
// into the SEALED acceptance (never into the pass itself), so a newcomer
// sees the room's memory — the promise "this place remembers everything"
// holds for pass joiners too (ADR-012 invariant 5, amended LR-4).
func TestPassHistoryPolicy(t *testing.T) {
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

	tid, err := alice.CreateSpace("Listening Room") // default memory: everything
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "the first listen happened here", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqID, JoinReady)

	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	sB, _ := bob.spaceForTest(tid)
	var seen bool
	for _, m := range sB.State.Messages() {
		if m.Text == "the first listen happened here" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("memory=everything must let a pass joiner read the room's history")
	}
	if sB.Undecryptable != 0 {
		t.Fatalf("no event should stay sealed under memory=everything, got %d", sB.Undecryptable)
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
	if _, ok := carol.spaceForTest(tid); ok {
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
