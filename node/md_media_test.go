package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
)

// THE REPORTED SHAPE, EXACTLY: a friend and a person share a space over an
// internet relay; the person pairs a phone; the friend posts a photo; the
// phone tries to open it. Both sides online the whole time.
//
// Text already survives this arrangement — TestAFriendsPushReachesTheNewPhone
// pinned that — because frames ride the dead-drop push, and the friend
// learned the phone as a RECIPIENT from its certificate. Media is different:
// the background push carries manifests only, the bytes travel by
// want→answer, and answerWantsRouted answers ONLY a device whose route it
// knows (PeerRoutesFor == empty → a silent return). An invited member left
// a route behind at the invitation; a paired phone was never invited, so
// unless some other path taught the friend where the phone listens, the
// want is heard and the answer is never sent — and the person watches
// "fetching…" forever while everyone is demonstrably online.
//
// On a LAN this never shows: T6 pushes pending media straight down the live
// link, no want, no route lookup — which is exactly why the reporter's
// same-room tests all passed and the internet failed.
func TestAPairedPhoneFetchesAFriendsPhotoOverTheRelay(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	brother := openRuntime(t, t.TempDir(), "brother")
	defer brother.Close()
	setPersonalRelay(t, brother, addr)
	tid, err := brother.CreateSpace("семья")
	if err != nil {
		t.Fatal(err)
	}

	laptop := openRuntime(t, t.TempDir(), "gleb")
	defer laptop.Close()
	setPersonalRelay(t, laptop, addr)
	invite, err := brother.MintInvite(tid, laptop.Device.ID, laptop.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	// The laptop speaks once so the brother's replica has met it.
	if _, err := laptop.Say(tid, "мы тут", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := laptop.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntilMsg(t, brother, addr, tid, "мы тут")

	// The phone arrives, and the laptop's next push carries its certificate.
	phone := pairChild(t, laptop, now)
	setPersonalRelay(t, phone, addr)
	if _, _, err := laptop.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := brother.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}

	// The friend posts a photo — big enough that its bytes cannot ride the
	// manifests-only background bundle and MUST come by want→answer.
	content := randBytes(t, 200_000)
	ref := emitVisual(t, brother, tid, content, 4096)
	if _, _, err := brother.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}

	// The phone hears about the photo (the block frame arrives)…
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := phone.PullFromRelay(addr); err != nil {
			t.Fatal(err)
		}
		if _, err := phone.AssetStatus(tid, ref.PublicIDHex()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the block frame never reached the phone")
		}
		time.Sleep(150 * time.Millisecond)
	}

	// …and asks for it. Everyone is online; every loop below runs by hand so
	// nothing depends on tick luck: the phone re-offers its want, the
	// brother syncs (which is when answerWants runs), the phone drains its
	// inbox. If the bytes can travel at all, three rounds of this is plenty.
	if err := phone.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	arrived := func() bool {
		st, err := phone.AssetStatus(tid, ref.PublicIDHex())
		return err == nil && st.State == assets.StateComplete
	}
	deadline = time.Now().Add(45 * time.Second)
	for !arrived() {
		if time.Now().After(deadline) {
			st, _ := phone.AssetStatus(tid, ref.PublicIDHex())
			t.Fatalf("the photo never reached the phone over the relay: state=%q reason=%q — "+
				"both devices online, text delivered, media stuck (the reported case)",
				st.State, st.Reason)
		}
		phone.relaySyncOnce(addr)   // re-offer the want; drains too
		brother.relaySyncOnce(addr) // the holder's chance to answer
		time.Sleep(700 * time.Millisecond)
	}
}

// AND NOW WITH TWO RELAYS, which is what production actually runs: the
// person's devices sit on one official relay and the friend auto-selected
// the other. Frames survive this arrangement by construction — RT-0 routes
// each recipient's copy to that recipient's own relay. The question is
// whether a media ANSWER does: the want names the wanter's device, the
// holder looks up PeerRoutesFor(that device) and answers to eps[0] — and
// everything hangs on that route naming the relay the phone actually
// drains, not whichever endpoint the holder happened to learn it by.
func TestAPairedPhoneFetchesAcrossTwoRelays(t *testing.T) {
	srvA, addrA := startRelay(t) // the brother's relay
	defer srvA.Close()
	srvB, addrB := startRelay(t) // the person's relay
	defer srvB.Close()
	now := uint64(time.Now().Unix())

	brother := openRuntime(t, t.TempDir(), "brother")
	defer brother.Close()
	setPersonalRelay(t, brother, addrA)
	tid, err := brother.CreateSpace("семья")
	if err != nil {
		t.Fatal(err)
	}

	laptop := openRuntime(t, t.TempDir(), "gleb")
	defer laptop.Close()
	setPersonalRelay(t, laptop, addrB)
	// THE PASS FLOW, as in production: a direct MintInvite records no
	// routes on either side, and this test's first red was measuring that
	// separate hole rather than the reported one.
	pass, err := brother.MintPass(tid, 1, 24, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := laptop.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, laptop, req, JoinReady)
	if _, err := laptop.Say(tid, "мы тут", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	laptop.relaySyncOnce(addrB) // routed push: brother's copy to HIS relay
	if _, err := brother.PullFromRelay(addrA); err != nil {
		t.Fatal(err)
	}

	phone := pairChild(t, laptop, now)
	setPersonalRelay(t, phone, addrB)
	laptop.relaySyncOnce(addrB)
	if _, err := brother.PullFromRelay(addrA); err != nil {
		t.Fatal(err)
	}

	content := randBytes(t, 200_000)
	ref := emitVisual(t, brother, tid, content, 4096)
	brother.relaySyncOnce(addrA)

	// A rate-limit answer here is the relay saying "later", not the test
	// failing — the shipped budget is four collects a second and a polling
	// loop must live under it like every real node does.
	// THE LAPTOP IS ONLINE AND SYNCING, as the person's first device is in
	// life — and it turns out to be the only bridge: the probe above shows
	// the brother has no route to the phone (and no held entry — the phone
	// is not even a recipient he knows to hold for), so the phone hears
	// about the photo only because the laptop relays what it received.
	deadline := time.Now().Add(60 * time.Second)
	for {
		laptop.relaySyncOnce(addrB)
		_, _ = phone.PullFromRelay(addrB)
		if _, err := phone.AssetStatus(tid, ref.PublicIDHex()); err == nil {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the block frame never reached the phone across relays")
		}
		time.Sleep(700 * time.Millisecond)
	}

	if err := phone.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	arrived := func() bool {
		st, err := phone.AssetStatus(tid, ref.PublicIDHex())
		return err == nil && st.State == assets.StateComplete
	}
	deadline = time.Now().Add(45 * time.Second)
	for !arrived() {
		if time.Now().After(deadline) {
			st, _ := phone.AssetStatus(tid, ref.PublicIDHex())
			t.Fatalf("the photo never crossed two relays: state=%q reason=%q",
				st.State, st.Reason)
		}
		// GENTLY. relaySyncOnce collects as well as pushes, and the test
		// relay enforces the shipped 240-collects-a-minute budget; a loop
		// that hammers it gets "rate limited" and proves only its own
		// impatience (measured: 200ms of this tripped the limiter).
		phone.relaySyncOnce(addrB)   // re-offer the want, routed; drains too
		laptop.relaySyncOnce(addrB)  // the first device keeps relaying
		brother.relaySyncOnce(addrA) // the holder's chance to answer
		time.Sleep(900 * time.Millisecond)
	}
}
