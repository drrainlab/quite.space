package node

// LT-4 S4: the ✓✓ leaves when the frame ARRIVES — from the pull that
// applied it, from the LAN batch that carried it — not with the next
// cycle; a burst is one receipt; a peer on the wire gets it over the wire.

import (
	"fmt"
	"testing"
	"time"
)

// holdCycle parks rt's relay loop before its next cycle and waits out the
// one that may be in flight. The returned func releases it.
func holdCycle(rt *Runtime) func() {
	release := make(chan struct{})
	rs := rt.relaySync
	rs.mu.Lock()
	rs.beforeCycle = func() { <-release }
	rs.mu.Unlock()
	time.Sleep(4 * cadence)
	return func() { close(release) }
}

func TestAReceiptLeavesOnArrivalNotOnTheCycle(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	defer holdCycle(bob)() // bob's cycle — and its sendReceipts net — never runs from here

	if _, err := alice.Say(tid, "дошло?", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// The relay's own word for it, not a mailbox count: the observer reads
	// one bucket, and a bucket boundary between the Put and the count read
	// as "never reached" once in ten.
	waitUntil(t, 10*time.Second, "the word never reached the relay", func() bool {
		return deliveryOf(t, alice, tid, "дошло?") != "sent"
	})
	start := time.Now()
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "alice never saw ✓✓ although bob applied the word", func() bool {
		return deliveryOf(t, alice, tid, "дошло?") == "delivered"
	})
	t.Logf("✓✓ home %v after bob's pull, with bob's cycle held shut", time.Since(start))
}

func TestABurstOfMessagesIsOneReceiptNotTwenty(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	defer holdCycle(bob)()
	before := bob.receiptPuts.Load()

	const n = 20
	for i := 0; i < n; i++ {
		if _, err := alice.Say(tid, fmt.Sprintf("залп %02d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// Bob reads as the words land, batch by batch, the way a phone's
	// doorbell would have him read.
	waitUntil(t, 15*time.Second, "bob never got the whole burst", func() bool {
		_, _ = bob.PullFromRelay(addr)
		time.Sleep(50 * time.Millisecond)
		return countMsg(t, bob, tid, "залп 19") >= 1
	})
	waitUntil(t, 5*time.Second, "alice never saw ✓✓ on the last word", func() bool {
		return deliveryOf(t, alice, tid, "залп 19") == "delivered"
	})
	time.Sleep(2 * receiptDebounce) // anything else still armed
	if puts := bob.receiptPuts.Load() - before; puts > 2 {
		t.Fatalf("a burst of %d words cost %d receipt Puts — one wake of alice's phone per word", n, puts)
	} else {
		t.Logf("%d words, %d receipt Put(s)", n, puts)
	}
}

func TestALanPeerReceiptsOverTheWireNotTheRelay(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	alice, bob, tid := pairOnRelay(t, addr)
	defer alice.Close()
	defer bob.Close()
	if err := alice.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("LAN listener unavailable in this sandbox: %v", err)
	}
	if err := bob.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Skipf("LAN listener unavailable in this sandbox: %v", err)
	}
	if err := bob.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", alice.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 15*time.Second, "the pair never authenticated on the LAN link", func() bool {
		return alice.lanPeerDevice(bob.Device.ID) && bob.lanPeerDevice(alice.Device.ID)
	})
	defer holdCycle(bob)()
	before := bob.receiptPuts.Load()
	mailBefore := mailboxCount(t, addr, tid, alice.Device.ID)

	if _, err := alice.Say(tid, "через комнату", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, "the word never crossed the room", func() bool {
		return countMsg(t, bob, tid, "через комнату") >= 1
	})
	waitUntil(t, 5*time.Second, "alice never saw ✓✓ from a peer on the wire", func() bool {
		return deliveryOf(t, alice, tid, "через комнату") == "delivered"
	})
	if puts := bob.receiptPuts.Load() - before; puts != 0 {
		t.Fatalf("bob put %d receipt(s) on the relay for an author on his own wire", puts)
	}
	if got := mailboxCount(t, addr, tid, alice.Device.ID); got != mailBefore {
		t.Fatalf("alice's mailbox grew %d→%d while bob was on her wire", mailBefore, got)
	}
}
