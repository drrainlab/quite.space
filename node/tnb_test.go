package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
)

// TN-B: a custody ACK upgrades the ladder to accepted_by_relay ONLY under
// a key pinned for the ingress link domain; a valid signature from an
// unpinned custodian records nothing (TOFU forbidden).
func TestCustodyReceiptPinning(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Bridged")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(tid, "over the boundary", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)

	sign := func(priv ed25519.PrivateKey) []byte {
		r := &bridge.CustodyReceipt{
			FrameIDs:   []id.EventID{eid},
			StoreID:    "test-store",
			AcceptedAt: uint64(time.Now().Unix()),
			ExpiresAt:  uint64(time.Now().Add(time.Hour).Unix()),
			Instance:   "test-bridge",
		}
		return r.Sign(priv)
	}
	deliver := func(receipt []byte) {
		rt.mu.Lock()
		rt.curLink = "lan"
		rt.handleCustodyReceipt(tid, receipt)
		rt.curLink = ""
		rt.mu.Unlock()
	}

	// A self-consistent receipt from an UNPINNED custodian: nothing recorded.
	_, evilPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deliver(sign(evilPriv))
	if d := sp.Trust.Delivery(eid, tid); d.Level >= claims.DeliveryAcceptedByRelay {
		t.Fatalf("unpinned custodian must not upgrade the ladder: %v", d.Level)
	}

	// Pin the real custodian for the "lan" domain → the ACK counts.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.PinCustodian("lan", pub); err != nil {
		t.Fatal(err)
	}
	deliver(sign(priv))
	if d := sp.Trust.Delivery(eid, tid); d.Level != claims.DeliveryAcceptedByRelay {
		t.Fatalf("pinned custody ACK not recorded: %v", d.Level)
	}
	if links := rt.CarriedBy(eid); len(links) == 0 || links[len(links)-1] != "bridge" {
		t.Fatalf("carried-by missing bridge custody: %v", links)
	}
}
