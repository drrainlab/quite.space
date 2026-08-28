// RT-0 / T0 — the semantic invariant, written before the code that will
// satisfy it:
//
//	NO SEMANTIC OPERATION MAY DEPEND ON TWO PARTICIPANTS HAVING SELECTED
//	THE SAME RELAY. Relay selection affects routing quality, never
//	reachability semantics.
//
// Premise in every test here: Alice's ingress is relay A, Bob's is relay B,
// and at no point may correctness require A == B. These are the tests the
// wave exists to turn green; on the tree they were written against, the
// measured behaviour was total silence — two healthy nodes, one shared
// private space, zero delivery.
//
// The tests assert BEHAVIOUR (messages cross, media completes, holds are
// visible), not the route book's internals: the book is T1's shape, and
// pinning fields here would weld the suite to one implementation of the
// invariant rather than to the invariant.
package node

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

// twoRelays stands up two independent relays and two runtimes, each with its
// OWN personal relay — the default state of two strangers after measured
// auto-selection, and the state every test in this file holds throughout.
func twoRelays(t *testing.T) (alice, bob *Runtime, addrA, addrB string) {
	t.Helper()
	srvA, addrA := startRelay(t)
	t.Cleanup(func() { srvA.Close() })
	srvB, addrB := startRelay(t)
	t.Cleanup(func() { srvB.Close() })

	alice = openRuntime(t, t.TempDir(), "alice")
	t.Cleanup(func() { alice.Close() })
	bob = openRuntime(t, t.TempDir(), "bob")
	t.Cleanup(func() { bob.Close() })

	setPersonalRelay(t, alice, addrA)
	setPersonalRelay(t, bob, addrB)
	return alice, bob, addrA, addrB
}

// THE HEADLINE. Two people, two relays, one private space, and a
// conversation that crosses in both directions exactly once. The join
// already works — the pass link carries the minter's relay and the guest
// walks through it — and then, on the old model, each side pushed and pulled
// only through its own personal relay and the frames never met.
func TestPrivateSpaceCrossesDifferentIngressRoutes(t *testing.T) {
	alice, bob, addrA, _ := twoRelays(t)

	// The pass rides Alice's own relay, exactly as mintLink would build a
	// quicklink. Bob's node knows relay A only for the duration of the join.
	tid, err := alice.CreateSpace("two friends")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	if _, err := alice.Say(tid, "one, from alice", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Say(tid, "two, from bob", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, 30*time.Second, "alice's message never reached bob across two relays", func() bool {
		return countMsg(t, bob, tid, "one, from alice") >= 1
	})
	waitUntil(t, 30*time.Second, "bob's message never reached alice across two relays", func() bool {
		return countMsg(t, alice, tid, "two, from bob") >= 1
	})

	// Exactly once: hold a few more sync cycles and count again. Route
	// changes and re-pushes must be dedup'd by event id, never re-applied.
	time.Sleep(5 * time.Second)
	if n := countMsg(t, bob, tid, "one, from alice"); n != 1 {
		t.Errorf("alice's message applied %d times on bob", n)
	}
	if n := countMsg(t, alice, tid, "two, from bob"); n != 1 {
		t.Errorf("bob's message applied %d times on alice", n)
	}
}

// THE GUARD RAIL, not a red: measured green on the tree this suite was
// written against. A community already crosses relays whole — contributions
// ride the space's ingress routes, and everything else, presence included,
// comes back out through the projection (RR-5; the reply-box half was fixed
// the day the second region landed). It is in the invariant suite so the
// private-side rework cannot regress the public side: the dead-drop lane the
// rework replaces is redundant for public spaces, and redundant is only safe
// to remove while this stays green.
func TestPublicSpaceMemberCopyCrossesRoutes(t *testing.T) {
	alice, bob, addrA, _ := twoRelays(t)

	tid, err := alice.CreateSpaceWithOptions("commons", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "welcome", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := bob.OpenPublicSpace(tid, addrA); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 25*time.Second, "bob never saw the projection", func() bool {
		return msgCount(bob, tid) >= 1
	})
	if err := bob.JoinPublicSpace(tid); err != nil {
		t.Fatal(err)
	}

	// The contributor lane, both ways, still holds across relays.
	if _, err := bob.Say(tid, "hello from bob", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "bob's contribution never materialized on alice", func() bool {
		return countMsg(t, alice, tid, "hello from bob") >= 1
	})

	// And the fleeting lane: alice's presence must reach bob while it is
	// still fresh. TTL generous enough that only routing can fail it.
	if err := alice.SetPresence(tid, "around", 120); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "alice's presence never reached a member on another relay", func() bool {
		return peerPresenceCurrent(t, bob, tid)
	})
}

// peerPresenceCurrent reports whether any OTHER member's card shows a
// current presence on rt's replica of tid.
func peerPresenceCurrent(t *testing.T, rt *Runtime, tid id.TerminalID) bool {
	t.Helper()
	now := uint64(time.Now().Unix())
	current := false
	_ = rt.withSpace(tid, func(st *spaceState) error {
		for _, c := range st.space.MemberCards(now) {
			if c.Terminal == rt.Self.TerminalID {
				continue
			}
			if c.Presence.Known && c.Presence.Current {
				current = true
			}
		}
		return nil
	})
	return current
}

// Media: the request leaves through the recipient's knowledge of the holder,
// the answer comes back to where the requester listens — and neither of
// those may assume a shared relay.
func TestMediaCrossesDifferentIngressRoutes(t *testing.T) {
	alice, bob, addrA, _ := twoRelays(t)

	tid, err := alice.CreateSpace("photo album")
	if err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 200_000)
	ref := emitVisual(t, alice, tid, content, 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}

	pass, err := alice.MintPass(tid, 1, 1, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	aid := ref.PublicIDHex()
	waitUntil(t, 30*time.Second, "the visual's frame never reached bob", func() bool {
		_, err := bob.AssetStatus(tid, aid)
		return err == nil
	})
	if err := bob.RequestAsset(tid, aid); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 60*time.Second, "the photo never completed across two relays", func() bool {
		s, err := bob.AssetStatus(tid, aid)
		return err == nil && s.State == assets.StateComplete
	})
}

// One node, three spaces, three peers, three relays — the hub's own relay is
// a fourth. Every conversation must hold at once: the loop may not assume
// even ONE of its spaces shares an address with it.
func TestThreePeersOnThreeRelays(t *testing.T) {
	srvH, addrH := startRelay(t)
	defer srvH.Close()
	hub := openRuntime(t, t.TempDir(), "hub")
	defer hub.Close()
	setPersonalRelay(t, hub, addrH)

	type peer struct {
		rt   *Runtime
		tid  id.TerminalID
		name string
	}
	peers := make([]peer, 0, 3)
	for _, name := range []string{"alice", "bob", "carol"} {
		srv, addr := startRelay(t)
		t.Cleanup(func() { srv.Close() })
		rt := openRuntime(t, t.TempDir(), name)
		t.Cleanup(func() { rt.Close() })
		setPersonalRelay(t, rt, addr)

		tid, err := hub.CreateSpace("with " + name)
		if err != nil {
			t.Fatal(err)
		}
		pass, err := hub.MintPass(tid, 1, 1, addrH)
		if err != nil {
			t.Fatal(err)
		}
		req, err := rt.JoinByPass(pass.Link)
		if err != nil {
			t.Fatal(err)
		}
		waitJoin(t, rt, req, JoinReady)
		peers = append(peers, peer{rt, tid, name})
	}

	for _, p := range peers {
		if _, err := hub.Say(p.tid, "hello "+p.name, SayOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.rt.Say(p.tid, p.name+" here", SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range peers {
		p := p
		waitUntil(t, 45*time.Second, "hub's hello never reached "+p.name, func() bool {
			return countMsg(t, p.rt, p.tid, "hello "+p.name) >= 1
		})
		waitUntil(t, 45*time.Second, p.name+"'s reply never reached the hub", func() bool {
			return countMsg(t, hub, p.tid, p.name+" here") >= 1
		})
	}
}

// FAIL CLOSED, VISIBLY. A member whose STATED routes are all down is a HOLD
// with a name — never a quiet re-aim at the sender's own relay in the hope
// that somebody happens to listen there. That downgrade — from "Bob said B"
// to "my relay then" — is the exact divergence this wave removes, and the
// hope-Put is the failure mode that reads as green for twenty silent
// seconds. (A device the book knows NOTHING about takes the recorded legacy
// bootstrap instead; this test is about known-but-dead, which must not.)
func TestNoRouteHoldsVisibly(t *testing.T) {
	// Stood up by hand rather than through twoRelays: this test needs to
	// KILL relay B mid-flight, so it must own the handle.
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	setPersonalRelay(t, alice, addrA)
	setPersonalRelay(t, bob, addrB)

	tid, err := alice.CreateSpace("known but dark")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 1, 1, addrA)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
	waitUntil(t, 10*time.Second, "premise: alice must hold bob's stated route before it dies", func() bool {
		return len(alice.PeerRoutesFor(bob.Device.ID)) > 0
	})

	// Bob and his relay both die. Alice KNOWS where bob listens — addrB,
	// stated in the invitation — and that route has gone dark.
	bob.Close()
	srvB.Close()

	if _, err := alice.Say(tid, "are you still there", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// The breaker needs a few failed cycles to call the endpoint offline;
	// after that the hold must surface with a route-shaped reason, and the
	// frames must NOT have been re-aimed at alice's own relay.
	waitUntil(t, 45*time.Second, "a member whose routes are all down was not reported as held", func() bool {
		for _, h := range alice.RelaySync().Held {
			if h.SpaceID == tid.Hex() && strings.Contains(h.Reason, "route") {
				return true
			}
		}
		return false
	})
}
