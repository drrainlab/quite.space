package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// RB-0B: the custody ACK, end to end, on the carrier.
//
// TN-B designed the receipt, minted the custodian key, and wrote the
// node-side pin check — then never sent one. AcceptUplink had no callers
// outside its own test and EncodeCustodyMessage had none anywhere, so a
// node that handed frames to a gateway heard nothing back and could not
// tell "carried" from "lost". Everything downstream of that silence — the
// delivery ladder, what the UI can honestly show — was stuck at
// handed_to_transport forever.
func TestCustodyAckOverRadioMovesTheLadder(t *testing.T) {
	oldPump, oldSummary := meshPumpEvery, meshSummaryEvery
	meshPumpEvery, meshSummaryEvery = 30*time.Millisecond, 200*time.Millisecond
	defer func() { meshPumpEvery, meshSummaryEvery = oldPump, oldSummary }()

	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	relayAddr := "127.0.0.1:" + itoa(port)

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Custody")
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
	if err := bob.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}

	radio, err := meshtastic.DialTCP(hub.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer radio.Close()
	br, err := bridge.New(bridge.Config{
		DataDir:     t.TempDir(),
		Instance:    "rb0b-test",
		Radio:       radio,
		RadioLink:   routing.LinkID("mesh:test-hub"),
		RadioDomain: routing.LoopDomainID("meshtastic-quiet@beta-mesh-01"),
		RelayAddr:   relayAddr,
		RelayDomain: routing.LoopDomainID("relay:" + relayAddr),
		Subscriptions: []bridge.Subscription{{
			NetworkID:       "beta-mesh-01",
			Terminal:        tid,
			RadioDevices:    []id.DeviceID{bob.Device.ID},
			InternetDevices: []id.DeviceID{alice.Device.ID},
		}},
		WakeEvery:     200 * time.Millisecond,
		AirtimePerMin: 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()

	// Bob pins THIS gateway for the radio link. Without the pin a valid
	// signature is only an observation — TOFU stays forbidden.
	if err := bob.PinCustodian("radio", br.CustodianPub()); err != nil {
		t.Fatal(err)
	}

	eid, err := bob.Say(tid, "handing this to the gateway", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if lvl := deliveryLevel(bob, tid, eid); lvl >= claims.DeliveryAcceptedByRelay {
		t.Fatalf("ladder started too high: %v", lvl)
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		now := time.Now()
		br.PumpRadio(now)
		br.PushAcks(now)
		br.WakeRadio(now)
		br.PushRadio(now)
		if deliveryLevel(bob, tid, eid) == claims.DeliveryAcceptedByRelay {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no custody ACK reached Bob: %+v", br.Stats())
		}
		time.Sleep(30 * time.Millisecond)
	}

	// The ladder moved for the right reason, and no further: custody by a
	// gateway is NOT delivery to a person, and must never be shown as one.
	st := deliveryStatus(bob, tid, eid)
	if st.Level.RequiresDestinationProof() {
		t.Fatal("a gateway's custody was recorded as proof of destination")
	}
	links := bob.CarriedBy(eid)
	if len(links) == 0 || links[len(links)-1] != "bridge" {
		t.Fatalf("custody not attributed to the bridge: %v", links)
	}
	// The unpinned case — same protocol, same valid signature, a key the
	// node never trusted — is covered by TestCustodyReceiptPinning.
}

// A withdrawal is not custody. When a gateway announces that it could not
// keep a frame to the promised time, the node must record the bad news
// rather than read the message as another confirmation.
func TestCustodyWithdrawalIsNotRecordedAsCustody(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Lapsing")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(tid, "hold this", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.PinCustodian("radio", pub); err != nil {
		t.Fatal(err)
	}
	deliver := func(raw []byte) {
		rt.mu.Lock()
		rt.curLink = "radio"
		rt.handleCustodyReceipt(tid, raw)
		rt.curLink = ""
		rt.mu.Unlock()
	}

	now := time.Now()
	held := (&bridge.CustodyReceipt{
		FrameIDs:   []id.EventID{eid},
		StoreID:    "store",
		AcceptedAt: uint64(now.Unix()),
		ExpiresAt:  uint64(now.Add(time.Hour).Unix()),
		Instance:   "gw0",
	}).Sign(priv)
	deliver(held)
	if lvl := sp.Trust.Delivery(eid, tid).Level; lvl != claims.DeliveryAcceptedByRelay {
		t.Fatalf("custody claim not recorded: %v", lvl)
	}
	if _, lapsed := rt.CustodyLapsed(eid); lapsed {
		t.Fatal("a live custody claim was read as a withdrawal")
	}

	// The gateway gives up.
	withdrawal := (&bridge.CustodyReceipt{
		FrameIDs:   []id.EventID{eid},
		StoreID:    "store",
		AcceptedAt: uint64(now.Unix()),
		ExpiresAt:  uint64(now.Add(time.Minute).Unix()),
		Instance:   "gw0",
		Kind:       bridge.ReceiptLapsed,
	}).Sign(priv)
	deliver(withdrawal)
	lapse, ok := rt.CustodyLapsed(eid)
	if !ok {
		t.Fatal("the withdrawal was swallowed: the sender still believes " +
			"a gateway is carrying this message")
	}
	if lapse.Instance != "gw0" || lapse.Space != tid {
		t.Fatalf("withdrawal recorded against the wrong thing: %+v", lapse)
	}
	// The receipt that WAS true when issued stays on the record. The
	// delivery ladder is closed and has no rung for "carried, then not" —
	// rewriting history would be the dishonest fix.
	if lvl := sp.Trust.Delivery(eid, tid).Level; lvl != claims.DeliveryAcceptedByRelay {
		t.Fatalf("a withdrawal rewrote a receipt that was true when issued: %v", lvl)
	}
}
