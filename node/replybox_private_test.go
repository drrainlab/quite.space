package node

// A private space's media answers come back to a box of their own, so a
// photo's chunks can never fill the mailbox the next message needs.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestAPrivateWantCarriesItsOwnReplyBox(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	// Alice stops reading so her mailbox keeps what bob puts there.
	alice.applyRelaySync("", 0)

	var h id.Hash
	h[0] = 0x5a
	bob.addRelayWants(tid, []id.Hash{h})
	bob.relaySyncOnce(addr)

	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	now := uint64(time.Now().Unix())
	items, err := client.Collect([][]byte{relay.CapFor(tid, alice.Device.ID, relay.Bucket(now))})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		parts, err := bundle.DecodeParts(it)
		if err != nil || len(parts.Wants) == 0 {
			continue
		}
		found = true
		if len(parts.ReplyBox) != relay.HintLen {
			t.Fatalf("the want rides without a reply box (%d bytes) — the answer would land in the frames mailbox", len(parts.ReplyBox))
		}
	}
	if !found {
		t.Fatal("bob's want never reached alice's mailbox")
	}
}
