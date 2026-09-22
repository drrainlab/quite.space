package node

// The durable offer book (LT-4 S5): what a restart no longer costs, what a
// mailbox mark is keyed by, when a mark expires, and the one invariant —
// the cursor never moves ahead of the relay's acceptance.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// pairAt is pairOnRelay with alice's data directory chosen by the test, so
// she can be closed and opened again from the same place.
func pairAt(t *testing.T, addr, aliceDir string) (alice, bob *Runtime, tid id.TerminalID) {
	t.Helper()
	alice = openRuntime(t, aliceDir, "alice")
	setPersonalRelay(t, alice, addr)
	bob = openRuntime(t, t.TempDir(), "bob")
	setPersonalRelay(t, bob, addr)
	var err error
	tid, err = alice.CreateSpace("книга")
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
	return alice, bob, tid
}

func putsTotal(srv *relayserver.Server) int64 {
	return int64(srv.StatusSnapshot("", "").Traffic.PutsTotal)
}

// A restart re-offers NOTHING that the mailbox already holds: the book
// remembers, and the peer's signed receipts floor my own chain besides.
func TestARestartDoesNotRemailTheHistory(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	dir := t.TempDir()
	alice, bob, tid := pairAt(t, addr, dir)
	defer bob.Close()
	for i := 0; i < 5; i++ {
		if _, err := alice.Say(tid, "слово "+string(rune('а'+i)), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 30*time.Second, "bob never got the five words", func() bool {
		return countMsg(t, bob, tid, "слово д") >= 1
	})
	time.Sleep(4 * cadence) // let the book settle its marks
	alice.Close()

	putsBefore := putsTotal(srv)
	mbBefore := mailboxCount(t, addr, tid, bob.Device.ID)
	alice2 := openRuntime(t, dir, "alice")
	defer alice2.Close()
	if alice2.offerBook == nil || alice2.offerBook.size() == 0 {
		t.Fatal("the book did not come back from disk")
	}
	time.Sleep(8 * cadence) // several cycles and the announce on open
	if got := mailboxCount(t, addr, tid, bob.Device.ID); got != mbBefore {
		t.Fatalf("bob's mailbox grew across alice's restart (%d → %d): the history was re-mailed", mbBefore, got)
	}
	if d := putsTotal(srv) - putsBefore; d > 2 {
		t.Fatalf("%d puts after a restart with nothing new to say", d)
	}
	// And a new word is exactly one delta.
	p := putsTotal(srv)
	if _, err := alice2.Say(tid, "после перезапуска", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "the word after the restart did not arrive", func() bool {
		return countMsg(t, bob, tid, "после перезапуска") >= 1
	})
	if d := putsTotal(srv) - p; d > 2 {
		t.Fatalf("a single word cost %d puts", d)
	}
}

// A mark names a MAILBOX — (space, recipient, endpoint). A device guessed
// at two relays has two marks; the second pass puts nothing on either.
func TestOfferMarksAreKeyedPerEndpoint(t *testing.T) {
	srvA, addrA := startRelay(t)
	defer srvA.Close()
	srvB, addrB := startRelay(t)
	defer srvB.Close()
	alice, bob, tid := pairOnRelay(t, addrA)
	defer alice.Close()
	defer bob.Close()
	alice.mu.Lock()
	delete(alice.ks.PeerRoutes, bob.Device.ID)
	alice.guessRelaysOverride = []string{addrA, addrB}
	alice.mu.Unlock()
	alice.resetOffers()
	if _, err := alice.Say(tid, "на оба", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	alice.relaySyncOnce(addrA)
	a1, b1 := putsTotal(srvA), putsTotal(srvB)
	if a1 == 0 || b1 == 0 {
		t.Fatalf("the guess did not lay a copy on both relays (%d, %d)", a1, b1)
	}
	if got := alice.offerBook.size(); got < 2 {
		t.Fatalf("book holds %d marks for two mailboxes", got)
	}
	alice.relaySyncOnce(addrA)
	alice.relaySyncOnce(addrA)
	if putsTotal(srvA)-a1 > 1 || putsTotal(srvB)-b1 > 1 {
		t.Fatalf("a quiet space kept re-mailing: A +%d, B +%d", putsTotal(srvA)-a1, putsTotal(srvB)-b1)
	}
}

// A day after the last FULL offer the mailbox is laid again from the
// start — whatever deltas were put since — because the relay may have
// forgotten it. Then quiet again.
func TestAMarkUntouchedForADayIsReoffered(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	off := false
	if err := bob.SetSettings(Settings{Relay: addr, DeliveryReceipts: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "до", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "bob never got the word", func() bool {
		return countMsg(t, bob, tid, "до") >= 1
	})
	alice.relaySyncOnce(addr)
	quiet := putsTotal(srv)
	alice.relaySyncOnce(addr)
	if putsTotal(srv)-quiet > 1 {
		t.Fatalf("a converged space re-mailed (%d puts)", putsTotal(srv)-quiet)
	}
	alice.offerBook.setFullAt(offerKey{tid, bob.Device.ID, addr}, time.Now().Add(-25*time.Hour))
	alice.relaySyncOnce(addr)
	if putsTotal(srv)-quiet < 1 {
		t.Fatal("a day-old mailbox was not laid again")
	}
	again := putsTotal(srv)
	alice.relaySyncOnce(addr)
	if putsTotal(srv)-again > 1 {
		t.Fatalf("the re-offer did not settle (%d more puts)", putsTotal(srv)-again)
	}
}

// A book that does not decode is an empty book: one full re-offer, deduped,
// and the node opens as usual.
func TestABookThatDoesNotDecodeIsAnEmptyBook(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	dir := t.TempDir()
	alice, bob, tid := pairAt(t, addr, dir)
	defer bob.Close()
	if _, err := alice.Say(tid, "до порчи", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "bob never got the word", func() bool {
		return countMsg(t, bob, tid, "до порчи") >= 1
	})
	alice.Close()
	root := alice.root
	if err := root.SaveSealed(offerBookName, []byte("this is not a book")); err != nil {
		t.Fatal(err)
	}
	alice2 := openRuntime(t, dir, "alice")
	defer alice2.Close()
	if alice2.offerBook == nil || alice2.offerBook.size() != 0 {
		t.Fatal("a corrupt book did not start empty")
	}
	if _, err := alice2.Say(tid, "после порчи", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "the word after the corrupt book did not arrive", func() bool {
		return countMsg(t, bob, tid, "после порчи") >= 1
	})
	if countMsg(t, bob, tid, "до порчи") != 1 {
		t.Fatal("the re-offer duplicated a word at the receiver")
	}
}

// THE INVARIANT: the cursor moves only after the relay accepted every body
// of the mailbox's group. A push that fails leaves it where it was, and
// when the relay is back everything from there is laid — nothing skipped.
func TestAFailedPushLeavesTheCursorWhereItWas(t *testing.T) {
	srv, addr := startRelay(t)
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	if _, err := alice.Say(tid, "пока реле живо", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "bob never got the first word", func() bool {
		return countMsg(t, bob, tid, "пока реле живо") >= 1
	})
	key := offerKey{tid, bob.Device.ID, addr}
	before := alice.offerBook.cursor(key, 1<<30)
	if before == 0 {
		t.Fatal("setup: no mark for bob's mailbox after a delivered word")
	}
	srv.Close()
	time.Sleep(2 * cadence)
	if _, err := alice.Say(tid, "реле упало", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * cadence) // the outbox fails and backs off; the cycle fails
	if got := alice.offerBook.cursor(key, 1<<30); got != before {
		t.Fatalf("the cursor moved from %d to %d while the relay was down", before, got)
	}
	srv2, _, err := relayserver.StartServer(addr, relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()
	alice.onWake() // the network is back: sockets and ladders reset
	alice.kickOutbox()
	waitUntil(t, 40*time.Second, "the word said during the outage never arrived", func() bool {
		alice.relaySyncOnce(addr)
		bob.relaySyncOnce(addr)
		return countMsg(t, bob, tid, "реле упало") >= 1
	})
	if got := alice.offerBook.cursor(key, 1<<30); got <= before {
		t.Fatalf("after a successful push the cursor stayed at %d", got)
	}
}
