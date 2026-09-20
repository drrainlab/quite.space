package node

// AN-2: a CLOSED node's mailboxes can be watched with no key at all — the
// watcher learns that mail arrived, takes nothing, and the mail is still
// there, whole, for the node that opens later.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// watchedPair: alice and bob share a space on one relay; bob's directory is
// returned so he can be closed and opened again.
func watchedPair(t *testing.T, addr string) (alice, bob *Runtime, bobDir string, tidHex string) {
	t.Helper()
	alice = openRuntime(t, t.TempDir(), "alice")
	setPersonalRelay(t, alice, addr)
	bobDir = t.TempDir()
	bob = openRuntime(t, bobDir, "bob")
	setPersonalRelay(t, bob, addr)
	tid, err := alice.CreateSpace("дозор")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 2, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
	if _, err := alice.Say(tid, "первое", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(tid, "ответ", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addr, "bob": addr}
	deadline := time.Now().Add(30 * time.Second)
	for countMsg(t, bob, tid, "первое") < 1 || countMsg(t, alice, tid, "ответ") < 1 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("the pair never converged")
		}
	}
	return alice, bob, bobDir, tid.Hex()
}

func TestAClosedNodeIsWatchedWithNoKeyAndNothingIsTaken(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, bobDir, _ := watchedPair(t, addr)
	defer alice.Close()

	plan := bob.BuildWatchPlan(0)
	if len(plan.Endpoints) == 0 || len(plan.Buckets) == 0 {
		t.Fatalf("an open node on a relay must have somewhere to watch: %+v", plan)
	}
	// The file is hints and endpoints. No capability, no id, no key.
	raw, _ := json.Marshal(plan)
	for _, forbidden := range []string{"cap", "key", "secret", "pass"} {
		if strings.Contains(strings.ToLower(string(raw)), `"`+forbidden) {
			t.Fatalf("the watch plan must carry nothing but hints; found a %q field", forbidden)
		}
	}
	tid := bob.relayMailboxSpaces()[0]
	bob.Close() // the node is CLOSED from here on

	// MAIL THAT ARRIVES WHILE NOBODY WATCHES is reported the moment a watcher
	// parks (the relay's waiting bit) — the hour without a network.
	if _, err := alice.Say(tid, "пока тебя не было", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	alice.relaySyncOnce(addr)
	before := srv.Pending()
	if before == 0 {
		t.Fatal("test needs alice's message to be sitting on the relay")
	}

	mail := make(chan struct{}, 8)
	stop := make(chan struct{})
	go RunWatch(plan, WatchEvents{Mail: func() { mail <- struct{}{} }}, stop)
	select {
	case <-mail:
	case <-time.After(10 * time.Second):
		t.Fatal("mail was waiting when the watcher parked, and it heard nothing")
	}

	// MAIL THAT ARRIVES WHILE IT WATCHES rings too.
	for len(mail) > 0 {
		<-mail
	}
	if _, err := alice.Say(tid, "а это при тебе", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	alice.relaySyncOnce(addr)
	select {
	case <-mail:
	case <-time.After(10 * time.Second):
		t.Fatal("a Put landed under a parked watcher and it heard nothing")
	}
	close(stop)

	// NOTHING WAS TAKEN. The watcher has no capability, so the relay still
	// holds everything, and the node that opens later reads all of it.
	if after := srv.Pending(); after < before {
		t.Fatalf("the watcher must not drain anything: pending %d -> %d", before, after)
	}
	bob2 := openRuntime(t, bobDir, "bob")
	defer bob2.Close()
	setPersonalRelay(t, bob2, addr)
	deadline := time.Now().Add(30 * time.Second)
	for countMsg(t, bob2, tid, "пока тебя не было") < 1 || countMsg(t, bob2, tid, "а это при тебе") < 1 {
		bob2.relaySyncOnce(addr)
		if time.Now().After(deadline) {
			t.Fatal("the reopened node did not find the mail the watcher had been rung about")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestTheWatchParksOnlyTheEpochsItNeeds(t *testing.T) {
	h := func(b byte) string { return strings.Repeat(string("0123456789abcdef"[b%16]), 2*relay.HintLen) }
	// An instant in the middle of an epoch, and one five minutes before its end.
	mid := time.Unix(1000*bucketSeconds+3*3600, 0)
	late := time.Unix(1001*bucketSeconds-5*60, 0)
	p := WatchPlan{Version: watchPlanVersion, ExpiresAt: mid.Add(72 * time.Hour).Unix(),
		Buckets: map[string][]string{"999": {h(1)}, "1000": {h(2)}, "1001": {h(3)}, "1002": {h(4)}}}

	got, ok := p.hintsAt(mid)
	if !ok || len(got) != 2 {
		t.Fatalf("mid-epoch parks the current and the previous epoch only, got %d (ok=%v)", len(got), ok)
	}
	got, ok = p.hintsAt(late)
	if !ok || len(got) != 3 {
		t.Fatalf("minutes before a rollover the next epoch joins, got %d (ok=%v)", len(got), ok)
	}
	// The week is never handed over at once.
	for _, x := range got {
		if strings.HasPrefix(h(4), string("0123456789abcdef"[x[0]>>4])) && x[0]>>4 == 4 {
			t.Fatal("an epoch two ahead was parked: that links future addresses for the relay")
		}
	}
	// Run out: no hints for the current epoch, or past the expiry.
	if _, ok := p.hintsAt(time.Unix(1005*bucketSeconds, 0)); ok {
		t.Fatal("a plan with nothing for the current epoch must say it has run out")
	}
	if _, ok := p.hintsAt(time.Unix(p.ExpiresAt+1, 0)); ok {
		t.Fatal("an expired plan must say so")
	}
}
