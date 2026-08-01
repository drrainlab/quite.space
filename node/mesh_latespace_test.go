// A space created AFTER a link was adopted must still be carried by it.
//
// The radio is adopted exactly once, at startup, when the space set is
// usually EMPTY — that is deliberate (a replugged device must not stack
// links). Before this test the pump snapshotted the space set at that
// moment and iterated the snapshot forever, so everything a person made
// during the session was carried by nothing and received by nothing over
// the radio until both sides restarted. Found by a live fake-mesh run:
// mesh tx stayed 0 until a restart, after which the same message flowed.
package node

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/meshtastic"
)

// meshPair boots two runtimes already attached to one fake mesh hub,
// with NO relay and NO LAN — the radio is the only road out.
func meshPair(t *testing.T) (*Runtime, *Runtime) {
	t.Helper()
	hub, err := meshtastic.StartHub("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	addr := hub.Addr()

	alice := openRuntime(t, t.TempDir(), "alice")
	t.Cleanup(alice.Close)
	bob := openRuntime(t, t.TempDir(), "bob")
	t.Cleanup(bob.Close)
	for _, rt := range []*Runtime{alice, bob} {
		if err := rt.StartMeshtastic("tcp:" + addr); err != nil {
			t.Fatal(err)
		}
	}
	return alice, bob
}

func TestASpaceCreatedAfterTheRadioIsAdoptedStillSyncs(t *testing.T) {
	alice, bob := meshPair(t)

	// The space is made now — with the radio already adopted and no other
	// transport in existence. Nothing may be restarted.
	tid, err := alice.CreateSpace("радио-комната")
	if err != nil {
		t.Fatal(err)
	}
	st := bob.GetSettings()
	if st.Relay != "" {
		t.Fatal("this test must prove the RADIO carried it, not a relay")
	}
	inv, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(inv); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "по радио, без рестарта", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range textsOf(t, bob, tid) {
			if strings.Contains(s, "без рестарта") {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the message never crossed the radio; bob holds %q", textsOf(t, bob, tid))
}

// The peer-facing half of the same bug: a space made after adoption must
// SEE the link, or media fetch reports "no peers" and goes relay-only.
func TestALateSpaceSeesTheAlreadyAdoptedLink(t *testing.T) {
	alice, _ := meshPair(t)
	tid, err := alice.CreateSpace("поздняя")
	if err != nil {
		t.Fatal(err)
	}
	var peers int
	alice.mu.Lock()
	if st, ok := alice.spaces[tid]; ok {
		peers = len(st.conns)
	}
	alice.mu.Unlock()
	if peers == 0 {
		t.Fatal("a space created after adoption sees no links — media fetch " +
			"would call itself peerless with a radio right there")
	}
}
