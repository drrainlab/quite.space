// T6-LAN / L0 — the headline, red first (owner's spec, plan T6):
//
//	A LOCAL GROUP STOPS USING THE RELAY FOR LOCAL RECIPIENTS.
//
// LAN is not "the space switches to LAN" — it is a more preferred route
// candidate for exactly those recipients the client directly observes
// locally. One event's fanout splits per destination: local recipients ride
// the LAN link, remote recipients ride the relay, and the relay receives NO
// member copies for the local ones. When the LAN disappears, delivery
// returns to the relay by itself — same relationship, no reinvite, no rekey.
//
// The relay's Fetch is non-destructive, so the test observes mailboxes from
// the outside: the assertion is a DELTA — a local recipient's mailbox must
// not grow while their device is provably on the wire with us.
package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// mailboxCount asks the relay, as an outside observer, how many items sit in
// one recipient's mailbox for one space right now (current bucket).
func mailboxCount(t *testing.T, addr string, tid id.TerminalID, dev id.DeviceID) int {
	t.Helper()
	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatalf("observer could not reach the relay: %v", err)
	}
	defer client.Close()
	items, err := client.Fetch([][]byte{relay.HintFor(tid, dev, relayBucketNow())})
	if err != nil {
		t.Fatalf("observer fetch failed: %v", err)
	}
	return len(items)
}

func TestLocalGroupStopsUsingRelayForLocalRecipients(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	open := func(name string) *Runtime {
		rt := openRuntime(t, t.TempDir(), name)
		t.Cleanup(func() { rt.Close() })
		setPersonalRelay(t, rt, addr)
		return rt
	}
	alice := open("alice")
	bob := open("bob")
	carol := open("carol")
	dave := open("dave")

	tid, err := alice.CreateSpace("one roof and one far away")
	if err != nil {
		t.Fatal(err)
	}
	for _, guest := range []*Runtime{bob, carol, dave} {
		pass, err := alice.MintPass(tid, 1, 1, addr)
		if err != nil {
			t.Fatal(err)
		}
		req, err := guest.JoinByPass(pass.Link)
		if err != nil {
			t.Fatal(err)
		}
		waitJoin(t, guest, req, JoinReady)
	}

	// Phase 1: everybody communicates through the relay.
	if _, err := alice.Say(tid, "через реле", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		rt   *Runtime
		name string
	}{{bob, "bob"}, {carol, "carol"}, {dave, "dave"}} {
		p := p
		waitUntil(t, 30*time.Second, p.name+" never got the relay-era message", func() bool {
			return countMsg(t, p.rt, tid, "через реле") >= 1
		})
	}

	// Phase 2: bob and carol walk into the same room as alice. Their relay
	// pull stops entirely so the mailbox observation below cannot race a
	// consumer — from here, anything they receive came over the wire. Dave's
	// loop stops too, for a different reason: every replica re-offers a
	// grown log to every member (the mesh's redundancy), so a live dave
	// would copy alice's event into bob's mailbox himself and the assertion
	// would measure DAVE's generosity, not ALICE's routing. The headline is
	// about one event's fanout from its author; dave here is a pure
	// listener whose mailbox growth is the relay-path proof.
	stopRelay := func(rt *Runtime) {
		s := rt.GetSettings()
		s.Relay = ""
		if err := rt.SetSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	stopRelay(bob)
	stopRelay(carol)
	stopRelay(dave)

	if err := alice.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("LAN listener unavailable in this sandbox: %v", err)
	}
	for _, rt := range []*Runtime{bob, carol} {
		if err := rt.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
			t.Skipf("LAN listener unavailable in this sandbox: %v", err)
		}
	}
	lanAddr := fmt.Sprintf("127.0.0.1:%d", alice.LAN().Port)
	if err := bob.ConnectPeer(lanAddr); err != nil {
		t.Fatal(err)
	}
	if err := carol.ConnectPeer(lanAddr); err != nil {
		t.Fatal(err)
	}
	// The route candidate exists only once the peer is AUTHENTICATED on the
	// link — a TLS socket alone names nobody.
	waitUntil(t, 15*time.Second, "alice never authenticated bob's device on the LAN link", func() bool {
		return alice.lanPeerDevice(bob.Device.ID)
	})
	waitUntil(t, 15*time.Second, "alice never authenticated carol's device on the LAN link", func() bool {
		return alice.lanPeerDevice(carol.Device.ID)
	})

	bobBefore := mailboxCount(t, addr, tid, bob.Device.ID)
	carolBefore := mailboxCount(t, addr, tid, carol.Device.ID)
	daveBefore := mailboxCount(t, addr, tid, dave.Device.ID)

	// One event, split fanout: bob → LAN, carol → LAN, dave → relay.
	if _, err := alice.Say(tid, "мы в одной комнате", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "bob never received over the LAN", func() bool {
		return countMsg(t, bob, tid, "мы в одной комнате") >= 1
	})
	waitUntil(t, 30*time.Second, "carol never received over the LAN", func() bool {
		return countMsg(t, carol, tid, "мы в одной комнате") >= 1
	})
	// Dave has no LAN link and his loop is off: his mailbox growing is the
	// relay-path half of the split fanout, and a manual pull confirms the
	// payload is real.
	waitUntil(t, 30*time.Second, "alice never delivered dave's copy to the relay", func() bool {
		return mailboxCount(t, addr, tid, dave.Device.ID) > daveBefore
	})
	if _, err := dave.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if countMsg(t, dave, tid, "мы в одной комнате") < 1 {
		t.Fatal("dave's relay copy did not carry the event")
	}

	// Hold a few more sync cycles: the local recipients' mailboxes must not
	// have grown — no member copy, and no "parallel copy just in case".
	time.Sleep(5 * time.Second)
	if n := mailboxCount(t, addr, tid, bob.Device.ID); n > bobBefore {
		t.Errorf("the relay carried a member copy for bob while he was on the wire (%d → %d)", bobBefore, n)
	}
	if n := mailboxCount(t, addr, tid, carol.Device.ID); n > carolBefore {
		t.Errorf("the relay carried a member copy for carol while she was on the wire (%d → %d)", carolBefore, n)
	}

	// Phase 3: the room empties. Both links die; the next event must return
	// to the relay by itself — same relationship, no reinvite.
	bob.StopLAN()
	carol.StopLAN()
	waitUntil(t, 20*time.Second, "alice kept believing the dead LAN links", func() bool {
		return !alice.lanPeerDevice(bob.Device.ID) && !alice.lanPeerDevice(carol.Device.ID)
	})

	if _, err := alice.Say(tid, "разошлись по домам", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "delivery never returned to the relay for bob", func() bool {
		return mailboxCount(t, addr, tid, bob.Device.ID) > bobBefore
	})
	waitUntil(t, 30*time.Second, "delivery never returned to the relay for carol", func() bool {
		return mailboxCount(t, addr, tid, carol.Device.ID) > carolBefore
	})

	// And the people behind the mailboxes actually catch up, exactly once.
	restoreRelay := func(rt *Runtime) {
		s := rt.GetSettings()
		s.Relay = addr
		if err := rt.SetSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	restoreRelay(bob)
	restoreRelay(carol)
	for _, text := range []string{"мы в одной комнате", "разошлись по домам"} {
		text := text
		waitUntil(t, 30*time.Second, "bob never converged after the room emptied: "+text, func() bool {
			return countMsg(t, bob, tid, text) >= 1
		})
		waitUntil(t, 30*time.Second, "carol never converged after the room emptied: "+text, func() bool {
			return countMsg(t, carol, tid, text) >= 1
		})
	}
	for _, text := range []string{"через реле", "мы в одной комнате", "разошлись по домам"} {
		if n := countMsg(t, bob, tid, text); n != 1 {
			t.Errorf("bob applied %q %d times", text, n)
		}
		if n := countMsg(t, carol, tid, text); n != 1 {
			t.Errorf("carol applied %q %d times", text, n)
		}
	}
}
