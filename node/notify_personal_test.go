package node

import (
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// The core says "somebody called you" — the candidate field the Android
// side's own comment has been waiting for. A signed mention of MY principal
// marks the candidate Personal; a plain message does not, and my own words
// never do (they are not news to me, let alone personal news).
func TestACandidateIsPersonalExactlyWhenMyMentionRidesTheEvent(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, alice, addr)
	setPersonalRelay(t, bob, addr)

	tid, err := alice.CreateSpace("the yard")
	if err != nil {
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

	var mu sync.Mutex
	got := map[string]bool{} // preview text → personal
	alice.ArmNotifications(func(c NotificationCandidate) {
		mu.Lock()
		got[c.PreviewText] = c.Personal
		mu.Unlock()
	})

	if _, err := bob.Say(tid, "hello everyone", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(tid, "alice, look at this",
		SayOptions{Mentions: []id.PrincipalID{alice.PrincipalID}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bob.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = alice.PullFromRelay(addr)
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	personal, ok := got["alice, look at this"]
	if !ok || !personal {
		t.Fatalf("the mention did not arrive personal: %+v", got)
	}
	if plain, ok := got["hello everyone"]; !ok || plain {
		t.Fatalf("a plain message must not be personal: %+v", got)
	}
}
