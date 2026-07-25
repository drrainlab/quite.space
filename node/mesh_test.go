package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// Full-stack M1.7 proof: two node runtimes, a private space with an invite,
// and convergence over a (fake) Meshtastic mesh — encrypted payloads
// fragmented into ≤200-byte packets on a private app port. Green here means
// the protocol works over the mesh; real LoRa adds loss and airtime, which
// the sync layer already tolerates (M0.6 simulator profiles).
func TestTwoNodesSyncOverMeshtastic(t *testing.T) {
	// Accelerate the LoRa cadence for the test.
	oldPump, oldSummary := meshPumpEvery, meshSummaryEvery
	meshPumpEvery, meshSummaryEvery = 30*time.Millisecond, 200*time.Millisecond
	defer func() { meshPumpEvery, meshSummaryEvery = oldPump, oldSummary }()

	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()

	tid, err := rtA.CreateSpace("Off-grid Camp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtA.Say(tid, "meet at the ridge at dawn", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	invite, err := rtA.MintInvite(tid, rtB.Device.ID, rtB.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtB.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}

	// Both nodes attach radios to the same mesh.
	if err := rtA.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	if err := rtB.StartMeshtastic("tcp:" + hub.Addr()); err != nil {
		t.Fatal(err)
	}
	if !rtA.Mesh().Connected || !rtB.Mesh().Connected {
		t.Fatal("radios not connected")
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		if msgCount(rtB, tid) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no convergence over mesh: B has %d events, mesh A %+v B %+v",
				logLen(rtB, tid), rtA.Mesh(), rtB.Mesh())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := msgTexts(rtB, tid); got[0] != "meet at the ridge at dawn" {
		t.Fatalf("message mangled over mesh: %q", got[0])
	}

	// Reply back over the same radio link.
	if _, err := rtB.Say(tid, "copy, bringing the recorder", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	for {
		if msgCount(rtA, tid) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reply did not arrive over mesh")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tx, rx := rtA.Mesh().TX, rtA.Mesh().RX; tx == 0 || rx == 0 {
		t.Fatalf("mesh counters implausible: tx=%d rx=%d", tx, rx)
	}
}
