// AR-1b's central invariant, and the one the whole wave is judged on:
//
//	Opening an existing journal publishes NOT ONE historical notification,
//	and the next new event publishes exactly one.
//
// The hazard is real and specific. Space.OnAbsorb fires for every absorbed
// event, and `AttachLog` replays the WHOLE log through it at open — so a
// notification plane wired there naively turns a first run over a large
// journal into one "new message" per event ever written.
//
// The defence here is structural, not a filter: the sink is ARMED separately,
// and a host cannot arm it until Open has returned. There is no id set to get
// right and no frontier to forget to persist for THIS hazard. (The durable
// presentation frontier and the bounded id set still belong to the coordinator
// — they defend against reconnect and process restart, which are different
// hazards and get their own tests on the Android side.)
package node

import (
	"fmt"
	"sync"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestOpeningAJournalNotifiesNothingAndTheNextEventNotifiesOnce(t *testing.T) {
	dir := t.TempDir()

	// The number is deliberately small. The invariant is structural — history
	// cannot reach a sink that does not exist yet — so it holds identically at
	// 300 and at 16 000, and a three-minute unit test would only make people
	// run it less.
	const history = 300

	rt := openRuntime(t, dir, "author")
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < history; i++ {
		if _, err := rt.Say(tid, fmt.Sprintf("historical %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rt.Close()

	// Reopen: every one of those events is replayed through the absorb funnel.
	rt2 := openRuntime(t, dir, "author")
	defer rt2.Close()

	var mu sync.Mutex
	var got []NotificationCandidate
	rt2.ArmNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("%d historical notifications after reopening a %d-event journal — "+
			"the plane is seeing the replay, which is the whole failure this "+
			"invariant exists to prevent", n, history)
	}

	// And now the next new event, exactly once.
	eid, err := rt2.Say(tid, "the new one", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("a single new event produced %d candidates, want exactly 1", len(got))
	}
	c := got[0]
	if c.EventID != eid {
		t.Errorf("candidate carries %v, want the event that was just written (%v)", c.EventID, eid)
	}
	if c.SpaceID != tid {
		t.Errorf("candidate names space %v, want %v", c.SpaceID, tid)
	}
	if !c.AuthoredLocally {
		t.Error("an event this node wrote must be marked AuthoredLocally — a person " +
			"is not told about the thing they just did, and the host should not " +
			"have to work out who they are to know that")
	}
}

// Disarming is a supported state, not a hole. A person who refuses the
// notification permission is in an ORDINARY situation, and the core should
// stop producing candidates nobody can render rather than have the host drop
// them silently and hope.
func TestDisarmingStopsCandidatesRatherThanLeavingTheHostToDropThem(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "author")
	defer rt.Close()
	tid, err := rt.CreateSpace("Room")
	if err != nil {
		t.Fatal(err)
	}

	var seen []id.EventID
	rt.ArmNotifications(func(c NotificationCandidate) { seen = append(seen, c.EventID) })
	if _, err := rt.Say(tid, "armed", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("armed: %d candidates, want 1", len(seen))
	}

	rt.ArmNotifications(nil)
	if _, err := rt.Say(tid, "disarmed", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("disarmed: %d candidates, want the count to stay at 1", len(seen))
	}
}

// The same hazard in different clothes: a space JOINED LATER, whose history
// travels with it.
//
// The Open path is safe for a structural reason — AttachLog runs before
// attach(), so during a reopen there is no absorb funnel to reach. A join is
// the case where the plane is ALREADY armed and a whole history arrives at
// once, and it is the one that would have turned "welcome to the space" into
// a notification per message ever written in it.
//
// memory=everything on purpose: this is the configuration where history
// actually travels, so it is the configuration where the hazard is real.
func TestJoiningASpaceWithHistoryDoesNotNotifyForThatHistory(t *testing.T) {
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

	tid, err := alice.CreateSpace("Long Room")
	if err != nil {
		t.Fatal(err)
	}
	const history = 25
	for i := 0; i < history; i++ {
		if _, err := alice.Say(tid, fmt.Sprintf("before bob %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// Bob's plane is armed BEFORE he joins — a phone that has been running.
	var mu sync.Mutex
	var got []NotificationCandidate
	bob.ArmNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	})

	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqID, JoinReady)

	mu.Lock()
	historical := len(got)
	mu.Unlock()
	if historical > 0 {
		t.Errorf("joining a space with %d prior messages produced %d notifications — "+
			"a person joining a long-running room would be told about every "+
			"message ever written in it", history, historical)
	}
}
