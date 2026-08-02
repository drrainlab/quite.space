package node

import (
	"fmt"
	"testing"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// A space that hands nothing over must SAY it is holding.
//
// Every reason the sync loop declines to push is a deliberate choice — it
// would rather hold than guess an address or mark unsent frames as handed
// off. The defect was never the holding; it was that holding and delivering
// looked identical from outside. A post sits in a local log, the relay light
// stays green, and nothing anywhere says the words "still here".
func TestASpaceThatCannotSendSaysSo(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
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

	// And it stops saying it the moment it is no longer true: alice pushes,
	// bob learns her device, and the hold clears on the next cycle.
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
	if held := bob.RelaySync().Held; len(held) != 0 {
		t.Fatalf("still reporting a hold after the peer became addressable: %+v", held)
	}
}
