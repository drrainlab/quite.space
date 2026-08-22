package node

import (
	"testing"
	"time"
)

// THE ROTATION SITE ADR-024 MISSED. A pass accept used to rotate blind:
// the knocker's device alone entered the wrap list, so a sibling that
// later learned the space by grant was deafened by the NEXT pass accept.
// Now the knock carries the person's certified set, the host learns it
// before admission, and the expansion runs before the mint — the whole
// person is wrapped from the first rotation on, and a second guest
// walking in through a pass (not an invite) no longer deafens the mac.
func TestPassAcceptWrapsTheWholePerson(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	now := uint64(time.Now().Unix())

	friend := openRuntime(t, t.TempDir(), "friend")
	defer friend.Close()
	setPersonalRelay(t, friend, addrA)

	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addrA)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addrA)
	nodes := map[string]*Runtime{"friend": friend, "mac": mac, "phone": phone}
	addrs := map[string]string{"friend": addrA, "mac": addrA, "phone": addrA}

	tid, err := friend.CreateSpace("комната F")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := friend.MintPass(tid, 3, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := phone.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, phone, req, JoinReady)

	// The host learned the MAC's certificate from the phone's knock, and
	// wrapped the first epoch to it — before the mac ever spoke there.
	friend.mu.Lock()
	_, knowsMac := friend.ident.certificateFor(mac.Device.ID)
	friend.mu.Unlock()
	if !knowsMac {
		t.Fatal("the host did not learn the sibling's certificate from the knock")
	}
	fsp, _ := friend.spaceForTest(tid)
	if !fsp.HasMember(mac.Device.ID) {
		t.Fatal("the pass accept rotated without wrapping the knocker's sibling")
	}

	// The mac converges through the identity plane as before.
	deadline := time.Now().Add(60 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := mac.spaceForTest(tid); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mac never learned the space its sibling joined")
		}
	}

	// A SECOND GUEST THROUGH A PASS — the rotation that used to deafen.
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	setPersonalRelay(t, carol, addrA)
	req2, err := carol.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, carol, req2, JoinReady)
	if _, err := friend.Say(tid, "после второго гостя", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(60 * time.Second)
	for countMsg(t, mac, tid, "после второго гостя") == 0 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("the mac went deaf on a pass-accept rotation — the wrap list lost the sibling")
		}
	}
}

// SIBLINGS ON DIFFERENT RELAYS, AND ONE THAT MOVED. The phone leaves the
// grant where its book says the mac listens — the ingress the freight
// snapshotted at pairing. The mac has since moved to another relay. It
// must still find the grant: every once-advertised ingress is drained for
// the identity mailbox exactly as it is for space mailboxes.
func TestSiblingsConvergeAfterARelayMove(t *testing.T) {
	srvA, addrA := startRelay(t) // the friend's
	defer srvA.Close()
	srvB, addrB := startRelay(t) // the person's, at pairing time
	defer srvB.Close()
	srvC, addrC := startRelay(t) // where the mac moves to
	defer srvC.Close()
	now := uint64(time.Now().Unix())

	friend := openRuntime(t, t.TempDir(), "friend")
	defer friend.Close()
	setPersonalRelay(t, friend, addrA)

	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addrB)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addrB)

	// The mac moves. Its book still names B as a once-advertised ingress.
	setPersonalRelay(t, mac, addrC)

	nodes := map[string]*Runtime{"friend": friend, "mac": mac, "phone": phone}
	addrs := map[string]string{"friend": addrA, "mac": addrC, "phone": addrB}

	tid, err := friend.CreateSpace("комната F")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := friend.MintPass(tid, 2, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := phone.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, phone, req, JoinReady)

	deadline := time.Now().Add(60 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := mac.spaceForTest(tid); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mac moved relays and never found the grant left at its old ingress")
		}
	}
}

// THE HOLD IS REPORTED, NOT SILENT (ADR-023). Before the sibling converges,
// the granting device names what it still owes and where it is leaving
// the offer; after convergence the list is empty.
func TestDevicesReportWhatIsStillOwed(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	now := uint64(time.Now().Unix())

	friend := openRuntime(t, t.TempDir(), "friend")
	defer friend.Close()
	setPersonalRelay(t, friend, addrA)
	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addrA)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addrA)

	tid, err := friend.CreateSpace("комната F")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := friend.MintPass(tid, 2, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := phone.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, phone, req, JoinReady)

	owed := func() []PendingGrant {
		for _, d := range phone.Devices() {
			if d.Device == mac.Device.ID.Hex() {
				return d.Pending
			}
		}
		t.Fatal("the mac is not in the phone's device list")
		return nil
	}
	before := owed()
	found := false
	for _, pg := range before {
		if pg.Space == tid.Hex() {
			found = true
			if pg.Via == "" {
				t.Fatalf("a pending grant names no relay: %+v", pg)
			}
		}
	}
	if !found {
		t.Fatalf("the space the phone just joined is not reported as owed to the mac: %+v", before)
	}

	nodes := map[string]*Runtime{"friend": friend, "mac": mac, "phone": phone}
	addrs := map[string]string{"friend": addrA, "mac": addrA, "phone": addrA}
	deadline := time.Now().Add(60 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := mac.spaceForTest(tid); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mac never converged")
		}
	}
	// The mac's certificate lands in the space log on install — the
	// phone observes it and the debt is gone.
	deadline = time.Now().Add(30 * time.Second)
	for {
		still := false
		for _, pg := range owed() {
			if pg.Space == tid.Hex() {
				still = true
			}
		}
		if !still {
			break
		}
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("the debt did not clear after the sibling was observed in the space")
		}
	}
	if got := phone.GrantRefusals(); len(got) != 0 {
		t.Fatalf("refusals on a healthy run: %v", got)
	}
}
