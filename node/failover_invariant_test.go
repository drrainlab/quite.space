// The established-space failover invariant, pinned.
//
// When two devices already share a space and become reachable over a
// compatible radio segment, Quite continues syncing THAT space. It creates no
// invitation and no pass, changes no membership, rotates no epoch, and asks
// the person for nothing. A contact is made once; radio does not remake it —
// it gives an existing relation another physical form.
//
// Mostly true by construction — the radio pump carries sync for spaces the
// node already holds and mints nothing — which is exactly why it needs a
// GUARD rather than a mechanism: nothing else would notice if a later change
// made a radio path re-invite or re-key.
package node

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/radiotransfer"
)

// radioBetween wires a lossless radio segment between two runtimes, the
// production shape (radiotransfer over a datagram carrier), and returns a
// disposer.
func radioBetween(t *testing.T, alice, bob *Runtime, seedPhrase string) func() {
	t.Helper()
	seed := sha256.Sum256([]byte(seedPhrase))
	key, err := radiotransfer.DeriveTransferKey(seed[:], radiotransfer.KDFVersion)
	if err != nil {
		t.Fatal(err)
	}
	aAir, bAir := newSegment(200, 0, 7)
	lim := radiotransfer.Limits{Window: 4, MaxRounds: 6,
		AckTimeout: 300 * time.Millisecond, SACKDelay: 10 * time.Millisecond,
		SendFloor: 5 * time.Millisecond, FrameGap: time.Millisecond}
	aEP, err := radiotransfer.Wrap(aAir, key, radiotransfer.EndpointOptions{
		Options: radiotransfer.Options{Limits: lim}})
	if err != nil {
		t.Fatal(err)
	}
	bEP, err := radiotransfer.Wrap(bAir, key, radiotransfer.EndpointOptions{
		Options: radiotransfer.Options{Limits: lim}})
	if err != nil {
		t.Fatal(err)
	}
	alice.adoptLink(endpointLink{aEP}, 50*time.Millisecond, time.Second, "radio")
	bob.adoptLink(endpointLink{bEP}, 50*time.Millisecond, time.Second, "radio")
	return func() { aEP.Close(); bEP.Close() }
}

// relationSnapshot is everything the invariant says must NOT move.
type relationSnapshot struct {
	invitations int
	passes      int
	joins       int
	epoch       uint64
	members     int
}

func snapshotRelation(t *testing.T, rt *Runtime, tid id.TerminalID) relationSnapshot {
	t.Helper()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st, ok := rt.spaces[tid]
	if !ok {
		t.Fatalf("the space is not held")
	}
	return relationSnapshot{
		invitations: len(rt.loadQuickLinks().Invitations),
		passes:      len(rt.ks.Passes),
		joins:       len(rt.ks.Joins),
		epoch:       st.space.CurrentEpoch(),
		members:     len(st.space.Members()),
	}
}

// The headline: a contact made over the ordinary path RESUMES over radio.
// Nothing is re-invited, re-keyed or re-confirmed — the same space, the same
// membership, the same epoch, and the very same EventID on both sides.
func TestAnExistingRelayContactResumesOverRadioWithoutInvitationOrRekey(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	// The contact, made ONCE, the ordinary way.
	tid := shareASpace(t, alice, bob, "made once")

	before := snapshotRelation(t, alice, tid)
	beforeBob := snapshotRelation(t, bob, tid)

	// The internet is gone; the radios come up. Nobody presses anything.
	closeRadio := radioBetween(t, alice, bob, "one segment from the invitation")
	defer closeRadio()

	said := "the same conversation, in another physical form"
	eid, err := alice.Say(tid, said, SayOptions{})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for !heard(t, bob, tid, said) {
		if time.Now().After(deadline) {
			t.Fatal("the existing space did not resume over the radio")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The same EVENT, not a re-told copy: bob holds the exact signed frame.
	bob.mu.Lock()
	_, sameEvent := bob.spaces[tid].space.State.EntryByID(eid)
	bob.mu.Unlock()
	if !sameEvent {
		t.Fatalf("the event arrived under a different identity than %s", eid)
	}

	// And the relation did not move. Alice is the authority, and for her the
	// claim is strict equality: no invitation, no pass, no join saga, no NEW
	// epoch, no membership change.
	afterAlice := snapshotRelation(t, alice, tid)
	if afterAlice != before {
		t.Fatalf("alice's relation moved when the radio came up:\n before %+v\n after  %+v\n"+
			"— the failover re-made a contact that already existed", before, afterAlice)
	}
	// Bob may CATCH UP to truth that predated the radio — the mint-time
	// rotation travels to him over his first-ever link, and history arriving
	// is not history being remade. What he may not do is mint anything of
	// his own or move PAST the authority.
	afterBob := snapshotRelation(t, bob, tid)
	if afterBob.invitations != beforeBob.invitations ||
		afterBob.passes != beforeBob.passes || afterBob.joins != beforeBob.joins {
		t.Fatalf("bob minted records when the radio came up:\n before %+v\n after  %+v",
			beforeBob, afterBob)
	}
	if afterBob.epoch != afterAlice.epoch {
		t.Fatalf("bob's epoch %d did not converge to the authority's %d — either "+
			"a failover rekey or a fork", afterBob.epoch, afterAlice.epoch)
	}
}

// The other direction of the same invariant: an event that travelled by
// radio does not reappear when a wider path returns. One EventID, one entry,
// however many carriers replayed it.
func TestReturningInternetDoesNotDuplicateRadioDeliveredEvents(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid := shareASpace(t, alice, bob, "no echoes")

	closeRadio := radioBetween(t, alice, bob, "the radio leg")
	defer closeRadio()

	said := "delivered once, whatever carries it later"
	if _, err := alice.Say(tid, said, SayOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for !heard(t, bob, tid, said) {
		if time.Now().After(deadline) {
			t.Fatal("the radio leg never delivered")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// "The internet returns": a second, faster path between the same two
	// nodes, carrying the same space — and with it, re-offers of everything
	// the radio already delivered.
	closeSecond := radioBetween(t, alice, bob, "the returning wide path")
	defer closeSecond()

	// Let several sync cycles run on both paths.
	time.Sleep(2 * time.Second)

	count := 0
	for _, got := range bobEntries(t, bob, tid) {
		if got == said {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the message exists %d times after the second path came up — "+
			"the EventID dedup did not hold across carriers", count)
	}
}
