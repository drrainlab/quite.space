// T6-LAN / L1 — the observed-route invariants, pinned (owner's spec):
//
//   observed LAN route:
//     ✓ bound to an authenticated DeviceID
//     ✓ lives only while directly observed
//     ✓ never encoded into the keystore
package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

// The hello, both directions: after one ConnectPeer each side holds an
// authenticated binding for the other's DEVICE — not for an address, not
// for a TLS socket, for the key.
func TestLANHelloBindsBothDevices(t *testing.T) {
	a := openRuntime(t, t.TempDir(), "a")
	defer a.Close()
	b := openRuntime(t, t.TempDir(), "b")
	defer b.Close()
	if err := a.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := b.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := b.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", a.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "the acceptor never authenticated the dialer", func() bool {
		return a.lanPeerDevice(b.Device.ID)
	})
	waitUntil(t, 10*time.Second, "the dialer never authenticated the acceptor", func() bool {
		return b.lanPeerDevice(a.Device.ID)
	})
}

// THE DEATH TEST (owner's name for it). After a restart the LAN peer must
// be re-discovered, never resurrected as a 192.168.x.x the client somehow
// still trusts. The binding is memory; the keystore must hold NO route
// whose transport is anything but a relay.
func TestObservedLANRouteIsNotPersistedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	a := openRuntime(t, dir, "a")
	b := openRuntime(t, t.TempDir(), "b")
	defer b.Close()
	if err := a.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := b.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := b.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", a.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "the binding never formed", func() bool {
		return a.lanPeerDevice(b.Device.ID)
	})

	// While the binding is LIVE, the keystore already holds nothing about
	// it — the invariant is "never encoded", not "cleaned up eventually".
	assertNoLANRoutes := func(rt *Runtime, when string) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		for dev, routes := range rt.ks.PeerRoutes {
			for _, route := range routes {
				if route.Transport != "relay" {
					t.Fatalf("%s: a %q-transport route for %s is in the keystore: %+v",
						when, route.Transport, dev.Hex()[:8], route)
				}
			}
		}
		for _, route := range rt.ks.SelfIngress {
			if route.Transport != "relay" {
				t.Fatalf("%s: a %q-transport ingress is in the keystore: %+v",
					when, route.Transport, route)
			}
		}
	}
	assertNoLANRoutes(a, "while live")

	// Restart. The device must come back knowing NOTHING about the wire.
	a.Close()
	a2 := openRuntime(t, dir, "a")
	defer a2.Close()
	if a2.lanPeerDevice(b.Device.ID) {
		t.Fatal("an observed LAN binding survived a restart")
	}
	assertNoLANRoutes(a2, "after restart")
}

// Lives only while observed: the binding dies WITH the link, not with a
// timetable — and delivery notices the same tick.
func TestLANBindingDiesWithTheLink(t *testing.T) {
	a := openRuntime(t, t.TempDir(), "a")
	defer a.Close()
	b := openRuntime(t, t.TempDir(), "b")
	defer b.Close()
	if err := a.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := b.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := b.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", a.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "the binding never formed", func() bool {
		return a.lanPeerDevice(b.Device.ID) && b.lanPeerDevice(a.Device.ID)
	})
	b.StopLAN()
	waitUntil(t, 15*time.Second, "a dead link's binding survived it", func() bool {
		return !a.lanPeerDevice(b.Device.ID)
	})
}

// One event over two carriers is ONE event. Alice hands the same frames to
// the relay (an explicit manual push — exactly the path that never
// offloads) while the LAN pump converges the same log; bob's replica must
// count the message once. The event log's dedup owns this; the test pins
// it from the transport side so switching carriers can never mint a
// second reality.
func TestSameEventArrivingOverTwoTransportsIsAppliedExactlyOnce(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, alice, addr)
	setPersonalRelay(t, bob, addr)

	tid, err := alice.CreateSpace("двойная дорога")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	if err := alice.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := bob.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("no LAN in this sandbox: %v", err)
	}
	if err := bob.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", alice.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "no binding", func() bool {
		return alice.lanPeerDevice(bob.Device.ID)
	})

	if _, err := alice.Say(tid, "одно событие", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// Force the relay copy explicitly, alongside whatever the wire does.
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "the event never arrived at all", func() bool {
		return countMsg(t, bob, tid, "одно событие") >= 1
	})
	time.Sleep(5 * time.Second) // let both carriers finish delivering
	if n := countMsg(t, bob, tid, "одно событие"); n != 1 {
		t.Fatalf("one event over two transports applied %d times", n)
	}
}

// keystoreRoutesOnlyRelay is referenced by the death test; keep the
// storage import honest even if assertions above change shape.
var _ = storage.Route{}
