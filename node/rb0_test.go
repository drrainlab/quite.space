package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bridge"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// RB-0A acceptance: the whole boundary, end to end, on TWO REAL runtimes.
//
// Bob has no internet. His only carrier is a Meshtastic mesh. Alice has no
// radio; her only carrier is the blind relay. Between them sits one blind
// bridge that holds no identity and no space keys. Nothing in this test is
// hand-made: every frame on the air is produced by a node's own sync engine
// or re-emitted verbatim by the bridge.
//
// This is the test the TN wave never had. TestTwoSegmentLoop proved the
// bridge could carry frames someone else had already framed; it could not
// see that a node and a bridge never meet in the same relay mailbox, and
// that a node on radio stays silent until something asks it for frames.
func TestRadioBridgeClosesTheLoop(t *testing.T) {
	// LoRa cadence compressed for the test; the protocol is unchanged.
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

	tid, err := alice.CreateSpace("Off Grid")
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

	// Bob is on the mesh and nowhere else. Alice never touches the radio.
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
		Instance:    "rb0-test",
		Radio:       radio,
		RadioLink:   routing.LinkID("mesh:test-hub"),
		RadioDomain: routing.LoopDomainID("meshtastic-quiet@beta-mesh-01"),
		RelayAddr:   relayAddr,
		RelayDomain: routing.LoopDomainID("relay:" + relayAddr),
		// D1: operator-provisioned opaque routing capability. The bridge is
		// told which mailboxes it serves and on which side of the boundary
		// each one lives; the ids stay opaque bytes to it.
		Subscriptions: []bridge.Subscription{{
			NetworkID:       "beta-mesh-01",
			Terminal:        tid,
			RadioDevices:    []id.DeviceID{bob.Device.ID},
			InternetDevices: []id.DeviceID{alice.Device.ID},
		}},
		WakeEvery: 200 * time.Millisecond,
		// The hub has none of LoRa's airtime limits, and this test is about
		// the loop, not the budget. Real airtime is measured on hardware in
		// RB-3; leaving the default here would just make the test wait.
		AirtimePerMin: 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()

	// One turn of the daemon loop from cmd/quiet-bridge.
	cycle := func() {
		now := time.Now()
		br.PumpRadio(now)
		br.WakeRadio(now)
		br.PushRadio(now)
		if _, err := br.PushRelay(now); err != nil {
			t.Log("relay push:", err)
		}
		if _, err := br.PullRelay(now); err != nil {
			t.Log("relay pull:", err)
		}
	}

	// ---- Uplink: Bob (radio only) → Alice (internet only) ----
	//
	// Bob's node announces its chain state on the air and then waits. Until
	// the bridge answers with a summary of its own, nothing asks Bob for
	// frames and the message never leaves his device.
	const fromBob = "the pass is open, coming down tomorrow"
	if _, err := bob.Say(tid, fromBob, SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		cycle()
		if _, err := alice.PullFromRelay(relayAddr); err != nil {
			t.Log("alice pull:", err)
		}
		if msgCount(alice, tid) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			s := br.Stats()
			t.Fatalf("uplink never closed: Bob's message did not reach Alice "+
				"(radio in/out %d/%d · relay in/out %d/%d · custody %d · refused %d)",
				s.RadioIn, s.RadioOut, s.RelayIn, s.RelayOut, br.QueueLen(), s.Refused)
		}
		time.Sleep(50 * time.Millisecond)
	}
	sa, _ := alice.Space(tid)
	if got := sa.State.Messages()[0].Text; got != fromBob {
		t.Fatalf("uplink mangled the message: %q", got)
	}

	// ---- Downlink: Alice (internet only) → Bob (radio only) ----
	//
	// Alice pushes exactly as she always does, into every other member's own
	// relay inbox. The bridge must find Bob's copy there — without eating it,
	// because Bob may yet come online himself — and put it on the air.
	const fromAlice = "understood, leaving the gate unlocked"
	if _, err := alice.Say(tid, fromAlice, SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(relayAddr, tid); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		cycle()
		if msgCount(bob, tid) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			s := br.Stats()
			t.Fatalf("downlink never closed: Alice's message did not reach Bob "+
				"(radio in/out %d/%d · relay in/out %d/%d · custody %d · refused %d)",
				s.RadioIn, s.RadioOut, s.RelayIn, s.RelayOut, br.QueueLen(), s.Refused)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Bob's own mailbox is still stocked: the bridge READ the downlink
	// without eating it. Asserted on the relay directly — asking Bob to pull
	// would prove nothing, since he already has these events off the air and
	// would apply zero either way.
	client, err := relay.DialClient(relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	nowU := uint64(time.Now().Unix())
	b := relay.Bucket(nowU)
	hints := [][]byte{relay.HintFor(tid, bob.Device.ID, b)}
	if b > 0 {
		hints = append(hints, relay.HintFor(tid, bob.Device.ID, b-1))
	}
	items, err := client.Fetch(hints)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("the bridge drained Bob's mailbox: a node that finds internet " +
			"after the bridge polled would discover its own mail already eaten")
	}

	// Blindness is not a claim in a comment: the bridge carried both
	// directions while holding no space key at all.
	if s := br.Stats(); s.RadioIn == 0 || s.RadioOut == 0 || s.RelayIn == 0 || s.RelayOut == 0 {
		t.Fatalf("a direction was never actually carried: %+v", s)
	}
}

// RB-0A exit criterion: three nodes and two gateways on one carrier, and
// the control traffic stays bounded.
//
// The failure this guards against is not a wrong message — it is a segment
// that never goes quiet. A gateway that answered a summary with a summary,
// or two gateways that treated each other's announcements as a reason to
// announce, would fill the band with protocol and deliver nothing. On a
// hub that only shows up as a slow test; on LoRa it is a dead channel and
// two flat batteries.
func TestRadioSegmentStaysQuiet(t *testing.T) {
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
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()

	tid, err := alice.CreateSpace("Two Gateways")
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range []*Runtime{bob, carol} {
		invite, err := alice.MintInvite(tid, peer.Device.ID, peer.Device.X25519Pub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.JoinInvite(invite); err != nil {
			t.Fatal(err)
		}
		if err := peer.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
			t.Fatal(err)
		}
	}

	const wakeEvery = 300 * time.Millisecond
	sub := bridge.Subscription{
		NetworkID:       "beta-mesh-01",
		Terminal:        tid,
		RadioDevices:    []id.DeviceID{bob.Device.ID, carol.Device.ID},
		InternetDevices: []id.DeviceID{alice.Device.ID},
	}
	var bridges []*bridge.Bridge
	for i := range 2 {
		radio, err := meshtastic.DialTCP(hub.Addr())
		if err != nil {
			t.Fatal(err)
		}
		defer radio.Close()
		br, err := bridge.New(bridge.Config{
			DataDir:   t.TempDir(),
			Instance:  "gw" + itoa(i),
			Radio:     radio,
			RadioLink: routing.LinkID("mesh:gw" + itoa(i)),
			// One SEGMENT, two links: the loop domain is shared so both
			// gateways recognise the same forwarding domain, while the link
			// ids stay distinct. Getting this backwards would break
			// split-horizon between them.
			RadioDomain:   routing.LoopDomainID("meshtastic-quiet@beta-mesh-01"),
			RelayAddr:     relayAddr,
			RelayDomain:   routing.LoopDomainID("relay:" + relayAddr),
			Subscriptions: []bridge.Subscription{sub},
			WakeEvery:     wakeEvery,
			AirtimePerMin: 1e9,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer br.Close()
		bridges = append(bridges, br)
	}

	cycle := func() {
		now := time.Now()
		for _, br := range bridges {
			br.PumpRadio(now)
			br.WakeRadio(now)
			br.PushRadio(now)
			br.PushRelay(now)
			br.PullRelay(now)
		}
	}

	if _, err := alice.Say(tid, "both of you should hear this once", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(relayAddr, tid); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	deadline := start.Add(20 * time.Second)
	for {
		cycle()
		if msgCount(bob, tid) >= 1 && msgCount(carol, tid) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("two gateways failed to deliver to two radio nodes: %+v %+v",
				bridges[0].Stats(), bridges[1].Stats())
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Now let the segment sit with nothing new to say, and watch it.
	settled := [2]bridge.Stats{bridges[0].Stats(), bridges[1].Stats()}
	quietStart := time.Now()
	const quiet = 2 * time.Second
	for time.Since(quietStart) < quiet {
		cycle()
		time.Sleep(30 * time.Millisecond)
	}

	for i, br := range bridges {
		s := br.Stats()
		// Announcements are rate-limited per destination, so an idle segment
		// costs at most one summary per cooldown — never one per received
		// packet, which is what a summary-answers-summary bug looks like.
		budget := int(quiet/wakeEvery) + 2
		if grew := s.Wakes - settled[i].Wakes; grew > budget {
			t.Fatalf("gateway %d announced %d times in %v (budget %d): "+
				"the carrier is feeding itself", i, grew, quiet, budget)
		}
		// Nothing new was said, so nothing new should have crossed either
		// boundary. Frames already carried must not be carried again.
		if grew := s.RelayOut - settled[i].RelayOut; grew != 0 {
			t.Fatalf("gateway %d pushed %d frames to the relay with nothing "+
				"new to push: uplink is echoing", i, grew)
		}
	}
	// And the duplicate the second gateway inevitably re-airs is collapsed by
	// the nodes, not left to become a second message on someone's screen.
	if n := msgCount(bob, tid); n != 1 {
		t.Fatalf("two gateways produced %d copies of one message", n)
	}
}
