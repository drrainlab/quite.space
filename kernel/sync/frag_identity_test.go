package sync

import (
	"bytes"
	"testing"
)

// The reason stream ids must not collide, pinned as behaviour rather than
// asserted away: reassembly is keyed on the stream id ALONE, so two senders
// using the same id on one carrier feed ONE buffer. Whichever fragment
// reaches an index first wins and the other is discarded as a duplicate, so
// one sender's message emerges and the other vanishes without a trace —
// no error, no partial, nothing to notice.
//
// A shared radio segment delivers every node's fragments to every other
// node's reassembler, so this is the ordinary case there, not an exotic
// one. The defence is that ids do not collide (see the base test below) —
// NOT that reassembly tolerates collision. If source-scoped keys are added
// later this test will fail, and that failure is the signal to rewrite it,
// not a regression.
//
// Residual limitation, stated plainly: an attacker on the segment can pick
// a colliding id deliberately. Source-scoping would not fix that either —
// a Meshtastic source address is unauthenticated. The damage is bounded by
// what reassembly feeds: a spliced buffer either fails to decode or decodes
// to an envelope whose signature does not verify. It is a denial vector,
// never a forgery one.
func TestCollidingStreamIDsLoseAMessageSilently(t *testing.T) {
	const mtu = 64
	alice := bytes.Repeat([]byte("A"), 300)
	bob := bytes.Repeat([]byte("B"), 300)

	aliceFrags, err := FragmentStream(1, alice, mtu)
	if err != nil {
		t.Fatal(err)
	}
	bobFrags, err := FragmentStream(1, bob, mtu) // the SAME id
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceFrags) < 2 || len(bobFrags) < 2 {
		t.Fatalf("test needs multi-fragment messages: %d and %d",
			len(aliceFrags), len(bobFrags))
	}

	r := NewReassembler()
	var got [][]byte
	for i := range max(len(aliceFrags), len(bobFrags)) {
		for _, side := range [][][]byte{aliceFrags, bobFrags} {
			if i >= len(side) {
				continue
			}
			msg, err := r.Feed(side[i])
			if err != nil {
				t.Fatalf("unexpected reassembly error: %v", err)
			}
			if msg != nil {
				got = append(got, msg)
			}
		}
	}
	if len(got) != 1 {
		t.Fatalf("colliding ids produced %d messages; this test documents "+
			"that they produce exactly one. If reassembly is now "+
			"source-scoped, rewrite it to assert two clean messages", len(got))
	}
	// One sender is served, the other is silently gone. Which one depends
	// on arrival order, which is why this is a data-loss bug rather than a
	// corruption one you could detect downstream.
	survived := bytes.Equal(got[0], alice)
	lost := bytes.Equal(got[0], bob)
	if survived == lost {
		t.Fatalf("expected exactly one sender's message to survive intact; "+
			"got a %d-byte buffer matching neither", len(got[0]))
	}
}

// The actual fix: a process does not begin numbering at a fixed point.
//
// Every counter used to start at zero, so every node on a segment emitted
// stream id 1 for its first fragmented message — collision was the DEFAULT
// after boot, on exactly the medium where messages are large enough to
// fragment. A random base makes overlap require two processes' [base,
// base+sent) ranges to meet in a 2^64 space.
func TestStreamIDBaseIsNotAFixedPoint(t *testing.T) {
	// With a zero-initialised counter this is 0 and the check fails. A
	// legitimate random base lands in the low 2^32 with probability 2^-32.
	if processStreamBase>>32 == 0 {
		t.Fatalf("stream ids begin at %d: every node on a segment would "+
			"collide on its first fragmented message", processStreamBase)
	}
	first, second := NextStreamID(), NextStreamID()
	if second != first+1 {
		t.Fatalf("ids not monotonic within a process: %d then %d", first, second)
	}
	if first <= processStreamBase {
		t.Fatalf("id %d did not advance past the base %d", first, processStreamBase)
	}
}
