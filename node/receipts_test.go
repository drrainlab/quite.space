package node

// DR-1 end to end: bob's device signs for holding alice's frames, and
// alice's checkmark ladder climbs — but never on a sibling's receipt,
// and never when the receiptor's switch is off.

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// deliveryOf finds the own entry containing text and returns its DR-1
// status word, or "" while the entry (or its status) is not there yet.
func deliveryOf(t *testing.T, rt *Runtime, tid id.TerminalID, text string) string {
	t.Helper()
	out := ""
	_ = rt.withSpace(tid, func(st *spaceState) error {
		for _, e := range st.space.State.Entries() {
			if e.Content.Text == nil || !strings.Contains(e.Content.Text.Text, text) {
				continue
			}
			frame, ok := st.space.Log.Get(e.ID)
			if !ok {
				continue
			}
			env, err := signal.Decode(frame)
			if err != nil || env.Device != rt.Device.ID {
				continue
			}
			out = rt.deliveryStatusLocked(tid, e.ID, env.Sequence)
		}
		return nil
	})
	return out
}

func TestAForeignDeviceReceiptLightsTheCheckmark(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)

	tid, err := alice.CreateSpace("камин")
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

	if _, err := alice.Say(tid, "чек долетит", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addr, "bob": addr}
	deadline := time.Now().Add(45 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if countMsg(t, bob, tid, "чек долетит") >= 1 &&
			deliveryOf(t, alice, tid, "чек долетит") == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bob holds the frame but alice's status is %q — the receipt never came home",
				deliveryOf(t, alice, tid, "чек долетит"))
		}
	}
}

func TestASiblingReceiptIsNotDelivered(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	mac := openRuntime(t, t.TempDir(), "alice")
	defer mac.Close()
	setPersonalRelay(t, mac, addr)
	phone := pairChild(t, mac, now)
	setPersonalRelay(t, phone, addr)

	tid, err := mac.CreateSpace("своим о своём")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mac.Say(tid, "только между нами", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	nodes := map[string]*Runtime{"mac": mac, "phone": phone}
	addrs := map[string]string{"mac": addr, "phone": addr}
	deadline := time.Now().Add(30 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if countMsg(t, phone, tid, "только между нами") >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sibling never converged — cannot test what its receipt means")
		}
	}
	// The phone has the frame; give its receipt time to land, then hold
	// the line: a sibling's receipt must never read as "delivered" — it
	// would mean "your own hand received it", which is not the question.
	for range 4 {
		convergeTick(nodes, addrs)
	}
	if got := deliveryOf(t, mac, tid, "только между нами"); got == "delivered" {
		t.Fatal("a sibling's receipt lit the delivered checkmark")
	}
}

func TestTheReceiptSwitchSilencesThisDevice(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)
	off := false
	bobSettings := bob.GetSettings()
	bobSettings.DeliveryReceipts = &off
	if err := bob.SetSettings(bobSettings); err != nil {
		t.Fatal(err)
	}

	tid, err := alice.CreateSpace("тихий уговор")
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
	if _, err := alice.Say(tid, "молчание тоже ответ", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addr, "bob": addr}
	deadline := time.Now().Add(30 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if countMsg(t, bob, tid, "молчание тоже ответ") >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bob never converged")
		}
	}
	for range 4 {
		convergeTick(nodes, addrs)
	}
	if got := deliveryOf(t, alice, tid, "молчание тоже ответ"); got == "delivered" {
		t.Fatal("bob's switch is off and alice still saw delivered")
	}
}
