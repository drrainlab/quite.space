package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// LR-1: the node-side emit gates — allowlist at emit, unkeep authorization,
// Shelf projection with restart persistence.
func TestKeepEmitGatesAndShelf(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Listening Room")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(tid, "the track everyone loved", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Keep an unknown target → rejected at emit.
	var ghost id.EventID
	ghost[0] = 0xEE
	if err := rt.Keep(tid, ghost, ""); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("ghost keep must be rejected, got %v", err)
	}

	// Keep the message with a note.
	if err := rt.Keep(tid, eid, "our anthem"); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	shelf := sp.State.Shelf()
	if len(shelf) != 1 || shelf[0].Kind != "text" {
		t.Fatalf("shelf wrong: %+v", shelf)
	}
	if shelf[0].Keepers[0].Note != "our anthem" {
		t.Fatalf("note lost: %+v", shelf[0].Keepers)
	}

	// Unkeep for someone else while NOT controller of their keep → but we
	// are the controller here, so forge a non-controller check by asking to
	// unkeep for a random principal: allowed only because we own the space.
	// The negative case (member ≠ controller) is covered by the reducer test;
	// here we verify the self path + persistence.
	if err := rt.Unkeep(tid, eid, rt.Principal.ID); err != nil {
		t.Fatal(err)
	}
	if len(sp.State.Shelf()) != 0 {
		t.Fatal("unkeep did not clear shelf")
	}

	// Re-keep, then restart: the Shelf must survive a full reload.
	if err := rt.Keep(tid, eid, "still ours"); err != nil {
		t.Fatal(err)
	}
	rt.Close()
	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	sp2, ok := rt2.spaceForTest(tid)
	if !ok {
		t.Fatal("space lost")
	}
	shelf2 := sp2.State.Shelf()
	if len(shelf2) != 1 || shelf2[0].Keepers[0].Note != "still ours" {
		t.Fatalf("shelf lost across restart: %+v", shelf2)
	}
}

// Note length gate at the API/runtime boundary.
func TestKeepNoteBound(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, _ := rt.CreateSpace("s")
	eid, _ := rt.Say(tid, "m", SayOptions{})
	long := strings.Repeat("x", 501)
	if err := rt.Keep(tid, eid, long); err == nil {
		t.Fatal("over-long note must be rejected")
	}
}
