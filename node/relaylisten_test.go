package node

// EN-2 end to end: a backgrounded device whose poll has stretched to a
// minute still receives within seconds, because the relay rings the
// doorbell. The timing IS the assertion — arrival long before the
// background poll interval proves the push path delivered, not the poll.

import (
	"testing"
	"time"
)

func TestADarkPhoneStillHearsTheDoorbell(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	parent := openRuntime(t, t.TempDir(), "alice")
	defer parent.Close()
	setPersonalRelay(t, parent, addr)
	tid, err := parent.CreateSpace("the workshop")
	if err != nil {
		t.Fatal(err)
	}
	child := pairChild(t, parent, now)
	setPersonalRelay(t, child, addr)

	// Prove the pipe first, in the foreground.
	if _, err := parent.Say(tid, "hello while watched", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parent.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, child, addr, tid, "hello while watched")

	// The phone goes dark. Background poll interval becomes:
	//   cadence × listenedMultiplier (with a parked listener)  = 60s here
	//   cadence × backgroundMultiplier (without)               = 6s here
	// waitUntilMsg's own deadline is 30s — so an arrival inside it, with
	// the listener parked, can only have come through the doorbell.
	child.SetForeground(false)

	// Give the listener manager time to park (it wakes every 5 cadences
	// and parking is one round trip).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !child.relayListenHealthy() {
		time.Sleep(100 * time.Millisecond)
	}
	if !child.relayListenHealthy() {
		t.Fatal("the listener never parked — the doorbell was never wired")
	}
	if got := child.syncInterval(cadence); got != cadence*listenedMultiplier {
		t.Fatalf("a dark listening phone polls every %v — the whole point was stretching it", got)
	}

	start := time.Now()
	if _, err := parent.Say(tid, "the doorbell rings", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parent.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	// NO manual pull on the child: arrival must come from the notify-kick.
	arrival := time.Now().Add(20 * time.Second)
	for time.Now().Before(arrival) {
		if countMsg(t, child, tid, "the doorbell rings") >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	took := time.Since(start)
	if countMsg(t, child, tid, "the doorbell rings") == 0 {
		t.Fatal("the dark phone never heard the doorbell")
	}
	if took >= cadence*listenedMultiplier {
		t.Fatalf("arrival took %v — that is the poll speaking, not the bell", took)
	}
	t.Logf("dark-phone arrival in %v (background poll would have been %v)",
		took, cadence*listenedMultiplier)
}

// darkListener parks child's listener with the phone dark, or fails.
func darkListener(t *testing.T, child *Runtime) {
	t.Helper()
	child.SetForeground(false)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !child.relayListenHealthy() {
		time.Sleep(100 * time.Millisecond)
	}
	if !child.relayListenHealthy() {
		t.Fatal("the listener never parked")
	}
}

// THE MAIL FIRST. The ring used to do nothing but kick the sync cycle, and
// the cycle collects personal mail LAST — after every public space this
// person publishes or reads. On the owner's locked phone that was 26 s from
// the ring to the notification. Here the cycle is held shut entirely: the
// message must still arrive, because the doorbell pulls on its own lane.
func TestTheDoorbellCollectsTheMailWithoutWaitingForTheCycle(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	parent := openRuntime(t, t.TempDir(), "alice")
	defer parent.Close()
	setPersonalRelay(t, parent, addr)
	tid, err := parent.CreateSpace("the ridge")
	if err != nil {
		t.Fatal(err)
	}
	child := pairChild(t, parent, now)
	setPersonalRelay(t, child, addr)
	if _, err := parent.Say(tid, "first", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parent.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, child, addr, tid, "first")
	darkListener(t, child)

	// Every cycle from here on stands at its first line until released —
	// the stand-in for a dozen public spaces on a sleeping phone's network.
	release := make(chan struct{})
	defer close(release)
	child.relaySync.mu.Lock()
	child.relaySync.beforeCycle = func() {
		select {
		case <-release:
		case <-child.stop:
		}
	}
	child.relaySync.mu.Unlock()

	if _, err := parent.Say(tid, "bring rope", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parent.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && countMsg(t, child, tid, "bring rope") == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if countMsg(t, child, tid, "bring rope") == 0 {
		t.Fatal("the ring was heard and the mail still waited for the cycle")
	}
}

// THE DOOR RINGS TOO. A standing invite is a mailbox nobody was parked on:
// with the owner's phone dark a guest's request waited for the background
// poll (three minutes behind a parked listener) — the owner shared a link,
// locked the phone, and the guest sat at "waiting for the owner" until the
// app was opened. Dark, parked, and admitted in seconds.
func TestAGuestIsAdmittedWhileTheOwnersPhoneIsDark(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	tid, err := alice.CreateSpace("base camp")
	if err != nil {
		t.Fatal(err)
	}
	info, err := alice.MintPass(tid, 1, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	darkListener(t, alice)
	if got := alice.syncInterval(cadence); got != cadence*listenedMultiplier {
		t.Fatalf("the dark owner polls every %v — the test would prove nothing", got)
	}
	// The door poll re-arms its ticker after each pass with whatever the
	// state earns AT THAT MOMENT — and the pass right after going dark may
	// have run before the listener parked, arming the plain background
	// interval. Outlast that one, so the ticker standing when the guest
	// knocks is the long, listened one and only the ring can answer in time.
	time.Sleep(cadence*backgroundMultiplier + 2*cadence)

	start := time.Now()
	reqID, err := bob.JoinByPass(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	space := waitJoin(t, bob, reqID, JoinReady)
	if space != tid.Hex() {
		t.Fatalf("joined the wrong space: %s", space)
	}
	took := time.Since(start)
	// A THIRD of the poll, not the whole of it: measured without the ring,
	// the poll answered at 17.2 s of its 18 — inside a whole-interval bound,
	// which is how this test once passed with the fix taken out.
	if took >= cadence*listenedMultiplier/3 {
		t.Fatalf("admission took %v — that is the poll answering, not the door", took)
	}
	t.Logf("dark-owner admission in %v (door poll would have been %v)", took, cadence*listenedMultiplier)
}
