package node

// LT-2 §6, narrowed, and its neighbour "I moved": a live device with no
// stated route is guessed at every official relay, and a device that
// changes relays tells its peers at once.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// pairOnRelay: alice owns a space, bob joins by pass, both on relay A,
// converged on one message from each side.
func pairOnRelay(t *testing.T, addrA string) (alice, bob *Runtime, tid id.TerminalID) {
	t.Helper()
	alice = openRuntime(t, t.TempDir(), "alice")
	setPersonalRelay(t, alice, addrA)
	bob = openRuntime(t, t.TempDir(), "bob")
	setPersonalRelay(t, bob, addrA)
	var err error
	tid, err = alice.CreateSpace("переезд")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 2, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
	if _, err := alice.Say(tid, "привет", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(tid, "и тебе", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addrA, "bob": addrA}
	deadline := time.Now().Add(30 * time.Second)
	for countMsg(t, bob, tid, "привет") < 1 || countMsg(t, alice, tid, "и тебе") < 1 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("the pair never converged on relay A")
		}
	}
	return alice, bob, tid
}

func TestAMovedDeviceTellsItsPeersWhereItListensNow(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	defer srvB.Close()
	alice, bob, tid := pairOnRelay(t, addrA)
	defer alice.Close()
	defer bob.Close()

	// Bob moves to relay B. Nothing else happens on bob's side: he reads.
	setPersonalRelay(t, bob, addrB)
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addrA, "bob": addrB}
	deadline := time.Now().Add(30 * time.Second)
	for {
		convergeTick(nodes, addrs)
		alice.mu.Lock()
		routes := alice.ks.PeerRoutes[bob.Device.ID]
		alice.mu.Unlock()
		known := false
		for _, rt := range routes {
			if rt.Endpoint == addrB && rt.Provenance != storage.RouteLegacy {
				known = true
			}
		}
		if known {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alice never learned bob's new relay; routes: %+v", routes)
		}
	}
	// And alice's next word lands on B, where bob now listens.
	if _, err := alice.Say(tid, "нашла тебя", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for countMsg(t, bob, tid, "нашла тебя") < 1 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("the message after the move never reached bob on relay B")
		}
	}
}

func TestALiveDeviceWithNoRouteIsGuessedAtEveryOfficialRelay(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	defer srvB.Close()
	alice, bob, tid := pairOnRelay(t, addrA)
	defer alice.Close()
	defer bob.Close()

	// Alice forgets everything she knows about where bob listens — the
	// state of a sender whose peer moved while its app was closed and
	// whose "I moved" it never received. Bob wrote today, so he is alive.
	alice.mu.Lock()
	delete(alice.ks.PeerRoutes, bob.Device.ID)
	alice.guessRelaysOverride = []string{addrA, addrB}
	alice.mu.Unlock()
	alice.resetOffers()
	if _, err := alice.Say(tid, "где ты", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	before := srvB.StatusSnapshot("", "").Traffic.PutsTotal
	alice.relaySyncOnce(addrA)
	if after := srvB.StatusSnapshot("", "").Traffic.PutsTotal; after == before {
		t.Fatal("no copy was guessed onto relay B, where bob may be listening")
	}
	// Bob, silently reading on B, gets it without ever having spoken again.
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addrA, "bob": addrB}
	setPersonalRelay(t, bob, addrB)
	deadline := time.Now().Add(30 * time.Second)
	for countMsg(t, bob, tid, "где ты") < 1 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("bob never received the guessed copy on relay B")
		}
	}
}

func TestAGhostGetsTheSingleCheapGuess(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	rt := openRuntime(t, t.TempDir(), "carol")
	defer rt.Close()
	setPersonalRelay(t, rt, addrA)
	rt.guessRelaysOverride = []string{addrA, "203.0.113.9:7411"}
	tid, err := rt.CreateSpace("призраки")
	if err != nil {
		t.Fatal(err)
	}
	var ghost id.DeviceID
	ghost[0] = 0x77
	rt.mu.Lock()
	st := rt.spaces[tid]
	rt.mu.Unlock()
	st.space.AddMember(ghost, [32]byte{}) // never wrote a byte: no sign of life
	if _, err := rt.Say(tid, "кто-нибудь", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	route := func(dev id.DeviceID, alive bool) ([]string, bool) {
		if alive {
			seen = append(seen, "alive")
		}
		return []string{addrA}, true
	}
	if _, _, _, _, _, err := rt.deliverSpaceRouted(tid, AssetsManifests, route, false, true, false); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatal("a device that never wrote must not count as alive")
	}
}
