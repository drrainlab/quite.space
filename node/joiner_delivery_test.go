package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// Somebody who JOINED is heard before the other side has spoken.
//
// pushToRelay addresses every OTHER member's own relay inbox, and it learns
// who those are from the devices that have authored a frame in this log
// unioned with st.space.Members() — which is populated at invite time and is
// therefore controller-only. So a joiner's FIRST push is addressed to nobody,
// and the loop deliberately holds the frames rather than marking them handed
// off.
//
// That hold is correct and it clears itself: the moment the controller's own
// cycle pushes into the joiner's inbox, the joiner learns the device and its
// waiting frames go out. This test pins the recovery, because the failure it
// would replace is silent — frames prepared every cycle and delivered to no
// one, with no error anywhere.
func TestAJoinerIsHeardBeforeTheOtherSideSpeaks(t *testing.T) {
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
	if err := alice.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := bob.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// THE POINT: alice has said nothing at all. Bob speaks first, which is
	// an ordinary thing for a person handed an invitation to do.
	if _, err := bob.Say(tid, "дерево рожает", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// The first push really is addressed to nobody — the starting condition
	// this test exists to recover from.
	if _, recipients, _, err := bob.pushToRelay(addr, tid, AssetsManifests); err != nil {
		t.Fatal(err)
	} else if recipients != 0 {
		t.Skip("a joiner already knows a peer device — nothing to recover from")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, _, _ = alice.PushToRelay(addr, tid)
		_, _ = bob.PullFromRelay(addr)
		_, _, _ = bob.PushToRelay(addr, tid)
		_, _ = alice.PullFromRelay(addr)
		for _, e := range bobEntries(t, alice, tid) {
			if e == "дерево рожает" {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("the joiner's first message never reached the person who invited them")
}
