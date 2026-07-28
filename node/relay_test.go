package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// M1.5 acceptance: two nodes that NEVER connect to each other converge
// through a blind courier, and the sender's trust engine records exactly
// accepted_by_relay — the ladder does not move without the recipient's own
// signed receipt.
func TestStoreAndForwardThroughBlindRelay(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := "127.0.0.1:" + itoa(port)

	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()

	tid, err := rtA.CreateSpace("Dead Drop")
	if err != nil {
		t.Fatal(err)
	}
	msgID, err := rtA.Say(tid, "left the recorder in the usual place", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := rtA.MintInvite(tid, rtB.Device.ID, rtB.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtB.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}

	// A pushes to the relay. No direct link to B exists at any point.
	pushed, deadline, err := rtA.PushToRelay(addr, tid)
	if err != nil {
		t.Fatal(err)
	}
	if pushed == 0 || deadline <= uint64(time.Now().Unix()) {
		t.Fatalf("push implausible: %d events, deadline %d", pushed, deadline)
	}
	if srv.Pending() != 1 {
		t.Fatalf("relay should hold 1 bundle, holds %d", srv.Pending())
	}

	// Honesty check: the sender's proven level is accepted_by_relay, and
	// the projection cannot be read as delivery.
	sA, _ := rtA.spaceForTest(tid)
	st := sA.Trust.Delivery(msgID, tid)
	if st.Level != claims.DeliveryAcceptedByRelay {
		t.Fatalf("pushed event level = %v", st.Level)
	}
	if st.Level.RequiresDestinationProof() {
		t.Fatal("relay receipt classified as destination proof")
	}

	// B pulls later — different process lifetime, no shared state.
	applied, err := rtB.PullFromRelay(addr)
	if err != nil {
		t.Fatal(err)
	}
	if applied == 0 {
		t.Fatal("nothing applied from relay")
	}
	sB, _ := rtB.spaceForTest(tid)
	msgs := sB.State.Messages()
	if len(msgs) != 1 || msgs[0].Text != "left the recorder in the usual place" {
		t.Fatalf("message did not survive the dead drop: %+v", msgs)
	}
	// Collect drained the mailbox.
	if srv.Pending() != 0 {
		t.Fatalf("relay still holds %d items after collect", srv.Pending())
	}
	// Pulling again is a clean no-op.
	if applied, err := rtB.PullFromRelay(addr); err != nil || applied != 0 {
		t.Fatalf("second pull: %d %v", applied, err)
	}
}

func TestRelayQuotaRefusesFlood(t *testing.T) {
	limits := relay.ServerLimits{MaxItemBytes: 1024, PerHint: 2, MaxTTL: time.Hour}
	srv, port, err := relay.StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	client, err := relay.DialClient("127.0.0.1:" + itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	hint := make([]byte, relay.HintLen)
	if _, err := client.Put(hint, 0, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(hint, 0, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(hint, 0, []byte("three")); err == nil {
		t.Fatal("per-hint quota not enforced")
	}
	// Oversized item refused.
	if _, err := client.Put(hint, 0, make([]byte, 4096)); err == nil {
		t.Fatal("oversized item accepted")
	}
}
