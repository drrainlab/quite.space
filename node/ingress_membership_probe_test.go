package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bundle"
)

// THE PROBE BEHIND not_a_member (MD-0b), asking the one question that decides
// whether bytes may be deleted:
//
//	message X arrives for a space this node is not in → refused.
//	then membership changes.
//	can THE SAME bytes X be admitted now?
//
// The suspicion is concrete: the refusal is `_, known := r.spaces[terminal]`,
// and that map GAINS ENTRIES — by joining, by creating, by a join saga that
// completes one tick after a bundle arrived. If the answer is yes, then
// not_a_member is projection state exactly like policy_refused, and deleting
// on it is the same loss wearing a third name.
func TestWhetherABundleForAnUnjoinedSpaceCanLaterBeAdmitted(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	setPersonalRelay(t, alice, addr)
	tid, err := alice.CreateSpace("the room bob has not joined")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "written before bob was a member", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// alice is live too (relay sync runs) — read her log under the lock.
	var mine [][]byte
	if err := alice.withSpace(tid, func(st *spaceState) error {
		mine = st.space.Log.FramesInRange(alice.Device.ID, 1, 1000)
		return nil
	}); err != nil {
		t.Fatal("alice lost the space")
	}
	if len(mine) == 0 {
		t.Fatal("nothing on alice's chain")
	}
	frame := mine[len(mine)-1]
	eid := id.EventIDOf(frame)
	item := bundle.Encode(tid, [][]byte{frame})

	bobDir := t.TempDir()
	bob := openRuntime(t, bobDir, "bob")
	defer bob.Close()
	setPersonalRelay(t, bob, addr)

	// BEFORE: bob is not in that space. The bytes must be KEPT — this is the
	// regression the probe below justified.
	held, err := bob.takeIngressCustody([][]byte{item}, storage.IngressRelay)
	if err != nil {
		t.Fatalf("take custody: %v", err)
	}
	if _, release := bob.applyHeldRelayItem(nil, held[0]); release {
		t.Fatal("a bundle for a space we have not joined YET was thrown away — " +
			"membership is local and mutable, so this is a projection refusal")
	}
	if left, err := reopenHold(t, bobDir).List(); err != nil || len(left) != 1 {
		t.Fatalf("held after restart = %d (err %v), want the bytes still ours", len(left), err)
	}

	// THE MEMBERSHIP CHANGE.
	pass, err := alice.MintPass(tid, 1, 1, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)

	// AFTER: offer the very same bytes again.
	hold, err := bob.ingressHold()
	if err != nil {
		t.Fatal(err)
	}
	hid, err := hold.Put(item, storage.HeldIngressMeta{ReceivedAt: 2, Source: storage.IngressRelay})
	if err != nil {
		t.Fatal(err)
	}
	// bob is a LIVE runtime here — its relay-sync goroutine judges frames
	// into this same log, so every read goes through the judge's lock.
	// (spaceForTest is only for tests that are the sole goroutine.)
	logHas := func() bool {
		var v bool
		if err := bob.withSpace(tid, func(st *spaceState) error {
			v = st.space.Log.Has(eid)
			return nil
		}); err != nil {
			t.Fatalf("bob has no space after joining: %v", err)
		}
		return v
	}
	alreadySynced := logHas()
	applied, release2 := bob.applyHeldRelayItem(nil, storage.HeldIngress{ID: hid, Raw: item})

	// THE CRITERION IS THE CHANGE OF VERDICT, not full admission. What the
	// original measurement found: before joining the bytes were LET GO, and
	// after joining the same bytes were kept and taken into the journal's own
	// ordering machinery to wait for predecessors bob does not have. Two
	// verdicts on one unchanged frame is the whole question — and the answer is
	// why the refusal above is now a keep rather than a delete.
	if release2 && !logHas() {
		t.Fatalf("MEASURED: membership changed and the verdict did not — the same "+
			"bytes are still let go (applied=%d), so not_a_member is terminal", applied)
	}
	switch {
	case alreadySynced:
		t.Log("MEASURED: once joined the same bytes are admissible — ordinary sync " +
			"delivered this copy first, which is the same verdict reached by a " +
			"second path")
	case logHas():
		t.Logf("MEASURED: after joining, the held bytes themselves were applied "+
			"(applied=%d) — not_a_member is TRANSIENT", applied)
	default:
		t.Log("MEASURED: after joining, the same bytes are no longer refused — the " +
			"journal keeps them pending its own predecessors. not_a_member is " +
			"TRANSIENT: it was knowledge about OUR membership, which is local " +
			"and mutable, never proof about the frame")
	}
}
