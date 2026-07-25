package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
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

// An unknown mode must never WIDEN what is permitted. A typo like
// "meshtastic_onyl" resolving to Auto would quietly open an internet relay
// for someone who was explicitly trying not to have one — and they would
// find out from the relay operator rather than from their own client.
func TestUnknownModeIsRefusedAndNeverWidens(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	tid, err := rt.CreateSpace("Careful")
	if err != nil {
		t.Fatal(err)
	}
	setMode(t, rt, ModeMeshtasticOnly)

	// Storing a typo is refused outright, and the good policy stands.
	s := rt.GetSettings()
	s.Connectivity = Connectivity{Mode: ConnectivityMode("meshtastic_onyl")}
	err = rt.SetSettings(s)
	if err == nil {
		t.Fatal("a mode this build cannot read was accepted into storage")
	}
	if _, bad := err.(ErrBadConnectivityMode); !bad {
		t.Fatalf("the refusal was not a validation error: %v", err)
	}
	if got := rt.Connectivity(); got.Mode != ModeMeshtasticOnly || got.Unreadable {
		t.Fatalf("a refused write disturbed the stored policy: %+v", got)
	}
	if rt.TransportAllowed(TransportRelay, tid) {
		t.Fatal("the relay became permitted after a refused write")
	}

	// And if such a value reaches the struct anyway — a corrupted file, a
	// downgrade — resolving it must hold rather than widen.
	corrupt := Connectivity{Mode: ConnectivityMode("something-newer")}
	if corrupt.allows(TransportRelay, tid) || corrupt.allows(TransportRadio, tid) ||
		corrupt.allows(TransportLAN, tid) {
		t.Fatal("an unreadable stored mode permitted a transport")
	}
	// Unset is different: a fresh install means Auto.
	fresh := Connectivity{}
	if !fresh.allows(TransportRelay, tid) || !fresh.allows(TransportRadio, tid) {
		t.Fatal("a fresh install did not default to Auto")
	}
}

// The relay connection may exist because one space permits it. That must
// not make another space's traffic fair game: the global gate governs the
// CONNECTION, the per-space gate governs routing and which mailboxes are
// polled at all.
func TestPerSpaceIsolationOnASharedRelay(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	open, err := alice.CreateSpace("Open Room")
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := alice.CreateSpace("Quiet Room")
	if err != nil {
		t.Fatal(err)
	}
	for _, tid := range []id.TerminalID{open, quiet} {
		invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bob.JoinInvite(invite); err != nil {
			t.Fatal(err)
		}
	}

	// One runtime, two policies: the open room uses the internet, the quiet
	// one is radio-only.
	s := alice.GetSettings()
	s.Connectivity = Connectivity{
		Mode:     ModeAuto,
		PerSpace: map[string]ConnectivityMode{quiet.Hex(): ModeMeshtasticOnly},
	}
	if err := alice.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	if _, err := alice.Say(open, "this one may travel", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(quiet, "this one may not", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// The relay is permitted overall, because the open room allows it.
	if !alice.anySpaceAllows(TransportRelay) {
		t.Fatal("the relay connection was refused even though a space allows it")
	}
	if _, _, err := alice.PushToRelay(addr, open); err != nil {
		t.Fatalf("the permitted space could not use the relay: %v", err)
	}
	// The quiet room is refused on the SAME open connection.
	if _, _, err := alice.PushToRelay(addr, quiet); err == nil {
		t.Fatal("a Meshtastic-only space was pushed to the relay")
	} else if _, blocked := err.(ErrTransportBlocked); !blocked {
		t.Fatalf("the refusal was not a policy refusal: %v", err)
	}

	// Bob pulls everything the relay will give him. He must receive the
	// open room's message and nothing from the quiet one — not because the
	// relay filtered it, but because it was never put there.
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if msgCount(bob, open) == 0 {
		t.Fatal("the permitted space did not converge over the relay")
	}
	if n := msgCount(bob, quiet); n != 0 {
		t.Fatalf("a Meshtastic-only space leaked %d messages onto the relay", n)
	}

	// And Alice does not POLL the quiet room's mailbox either: a space set
	// to radio-only should leave no trace of its activity on the relay,
	// including the shape of what it asks for.
	if _, err := alice.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if !alice.TransportAllowed(TransportRelay, open) {
		t.Fatal("the open room lost relay access")
	}
	if alice.TransportAllowed(TransportRelay, quiet) {
		t.Fatal("the quiet room gained relay access")
	}
}
