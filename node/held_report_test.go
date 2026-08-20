package node

import (
	"fmt"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"testing"

	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// A space that hands nothing over must SAY it is holding.
//
// Every reason the sync loop declines to push is a deliberate choice — it
// would rather hold than guess an address or mark unsent frames as handed
// off. The defect was never the holding; it was that holding and delivering
// looked identical from outside. A post sits in a local log, the relay light
// stays green, and nothing anywhere says the words "still here".
func TestASpaceThatCannotSendSaysSo(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Quiet Line")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(tid, "написано в темноте", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// Arm the sync state, then stop the background loop so the cycle below
	// is the only one that runs and the assertion cannot race a recovery.
	bob.applyRelaySync(addr, 0)
	bob.applyRelaySync("", 0)
	bob.relaySyncOnce(addr)

	st := bob.RelaySync()
	if len(st.Held) == 0 {
		t.Fatal("the space handed nothing over and reported nothing: " +
			"a person watching this screen sees a healthy relay and a post " +
			"that has quietly gone nowhere")
	}
	h := st.Held[0]
	if h.SpaceID != tid.Hex() {
		t.Fatalf("held names the wrong space: %s", h.SpaceID)
	}
	if h.Reason != heldNoRecipient {
		t.Fatalf("held reason = %q, want %q", h.Reason, heldNoRecipient)
	}
	if h.Frames <= 0 {
		t.Fatalf("held reports %d waiting frames — the count is the part a "+
			"person acts on", h.Frames)
	}

	// Alice pushes and bob learns her DEVICE — but a direct invite records
	// no routes, so bob still does not know where alice LISTENS. The old
	// code called the next delivery a success anyway: the bootstrap put the
	// copy at bob's own relay, recorded that guess as if alice had stated
	// it, and cleared the hold. On one shared relay the luck held; across
	// two it was the black hole of BETA_AUDIT_2026-08-20. The honest report
	// is a DIFFERENT hold: delivered on a guess, cursor unmoved.
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	bob.relaySyncOnce(addr)
	held := bob.RelaySync().Held
	if len(held) != 1 || held[0].Reason != heldTentative {
		t.Fatalf("delivery on a guess must be held as a guess: %+v", held)
	}

	// And the hold clears only for the real thing: a STATED route.
	bob.mu.Lock()
	bob.recordPeerRouteLocked(alice.Device.ID, addr, "relay", storage.RouteInvitation)
	bob.mu.Unlock()
	bob.relaySyncOnce(addr)
	if held := bob.RelaySync().Held; len(held) != 0 {
		t.Fatalf("still holding after a stated route existed: %+v", held)
	}
}
