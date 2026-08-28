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
