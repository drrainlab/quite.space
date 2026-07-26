package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/bridge"
	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

func gatewayBeacon(t *testing.T, priv ed25519.PrivateKey, b bridge.Beacon) []byte {
	t.Helper()
	if b.ValidFor == 0 {
		b.ValidFor = 600
	}
	if b.NetworkID == "" {
		b.NetworkID = "beta-mesh-01"
	}
	raw, err := bridge.SignBeacon(priv, b)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func onlyGateway(t *testing.T, rt *Runtime) GatewayPresence {
	t.Helper()
	gws := rt.Gateways()
	if len(gws) != 1 {
		t.Fatalf("%d gateways, want 1: %+v", len(gws), gws)
	}
	return gws[0]
}

// A gateway whose key is not pinned is the normal state at the start: the
// person has just switched the radio on and has not been told a key yet.
// Hiding it would leave them with nothing; believing it would be trust on
// first use, which ADR-015 §7 forbids. So it is SHOWN and marked untrusted,
// with the fingerprint they need to check against what the operator said.
func TestAnUnpinnedGatewayIsShownAsUntrusted(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		Label: "roof Pi", BootID: 7, Sequence: 1, UplinkUp: true, QueueDepth: 3,
	}), time.Now())

	gw := onlyGateway(t, rt)
	if gw.Trusted {
		t.Fatal("a gateway nobody pinned was trusted on sight")
	}
	if gw.Fingerprint == "" {
		t.Fatal("no fingerprint to check against what the operator said")
	}
	if gw.Label != "roof Pi" || !gw.UplinkUp || gw.QueueDepth != 3 {
		t.Errorf("claims lost: %+v", gw)
	}
	if !gw.Fresh(time.Now()) {
		t.Error("a beacon that just arrived is not fresh")
	}
}

// Once pinned, the same gateway is trusted — and it is the SAME key that
// signs its custody receipts, so a person has one fingerprint to check.
func TestAPinnedGatewayIsTrusted(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := rt.PinCustodian("radio", pub); err != nil {
		t.Fatal(err)
	}

	rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		Label: "roof Pi", BootID: 7, Sequence: 1,
	}), time.Now())

	if gw := onlyGateway(t, rt); !gw.Trusted {
		t.Fatalf("a pinned gateway was not trusted: %+v", gw)
	}

	// Pinned on a DIFFERENT link is not pinned here. A receipt or beacon
	// only counts on the boundary its pin names.
	other := openRuntime(t, t.TempDir(), "carol")
	defer other.Close()
	if err := other.PinCustodian("lan", pub); err != nil {
		t.Fatal(err)
	}
	other.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		BootID: 7, Sequence: 1,
	}), time.Now())
	if gw := onlyGateway(t, other); gw.Trusted {
		t.Fatal("a key pinned for the LAN was trusted on the radio")
	}
}

// Freshness must not depend on this device's clock. Bob has been off the
// internet for days and his clock may be badly wrong; a presence check that
// failed because of clock drift would be the least debuggable failure in the
// whole wave. A beacon claiming to be from 2031 — or from 1998 — is still
// discovered, and still ages out on elapsed time.
func TestFreshnessIgnoresTheWallClock(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	arrived := time.Now()
	for _, slot := range []uint64{1, 4_000_000_000} { // 1970 and 2096
		rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
			BootID: 7, Sequence: slot, IssuedSlot: slot, ValidFor: 600,
		}), arrived)
		gw := onlyGateway(t, rt)
		if !gw.Fresh(arrived) {
			t.Fatalf("a beacon issued at slot %d was not discovered", slot)
		}
	}
	gw := onlyGateway(t, rt)
	if gw.Fresh(arrived.Add(601 * time.Second)) {
		t.Error("presence outlived the validity the gateway itself stated")
	}
	if !gw.Fresh(arrived.Add(599 * time.Second)) {
		t.Error("presence expired early")
	}
}

// A replayed beacon is the cheapest attack on this carrier: capture one
// packet, rebroadcast it forever, and a gateway that has been off for a week
// looks present. Ordering is by (boot, sequence), so a repeat cannot refresh
// anything.
func TestAReplayedBeaconCannotRefreshPresence(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	start := time.Now()
	captured := gatewayBeacon(t, priv, bridge.Beacon{
		BootID: 7, Sequence: 3, IssuedSlot: 1_000, ValidFor: 60,
	})
	rt.noteBeacon("radio", captured, start)

	// An hour later the attacker replays the same packet.
	later := start.Add(time.Hour)
	rt.noteBeacon("radio", captured, later)

	gw := onlyGateway(t, rt)
	if gw.Fresh(later) {
		t.Fatal("a replayed beacon made a gateway that has been gone for an " +
			"hour look present")
	}
	if !gw.LastHeard.Equal(start) {
		t.Errorf("the replay moved the last-heard time to %v", gw.LastHeard)
	}

	// A genuinely newer announcement from the same boot does refresh it.
	rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		BootID: 7, Sequence: 4, IssuedSlot: 2_000, ValidFor: 60,
	}), later)
	if !onlyGateway(t, rt).Fresh(later) {
		t.Fatal("a newer beacon from the same boot did not refresh presence")
	}
}

// A gateway that restarts begins its sequence again, so sequence alone
// cannot order two boots. They are ordered by the gateway's OWN stated time —
// one gateway claim against another, never against this device's clock. A
// replay of a previous boot therefore still loses.
func TestARestartedGatewayIsAcceptedButAnOldBootReplayedIsNot(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	start := time.Now()
	oldBoot := gatewayBeacon(t, priv, bridge.Beacon{
		BootID: 7, Sequence: 90, IssuedSlot: 1_000, ValidFor: 60,
	})
	rt.noteBeacon("radio", oldBoot, start)

	// The gateway reboots: new boot id, sequence back to 1, later slot.
	restart := start.Add(time.Minute)
	rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		BootID: 8, Sequence: 1, IssuedSlot: 2_000, ValidFor: 60,
	}), restart)
	gw := onlyGateway(t, rt)
	if !gw.Fresh(restart) || gw.BootID != 8 {
		t.Fatalf("a restarted gateway was not recognised: %+v", gw)
	}

	// Now the attacker replays the pre-restart beacon, whose sequence is far
	// higher. It is from an older boot and must lose.
	replay := restart.Add(time.Minute)
	rt.noteBeacon("radio", oldBoot, replay)
	gw = onlyGateway(t, rt)
	if gw.BootID != 8 {
		t.Fatalf("a replayed older boot displaced the current one: %+v", gw)
	}
	if gw.Fresh(replay) {
		t.Fatal("a replayed older boot refreshed presence")
	}
}

// A typo in the network id is a real setup mistake, and the honest answer is
// not silence: the beacons are refused, and the fact that they were refused
// is counted so the person can see the gateway they can hear is on another
// network.
func TestAForeignNetworkIsRefusedAndCounted(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	rt.SetMeshNetwork("beta-mesh-01")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		NetworkID: "someone-elses-mesh", BootID: 7, Sequence: 1,
	}), time.Now())

	if gws := rt.Gateways(); len(gws) != 0 {
		t.Fatalf("a gateway on another network was adopted: %+v", gws)
	}
	if n := rt.ForeignBeacons(); n != 1 {
		t.Fatalf("foreign beacons counted %d, want 1 — a typo in the network "+
			"id would look exactly like an absent gateway", n)
	}

	// With no network configured, everything is shown: refusing by default
	// would hide the gateway from someone who has not set an id yet.
	fresh := openRuntime(t, t.TempDir(), "carol")
	defer fresh.Close()
	fresh.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		NetworkID: "someone-elses-mesh", BootID: 7, Sequence: 1,
	}), time.Now())
	if gws := fresh.Gateways(); len(gws) != 1 {
		t.Fatal("a node with no configured network hid the only gateway it could hear")
	}
}

// A beacon that does not verify is not evidence of anything and must leave
// no trace in presence — otherwise anyone on the carrier could invent a
// gateway by broadcasting noise.
func TestGarbageAndForgeriesLeaveNoTrace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	good := gatewayBeacon(t, priv, bridge.Beacon{BootID: 7, Sequence: 1})
	forged := append([]byte(nil), good...)
	forged[len(forged)-1] ^= 0xff

	rt.noteBeacon("radio", forged, time.Now())
	rt.noteBeacon("radio", []byte("not a beacon at all"), time.Now())
	rt.noteBeacon("radio", nil, time.Now())

	if gws := rt.Gateways(); len(gws) != 0 {
		t.Fatalf("noise invented a gateway: %+v", gws)
	}
}

// The beacon is ADVISORY. Its absence changes what a person is shown; it
// must never gate the queue. A node that refused to transmit until it had
// heard a gateway would be undeliverable on any segment whose gateway is
// merely quiet — and unable to bootstrap at all.
func TestSendingDoesNotWaitForAGateway(t *testing.T) {
	oldPump, oldSummary := meshPumpEvery, meshSummaryEvery
	meshPumpEvery, meshSummaryEvery = 30*time.Millisecond, 200*time.Millisecond
	defer func() { meshPumpEvery, meshSummaryEvery = oldPump, oldSummary }()

	h, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	hub := h.Addr()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Off-grid Camp")
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
	if err := alice.StartMeshtastic("tcp:" + hub); err != nil {
		t.Fatal(err)
	}
	if err := bob.StartMeshtastic("tcp:" + hub); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "nobody has announced a gateway", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 15*time.Second,
		"a message did not go out because no gateway had announced itself",
		func() bool { return msgCount(bob, tid) >= 1 })

	if gws := alice.Gateways(); len(gws) != 0 {
		t.Fatalf("gateways appeared from nowhere: %+v", gws)
	}
}

// The status line a person reads has to distinguish the three cases that
// otherwise all look like silence.
func TestPresenceLineNamesWhichSilenceThisIs(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	if got := rt.GatewaySummary(time.Now()); !strings.Contains(got, "no gateway") {
		t.Errorf("with nothing heard: %q", got)
	}

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	rt.noteBeacon("radio", gatewayBeacon(t, priv, bridge.Beacon{
		Label: "roof Pi", BootID: 7, Sequence: 1, ValidFor: 60, UplinkUp: false,
	}), now)

	got := rt.GatewaySummary(now)
	if !strings.Contains(got, "not trusted") {
		t.Errorf("an unpinned gateway did not read as untrusted: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "uplink") {
		t.Errorf("a gateway with no internet did not say so: %q", got)
	}

	stale := rt.GatewaySummary(now.Add(2 * time.Minute))
	if !strings.Contains(stale, "last heard") {
		t.Errorf("a gateway that has gone quiet did not say when: %q", stale)
	}
}
