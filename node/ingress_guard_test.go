package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

func TestRejectedRingTTLAndEviction(t *testing.T) {
	r := newRejectedRing()
	base := time.Unix(1_700_000_000, 0)
	var a, b id.EventID
	a[0], b[0] = 1, 2

	r.remember(a, base)
	if !r.has(a, base) {
		t.Fatal("freshly remembered id not present")
	}
	// Expired after the TTL.
	if r.has(a, base.Add(ingressRejectedTTL+time.Second)) {
		t.Fatal("expired id still reported present")
	}
	// remember is idempotent; unknown id absent.
	r.remember(b, base)
	if r.has(a, base) {
		t.Fatal("id should have been lazily expired and dropped")
	}
	if !r.has(b, base) {
		t.Fatal("second id missing")
	}

	// Size eviction: overflow drops the oldest.
	r2 := newRejectedRing()
	first := id.EventID{}
	first[0] = 0xFF
	r2.remember(first, base)
	for i := 0; i < ingressRejectedMax+10; i++ {
		var e id.EventID
		e[0] = byte(i)
		e[1] = byte(i >> 8)
		e[2] = 0xAB
		r2.remember(e, base)
	}
	if r2.has(first, base) {
		t.Fatal("oldest entry survived overflow eviction")
	}
	if len(r2.order) > ingressRejectedMax {
		t.Fatalf("ring exceeded max: %d", len(r2.order))
	}
}

func TestAuthorBudgetCaps(t *testing.T) {
	b := newAuthorBudget(0)
	var alice, bob id.DeviceID
	alice[0], bob[0] = 0xA1, 0xB0

	// Alice fills her per-author frame cap; further frames are refused.
	for i := 0; i < ingressMaxFramesPerAuthorCycle; i++ {
		if !b.admit(alice, 10) {
			t.Fatalf("alice frame %d refused early", i)
		}
	}
	if b.admit(alice, 10) {
		t.Fatal("alice exceeded her per-author frame cap")
	}
	// Bob is unaffected by Alice's spend.
	if !b.admit(bob, 10) {
		t.Fatal("bob refused despite his own budget being free")
	}

	// Byte cap is independent of frame count.
	b2 := newAuthorBudget(0)
	if !b2.admit(alice, ingressMaxBytesPerAuthorCycle) {
		t.Fatal("first big frame refused")
	}
	if b2.admit(alice, 1) {
		t.Fatal("byte cap not enforced")
	}
}

// The reason the owner's limit is charged AFTER verification and not before.
//
// The budget key is the CLAIMED signer device, read without checking a
// signature — it has to be, since the whole point is to bound the cost of
// checking. That is harmless for the defence caps, which are loose enough
// that a forged flood only delays somebody. It would not be harmless for an
// owner's limit: forge a named contributor's device id, spend their share
// every cycle, and they are silenced by the space's own rate control.
//
// So the limit is CHECKED on the claim (an over-limit frame should cost no
// verification) and CONSUMED only by charge(), which the drain calls after
// Absorb has said the frame really is theirs.
func TestAForgedAuthorCannotSpendAVictimsAllowance(t *testing.T) {
	const limit = 8
	b := newAuthorBudget(limit)
	var victim id.DeviceID
	victim[0] = 0x71

	// An attacker pushes a flood of frames CLAIMING to be the victim. Every
	// one fails verification, so the drain never charges it.
	for i := 0; i < ingressMaxFramesPerAuthorCycle; i++ {
		if !b.withinPolicy(victim) {
			t.Fatalf("forged frame %d already ate the victim's allowance", i)
		}
		// ...Absorb refuses; no charge().
	}

	// The victim's own frames still land, all of them.
	for i := 0; i < limit; i++ {
		if !b.withinPolicy(victim) {
			t.Fatalf("the victim was silenced at their own frame %d", i)
		}
		b.charge(victim)
	}
	if b.withinPolicy(victim) {
		t.Fatal("the owner's limit did not bind once real frames were charged")
	}
}

func TestThePolicyLimitIsPerAuthorAndOptional(t *testing.T) {
	var alice, bob id.DeviceID
	alice[0], bob[0] = 0xA1, 0xB0

	b := newAuthorBudget(2)
	b.charge(alice)
	b.charge(alice)
	if b.withinPolicy(alice) {
		t.Fatal("alice exceeded the owner's limit")
	}
	if !b.withinPolicy(bob) {
		t.Fatal("bob was limited by alice's spending")
	}

	// Zero means "defence caps only" — the state of every space that never
	// touched this control, which must behave exactly as it did before.
	none := newAuthorBudget(0)
	for i := 0; i < ingressMaxFramesPerAuthorCycle*4; i++ {
		if !none.withinPolicy(alice) {
			t.Fatalf("an unlimited space refused frame %d", i)
		}
		none.charge(alice)
	}
}

// The bound duplicated into terminals (which must not import node) has to
// stay equal to the defence cap it mirrors, or a policy could ask for a
// limit looser than the ceiling that actually applies.
func TestThePolicyCeilingMatchesTheDefenceCap(t *testing.T) {
	if terminals.MaxFramesPerAuthor != ingressMaxFramesPerAuthorCycle {
		t.Fatalf("terminals says %d, the drain enforces %d",
			terminals.MaxFramesPerAuthor, ingressMaxFramesPerAuthorCycle)
	}
}
