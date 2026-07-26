package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// A wrong device path or a radio that is not there must be reported to the
// caller, not swallowed into a retry loop. "I typed the wrong path" and "the
// radio is unplugged" need different reactions from the person.
func TestFirstDialFailureIsReturnedNotRetriedSilently(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()

	if err := rt.StartMeshtastic("tcp:127.0.0.1:1"); err == nil {
		t.Fatal("a radio that is not there reported success")
	}
	if err := rt.StartMeshtastic("carrier-pigeon:/dev/bird"); err == nil {
		t.Fatal("an unknown target form reported success")
	}
	st := rt.Mesh()
	if st.Connected || st.Reconnecting {
		t.Fatalf("a failed first dial left a supervisor running: %+v", st)
	}
}

// The failure this step exists for: someone unplugs the USB radio, or the
// node reboots after a config change. Before this the link died once and
// stayed dead — the node went permanently deaf with no error anywhere.
func TestRadioComesBackAfterTheLinkDrops(t *testing.T) {
	oldPump, oldSummary := meshPumpEvery, meshSummaryEvery
	oldMin, oldMax := meshBackoffMin, meshBackoffMax
	meshPumpEvery, meshSummaryEvery = 30*time.Millisecond, 200*time.Millisecond
	meshBackoffMin, meshBackoffMax = 20*time.Millisecond, 100*time.Millisecond
	defer func() {
		meshPumpEvery, meshSummaryEvery = oldPump, oldSummary
		meshBackoffMin, meshBackoffMax = oldMin, oldMax
	}()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.SetConfig(&meshtastic.HubConfig{Region: 3, HopLimit: 3,
		UsePreset: true, TxEnabled: true, ChannelName: "quiet-beta"})

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
	if err := alice.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	if err := bob.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}

	if _, err := alice.Say(tid, "before the radio dropped", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 15*time.Second, "the first message never crossed the mesh", func() bool {
		return msgCount(bob, tid) >= 1
	})

	// The radio is yanked out, or reboots after a config change.
	hub.SetConfig(&meshtastic.HubConfig{Region: 2 /* someone changed it */, HopLimit: 3,
		UsePreset: true, TxEnabled: true, ChannelName: "quiet-beta"})
	hub.DropAll()

	// Assert on the counter, not on Reconnecting: at this backoff the link
	// is often back before a poll can sample the transient down state, and a
	// test that depends on catching it would be flaky for no good reason.
	// The counter only advances after a link died AND was replaced.
	waitUntil(t, 20*time.Second, "the radio never came back on its own", func() bool {
		return alice.Mesh().Reconnects >= 1 && bob.Mesh().Reconnects >= 1 &&
			alice.Mesh().Connected && bob.Mesh().Connected
	})

	if _, err := alice.Say(tid, "after the radio came back", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "nothing crossed the mesh after it reconnected", func() bool {
		return msgCount(bob, tid) >= 2
	})

	// A node that reconnected reports what it is configured for NOW. A stale
	// picture would have the diagnostic explaining a fault on a radio whose
	// settings someone has since changed.
	cfg := alice.MeshConfig()
	if cfg.LoRa == nil {
		t.Fatal("no configuration after reconnect")
	}
	if got := cfg.LoRa.RegionName(); got != "EU_433" {
		t.Errorf("configuration is stale after reconnect: region %q, "+
			"the node now reports EU_433", got)
	}
	if st := alice.Mesh(); st.Reconnects == 0 {
		t.Error("a reconnect happened and was not counted")
	}
}

// Every reconnect adopts a link. If the dead ones are not let go, a node on a
// flaky USB port accumulates pump goroutines and duplicate sends until it
// falls over — and the symptom would be a slow node, not an obvious leak.
func TestReconnectDoesNotStackLinks(t *testing.T) {
	oldPump, oldSummary := meshPumpEvery, meshSummaryEvery
	oldMin, oldMax := meshBackoffMin, meshBackoffMax
	meshPumpEvery, meshSummaryEvery = 20*time.Millisecond, 100*time.Millisecond
	meshBackoffMin, meshBackoffMax = 10*time.Millisecond, 50*time.Millisecond
	defer func() {
		meshPumpEvery, meshSummaryEvery = oldPump, oldSummary
		meshBackoffMin, meshBackoffMax = oldMin, oldMax
	}()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	if _, err := rt.CreateSpace("Camp"); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}

	for i := range 4 {
		hub.DropAll()
		waitUntil(t, 10*time.Second, "the link did not come back after a drop", func() bool {
			return rt.Mesh().Connected && rt.Mesh().Reconnects > i
		})
	}
	// Let any straggler pump pass finish noticing its link is dead.
	waitUntil(t, 5*time.Second, "dead radio links were never let go", func() bool {
		return liveRadioLinks(rt) == 1
	})
	if n := liveRadioLinks(rt); n != 1 {
		t.Fatalf("%d radio links alive after 4 reconnects, want 1", n)
	}
}

// A radio that never comes back must not be redialled as fast as the machine
// can manage. On a battery-powered Pi that is the difference between waiting
// and flattening the battery.
func TestAMissingRadioIsNotHammered(t *testing.T) {
	oldMin, oldMax := meshBackoffMin, meshBackoffMax
	meshBackoffMin, meshBackoffMax = 20*time.Millisecond, 200*time.Millisecond
	defer func() { meshBackoffMin, meshBackoffMax = oldMin, oldMax }()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := hub.Addr()
	rt := openRuntime(t, t.TempDir(), "bob")
	defer rt.Close()
	if _, err := rt.CreateSpace("Camp"); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartMeshtastic("tcp:" + addr); err != nil {
		t.Fatal(err)
	}
	// The radio goes away for good.
	hub.Close()
	waitUntil(t, 5*time.Second, "the node never started retrying a radio that vanished", func() bool {
		return rt.Mesh().Reconnecting
	})

	start := rt.Mesh().Attempts
	time.Sleep(time.Second)
	attempts := rt.Mesh().Attempts - start
	// Unbounded retry at this backoff floor would be ~50 in a second.
	if attempts > 20 {
		t.Errorf("%d dial attempts in one second: the backoff is not holding", attempts)
	}
	if attempts == 0 {
		t.Error("no dial attempts at all: the node gave up on the radio")
	}
	st := rt.Mesh()
	if st.Err == "" {
		t.Error("a node that cannot reach its radio reports no reason")
	}
	if st.NextRetryIn <= 0 {
		t.Error("the status does not say when the next attempt happens")
	}
}

// liveRadioLinks counts the radio links the runtime still pumps.
func liveRadioLinks(r *Runtime) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.liveLinks[TransportRadio]
}
