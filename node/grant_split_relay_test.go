package node

// Field regression (2026-09-01): a private space created on the mac
// AFTER pairing never appeared on the phone. Root cause: the courtesy
// offer sits at the GRANTOR's relay when its book holds no route for
// the sibling, and the sibling only drained its own ingresses — with
// the devices on different relays the first grant stranded forever.
// The fix: fetchGrants also knocks on every relay a certified sibling
// states in the route book (grants.go siblingIngresses).

import (
	"testing"
	"time"
)

func TestAPrivateSpaceCreatedAfterPairingReachesThePhone(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	now := uint64(time.Now().Unix())

	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addrA)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addrA)

	tid, err := mac.CreateSpace("тайная комната")
	if err != nil {
		t.Fatal(err)
	}

	nodes := map[string]*Runtime{"mac": mac, "phone": phone}
	addrs := map[string]string{"mac": addrA, "phone": addrA}
	deadline := time.Now().Add(45 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := phone.spaceForTest(tid); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("shared relay: the private space never reached the paired phone")
		}
	}
}

func TestAPrivateSpaceReachesThePhoneOnAnotherRelay(t *testing.T) {
	srvA, addrA := startRelay(t) // the mac's
	defer srvA.Close()
	srvB, addrB := startRelay(t) // the phone's
	defer srvB.Close()
	now := uint64(time.Now().Unix())

	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addrA)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addrB)

	tid, err := mac.CreateSpace("тайная комната")
	if err != nil {
		t.Fatal(err)
	}

	nodes := map[string]*Runtime{"mac": mac, "phone": phone}
	addrs := map[string]string{"mac": addrA, "phone": addrB}
	deadline := time.Now().Add(45 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := phone.spaceForTest(tid); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("split relays: the private space never reached the paired phone")
		}
	}
}

// The field shape: a shared history converged on one relay, THEN the
// phone moves relays (the person edited relay settings), THEN the mac
// creates a private space. The phone authors after the move, so its new
// ingress is a signed statement the mac can hold in its route book.
func TestAPrivateSpaceFollowsAPhoneThatMovedRelays(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	defer srvB.Close()
	now := uint64(time.Now().Unix())

	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addrA)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addrA)

	s1, err := mac.CreateSpace("общая история")
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]*Runtime{"mac": mac, "phone": phone}
	addrs := map[string]string{"mac": addrA, "phone": addrA}
	deadline := time.Now().Add(30 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := phone.spaceForTest(s1); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("baseline: the first space never converged on the shared relay")
		}
	}

	// The phone moves; speaking afterwards is what advertises the move.
	setPersonalRelay(t, phone, addrB)
	addrs["phone"] = addrB
	if _, err := phone.Say(s1, "я переехал", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	convergeTick(nodes, addrs)
	convergeTick(nodes, addrs)

	s2, err := mac.CreateSpace("тайная комната")
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(45 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := phone.spaceForTest(s2); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("after the phone moved relays, the new private space never followed it")
		}
	}
}
