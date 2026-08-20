package node

// THE STREAM 1B RELEASE GATE — the owner's scenario, verbatim from
// ADR-024, as one causal run. RED until principal convergence lands;
// v0.1.5 does not ship while this is red.
//
//	P{Phone A, Mac B} paired · Friend F on another relay
//	1-2. the MAC IS OFFLINE. 3. the phone joins F's space.
//	4. the mac returns → it learns the space BY ITSELF.
//	5. F speaks → both devices hear.
//	6. the mac speaks → F admits it as the SAME principal.
//	7-8. F rotates → BOTH devices read the new epoch.
//	9. mirrored: the mac joins somewhere → the phone converges.
//	10. revoke the phone → no new grants/epochs reach it; the mac lives.
//
// The offline window in step 2 is load-bearing: Quiet Spaces is
// local-first, and "both happened to be online" does not count as
// convergence. Delivery is expected to be HELD and re-offered until the
// sibling is OBSERVED inside the space (ADR-023 / ADR-024), which is
// exactly what the window exercises.

import (
	"testing"
	"time"
)

// convergeTick advances every open runtime one gentle cycle.
func convergeTick(nodes map[string]*Runtime, addrs map[string]string) {
	for name, rt := range nodes {
		if rt != nil {
			rt.relaySyncOnce(addrs[name])
		}
	}
	time.Sleep(700 * time.Millisecond)
}

func TestOnePrincipalManyDevices(t *testing.T) {
	srvA, addrA := startRelay(t) // the friend's relay
	defer srvA.Close()
	srvB, addrB := startRelay(t) // the person's relay
	defer srvB.Close()
	_ = srvA
	_ = srvB
	now := uint64(time.Now().Unix())

	friend := openRuntime(t, t.TempDir(), "friend")
	defer friend.Close()
	setPersonalRelay(t, friend, addrA)

	macDir := t.TempDir()
	mac := openRuntime(t, macDir, "gleb") // the FIRST device: holds the root
	setPersonalRelay(t, mac, addrB)
	phone := pairChild(t, mac, now)
	defer phone.Close()
	setPersonalRelay(t, phone, addrB)

	nodes := map[string]*Runtime{"friend": friend, "mac": mac, "phone": phone}
	addrs := map[string]string{"friend": addrA, "mac": addrB, "phone": addrB}

	// ── 2. THE MAC GOES OFFLINE before anything happens. ────────────────
	mac.Close()
	nodes["mac"] = nil

	// ── 3. The phone joins the friend's space (the quick-link ending is
	// JoinByPass; the pass flow is the same terminal machinery and is
	// deterministic here). ──────────────────────────────────────────────
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
	if _, err := phone.Say(tid, "я вошёл с телефона", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// A few cycles while the mac is away: the grant for it must be HELD,
	// not lost, and nothing may panic for lack of a sibling.
	for i := 0; i < 3; i++ {
		convergeTick(nodes, addrs)
	}

	// ── 4. The mac returns and must learn the space BY ITSELF: no second
	// pairing, no second link, no invite from F. ────────────────────────
	mac = openRuntime(t, macDir, "gleb")
	defer mac.Close()
	setPersonalRelay(t, mac, addrB)
	nodes["mac"] = mac

	deadline := time.Now().Add(60 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := mac.spaceForTest(tid); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("step 4: the mac never learned the space its sibling joined — " +
				"the grant was lost or never held for the offline window")
		}
	}

	// ── 5. F speaks: both devices hear. ─────────────────────────────────
	if _, err := friend.Say(tid, "привет вам обоим", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(60 * time.Second)
	for countMsg(t, phone, tid, "привет вам обоим") == 0 ||
		countMsg(t, mac, tid, "привет вам обоим") == 0 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatalf("step 5: F's message reached phone=%d mac=%d — a converged sibling must hear",
				countMsg(t, phone, tid, "привет вам обоим"), countMsg(t, mac, tid, "привет вам обоим"))
		}
	}

	// ── 6. The mac speaks: F admits the frame (the cert chain names the
	// same principal — a rejected or held frame here means the sibling
	// arrived as a stranger). ───────────────────────────────────────────
	if _, err := mac.Say(tid, "а это я с мака", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(60 * time.Second)
	for countMsg(t, friend, tid, "а это я с мака") == 0 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("step 6: F never admitted the mac's message — the sibling is not the principal to F")
		}
	}

	// ── 7-8. F rotates (inviting a third device is the ordinary rotation
	// trigger), then speaks: BOTH siblings must read the new epoch. This
	// is the latent blocker made a gate: without owner-side expansion the
	// mac was never in F's wrap list and goes deaf exactly here. ────────
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	invite, err := friend.MintInvite(tid, carol.Device.ID, carol.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := carol.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := friend.Say(tid, "после ротации", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(60 * time.Second)
	for countMsg(t, phone, tid, "после ротации") == 0 ||
		countMsg(t, mac, tid, "после ротации") == 0 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatalf("step 8: after RotateEpoch phone=%d mac=%d — a sibling went deaf on rotation",
				countMsg(t, phone, tid, "после ротации"), countMsg(t, mac, tid, "после ротации"))
		}
	}

	// ── 9. Mirrored: the MAC joins a second friend; the PHONE converges. ─
	friend2 := openRuntime(t, t.TempDir(), "friend2")
	defer friend2.Close()
	setPersonalRelay(t, friend2, addrA)
	nodes["friend2"], addrs["friend2"] = friend2, addrA
	tid2, err := friend2.CreateSpace("комната F2")
	if err != nil {
		t.Fatal(err)
	}
	pass2, err := friend2.MintPass(tid2, 2, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req2, err := mac.JoinByPass(pass2.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, mac, req2, JoinReady)
	deadline = time.Now().Add(60 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if _, ok := phone.spaceForTest(tid2); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("step 9: the phone never learned the space the mac joined — convergence is one-directional")
		}
	}

	// ── 10. Revoke the phone from the mac (the root device). New grants
	// and epochs stop reaching it; the mac keeps living. ────────────────
	if err := mac.RevokeDevice(phone.Device.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		convergeTick(nodes, addrs)
	}
	friend3 := openRuntime(t, t.TempDir(), "friend3")
	defer friend3.Close()
	setPersonalRelay(t, friend3, addrA)
	nodes["friend3"], addrs["friend3"] = friend3, addrA
	tid3, err := friend3.CreateSpace("комната F3")
	if err != nil {
		t.Fatal(err)
	}
	pass3, err := friend3.MintPass(tid3, 2, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req3, err := mac.JoinByPass(pass3.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, mac, req3, JoinReady)
	// Give convergence every chance it does NOT deserve: the revoked
	// phone must never install this space.
	for i := 0; i < 8; i++ {
		convergeTick(nodes, addrs)
	}
	if _, ok := phone.spaceForTest(tid3); ok {
		t.Fatal("step 10: a REVOKED device installed a new grant")
	}
	// …while the mac itself is fine in the new room.
	if _, err := mac.Say(tid3, "мак живёт", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(45 * time.Second)
	for countMsg(t, friend3, tid3, "мак живёт") == 0 {
		convergeTick(nodes, addrs)
		if time.Now().After(deadline) {
			t.Fatal("step 10: revoking the phone broke the mac")
		}
	}
}
