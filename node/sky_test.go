package node

// SK-1 end to end: a sky started on one node, a stroke drawn on another,
// the picture identical on both — and an erase that only removes the
// eraser's own stroke.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func strokesOf(t *testing.T, rt *Runtime, tid id.TerminalID, sky id.EventID) int {
	t.Helper()
	n := 0
	_ = rt.withSpace(tid, func(st *spaceState) error {
		n = len(st.space.State.SkyStrokes(sky))
		return nil
	})
	return n
}

func TestASkyIsDrawnTogetherAndErasedAlone(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)

	tid, err := alice.CreateSpace("orbit")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(tid, 2, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	payload, _ := (&schemas.SkyBlock{Title: "nebula"}).Encode()
	sky, err := alice.EmitBlock(tid, schemas.BlockSky, payload)
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]*Runtime{"alice": alice, "bob": bob}
	addrs := map[string]string{"alice": addr, "bob": addr}
	deadline := time.Now().Add(30 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if countKind(t, bob, tid, "sky") >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sky never reached bob")
		}
	}

	// Each draws one stroke.
	sa, _ := (&schemas.SkyStrokeEvent{Sky: sky, Points: []byte{1, 1, 2, 2}, Bright: 3}).Encode()
	if _, err := alice.EmitBlock(tid, schemas.SkyStroke, sa); err != nil {
		t.Fatal(err)
	}
	sb, _ := (&schemas.SkyStrokeEvent{Sky: sky, Points: []byte{9, 9, 8, 8}, Bright: 1}).Encode()
	bobStroke, err := bob.EmitBlock(tid, schemas.SkyStroke, sb)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if strokesOf(t, alice, tid, sky) == 2 && strokesOf(t, bob, tid, sky) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pictures differ: alice %d bob %d", strokesOf(t, alice, tid, sky), strokesOf(t, bob, tid, sky))
		}
	}

	// Alice tries to erase bob's stroke: refused everywhere. Bob erases
	// his own: gone everywhere.
	ea, _ := (&schemas.SkyStrokeEvent{Sky: sky, Erase: []id.EventID{bobStroke}}).Encode()
	if _, err := alice.EmitBlock(tid, schemas.SkyStroke, ea); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		convergeTick(nodes, addrs)
	}
	if strokesOf(t, alice, tid, sky) != 2 || strokesOf(t, bob, tid, sky) != 2 {
		t.Fatal("somebody erased another person's stroke")
	}
	eb, _ := (&schemas.SkyStrokeEvent{Sky: sky, Erase: []id.EventID{bobStroke}}).Encode()
	if _, err := bob.EmitBlock(tid, schemas.SkyStroke, eb); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		convergeTick(nodes, addrs)
		if strokesOf(t, alice, tid, sky) == 1 && strokesOf(t, bob, tid, sky) == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bob's erase never converged: alice %d bob %d", strokesOf(t, alice, tid, sky), strokesOf(t, bob, tid, sky))
		}
	}
}

func countKind(t *testing.T, rt *Runtime, tid id.TerminalID, kind string) int {
	t.Helper()
	n := 0
	_ = rt.withSpace(tid, func(st *spaceState) error {
		for _, e := range st.space.State.Entries() {
			if string(e.Kind) == kind {
				n++
			}
		}
		return nil
	})
	return n
}
