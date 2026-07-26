package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// The whole point, over a real mesh: Bob switches on a radio and finds out
// that a gateway is there — before sending anything, and without having to
// guess whether the silence means "no gateway", "gateway busy" or "wrong
// channel".
//
// Deliberately end to end. Every piece of this passed its own unit test
// while the beacon still could not reach a node at all, because a beacon
// names no terminal and the pump routed messages by terminal: it was being
// dropped one layer above everything that worked.
func TestAGatewayIsFoundOverTheMesh(t *testing.T) {
	oldPump, oldSummary := meshPumpEvery, meshSummaryEvery
	meshPumpEvery, meshSummaryEvery = 30*time.Millisecond, 200*time.Millisecond
	defer func() { meshPumpEvery, meshSummaryEvery = oldPump, oldSummary }()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	bob.SetMeshNetwork("beta-mesh-01")
	tid, err := bob.CreateSpace("Off-grid Camp")
	if err != nil {
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
		Instance:    "roof Pi",
		Radio:       radio,
		RadioLink:   routing.LinkID("mesh:test-hub"),
		RadioDomain: routing.LoopDomainID("meshtastic-quiet@beta-mesh-01"),
		RelayAddr:   "127.0.0.1:1",
		RelayDomain: routing.LoopDomainID("relay:none"),
		NetworkID:   "beta-mesh-01",
		BeaconEvery: 50 * time.Millisecond,
		Subscriptions: []bridge.Subscription{{
			NetworkID:    "beta-mesh-01",
			Terminal:     tid,
			RadioDevices: []id.DeviceID{bob.Device.ID},
		}},
		WakeEvery:     time.Hour, // only the beacon should speak here
		AirtimePerMin: 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()

	announce := func() {
		for range 3 {
			br.PushBeacon(time.Now())
			time.Sleep(60 * time.Millisecond)
		}
	}
	announce()
	waitUntil(t, 15*time.Second, "Bob never heard the gateway announce itself",
		func() bool {
			announce()
			return len(bob.Gateways()) > 0
		})

	gw := bob.Gateways()[0]
	if gw.Label != "roof Pi" {
		t.Errorf("label did not survive the mesh: %q", gw.Label)
	}
	if gw.Trusted {
		t.Fatal("an unpinned gateway arrived trusted")
	}
	if gw.Fingerprint != fingerprintOf(br.CustodianPub()) {
		t.Fatalf("the fingerprint shown does not match the gateway's key:\n"+
			" shown %s\n actual %s — a person comparing it against what the "+
			"operator told them would be misled",
			gw.Fingerprint, fingerprintOf(br.CustodianPub()))
	}
	if !gw.Fresh(time.Now()) {
		t.Error("a beacon that just arrived is already stale")
	}

	// The bootstrap ritual: the person compares the fingerprint with what the
	// operator gave them, pins it, and the SAME key is what signs custody
	// receipts — one fingerprint to check, not two.
	if err := bob.PinCustodian("radio", br.CustodianPub()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "pinning the gateway did not make it trusted",
		func() bool {
			announce()
			gws := bob.Gateways()
			return len(gws) == 1 && gws[0].Trusted
		})
}
