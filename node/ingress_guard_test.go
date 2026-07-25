package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
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
	b := newAuthorBudget()
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
	b2 := newAuthorBudget()
	if !b2.admit(alice, ingressMaxBytesPerAuthorCycle) {
		t.Fatal("first big frame refused")
	}
	if b2.admit(alice, 1) {
		t.Fatal("byte cap not enforced")
	}
}
