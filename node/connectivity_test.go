package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// setMode installs a connectivity policy.
func setMode(t *testing.T, rt *Runtime, m ConnectivityMode) {
	t.Helper()
	s := rt.GetSettings()
	s.Connectivity = Connectivity{Mode: m}
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
}

// oversized returns a frame size that no radio profile will carry.
func oversized() int { return routing.BetaOutboundCap + 1 }

// "Too large" is a property of the PAIRING of a message and a carrier, not
// of the event. Marking the event itself would strand a message that has a
// perfectly good path over the internet.
func TestTooLargeBlocksOnlyTheRadio(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	tid, err := rt.CreateSpace("Big")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	big := id.EventID{0xB1}
	rt.mu.Lock()
	_, err = rt.ledger.Enqueue(big, tid, oversized(), now)
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	in, _ := rt.ledger.Get(big)

	if in.BlockedOn(TransportRadio) != BlockTooLarge {
		t.Fatal("an oversized frame is not blocked on the radio")
	}
	for _, k := range []TransportKind{TransportRelay, TransportLAN} {
		if in.BlockedOn(k) != BlockNone {
			t.Fatalf("%v was blocked by a radio size limit", k)
		}
		if !in.EligibleOn(k) {
			t.Fatalf("%v became ineligible because of a radio limit", k)
		}
	}
	// The intent is alive, not failed.
	if in.State == IntentSettled {
		t.Fatal("an oversized frame was treated as a dead event")
	}
	if len(rt.ledger.Due(now, 10)) != 1 {
		t.Fatal("the intent stopped being tracked")
	}
}

// (1) Meshtastic only + too large: no radio attempt is eligible, the intent
// stays alive, and the projection says what is actually happening rather
// than showing a retry that cannot succeed.
func TestMeshtasticOnlyOversizedWaitsForAFasterLink(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	tid, err := rt.CreateSpace("Big")
	if err != nil {
		t.Fatal(err)
	}
	setMode(t, rt, ModeMeshtasticOnly)
	now := time.Now()
	big := id.EventID{0xB2}
	rt.mu.Lock()
	_, err = rt.ledger.Enqueue(big, tid, oversized(), now)
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	in, _ := rt.ledger.Get(big)

	if got := rt.eligibleTransports(in); len(got) != 0 {
		t.Fatalf("a transport was offered for a message that cannot use it: %v", got)
	}
	view, ok := rt.Delivery(big)
	if !ok {
		t.Fatal("no projection for a tracked message")
	}
	if view.Waiting != "faster_link" {
		t.Fatalf("the projection does not say what is happening: %+v", view)
	}
	if view.Proof != "created_local" {
		t.Fatalf("proof moved without anything being sent: %q", view.Proof)
	}
	// Still ours, still alive.
	if _, ok := rt.ledger.Get(big); !ok {
		t.Fatal("the intent was dropped instead of waiting")
	}
}

// (2) Auto + too large on radio + internet available: the SAME EventID
// leaves over the internet. No new event is authored.
func TestAutoRoutesOversizedOverInternet(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	tid, err := rt.CreateSpace("Big")
	if err != nil {
		t.Fatal(err)
	}
	setMode(t, rt, ModeAuto)
	now := time.Now()
	big := id.EventID{0xB3}
	rt.mu.Lock()
	_, err = rt.ledger.Enqueue(big, tid, oversized(), now)
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	in, _ := rt.ledger.Get(big)

	got := rt.eligibleTransports(in)
	if len(got) == 0 {
		t.Fatal("Auto offered nothing for a message the internet could carry")
	}
	for _, k := range got {
		if k == TransportRadio {
			t.Fatal("Auto offered the radio for a frame it cannot carry")
		}
	}
	// And the projection does not claim it is stuck: a path exists.
	if view, _ := rt.Delivery(big); view.Waiting != "" {
		t.Fatalf("a message with a usable path was shown as waiting: %+v", view)
	}
}

// (3) Meshtastic only → too large → the person switches to Internet only.
// The EXISTING intent becomes sendable. No event is recreated: the ledger
// tracks responsibility for an EventID, not a queue of send attempts.
func TestModeChangeMakesTheExistingIntentSendable(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	tid, err := rt.CreateSpace("Big")
	if err != nil {
		t.Fatal(err)
	}
	setMode(t, rt, ModeMeshtasticOnly)
	now := time.Now()
	big := id.EventID{0xB4}
	rt.mu.Lock()
	before, err := rt.ledger.Enqueue(big, tid, oversized(), now)
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.eligibleTransports(before); len(got) != 0 {
		t.Fatalf("setup: %v", got)
	}

	setMode(t, rt, ModeInternetOnly)

	after, ok := rt.ledger.Get(big)
	if !ok {
		t.Fatal("the intent did not survive the mode change")
	}
	if after.EventID != before.EventID {
		t.Fatal("a new event was created instead of re-attempting the old one")
	}
	if after.AttemptNo != before.AttemptNo {
		t.Fatalf("the mode change consumed an attempt: %d then %d",
			before.AttemptNo, after.AttemptNo)
	}
	got := rt.eligibleTransports(after)
	if len(got) == 0 {
		t.Fatal("the existing intent is still unsendable after the mode change")
	}
	for _, k := range got {
		if k == TransportRadio {
			t.Fatal("Internet only offered the radio")
		}
	}
	if view, _ := rt.Delivery(big); view.Waiting != "" {
		t.Fatalf("still shown as waiting after a usable path appeared: %+v", view)
	}
}

// The gate refuses BEFORE a connection is opened. "We dialled and then
// decided not to" still tells a relay operator this device is awake, on
// this address, right now — which is what someone choosing Meshtastic only
// is trying not to say.
func TestRelayGateRefusesBeforeDialling(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	tid, err := rt.CreateSpace("Quiet")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "not for the internet", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	setMode(t, rt, ModeMeshtasticOnly)

	// An address that would fail loudly if anything actually dialled it.
	const unroutable = "127.0.0.1:1"
	if _, _, err := rt.PushToRelay(unroutable, tid); err == nil {
		t.Fatal("a forbidden relay push reported success")
	} else if _, blocked := err.(ErrTransportBlocked); !blocked {
		t.Fatalf("the refusal was not a policy refusal: %v", err)
	}
	if _, err := rt.PullFromRelay(unroutable); err == nil {
		t.Fatal("a forbidden relay pull reported success")
	} else if _, blocked := err.(ErrTransportBlocked); !blocked {
		t.Fatalf("the refusal was not a policy refusal: %v", err)
	}

	// Offline refuses everything.
	setMode(t, rt, ModeOffline)
	if rt.TransportAllowed(TransportRadio, tid) ||
		rt.TransportAllowed(TransportRelay, tid) ||
		rt.TransportAllowed(TransportLAN, tid) {
		t.Fatal("offline mode allowed a transport")
	}
	// ...and still tracks the message.
	if rt.ledger.Len() == 0 {
		t.Fatal("offline dropped responsibility instead of holding it")
	}
}
