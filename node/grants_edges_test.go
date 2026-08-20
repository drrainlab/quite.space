package node

// The four edges the owner's review named before 1B may ship (ADR-024):
// a stale grant replay must roll nothing back; a revoked grantor's paper
// is refused no matter how historically valid its signature; the sibling
// set converges TRANSITIVELY (no hub-and-spoke); and grants flow between
// the non-hub siblings while the hub is offline — the proof the
// principal exists independently of the device that did the pairing.

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/crypto"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// TestStaleIdentityGrantCannotRollbackSpace — the mailbox is Fetch-based
// (non-destructive) BY DESIGN, so an old grant WILL be seen again after
// the space has moved on. Application must be strictly monotonic: an
// already-attached space is untouched, epochs never regress, and a
// message sealed under a LATER epoch still reads after the replay.
func TestStaleIdentityGrantCannotRollbackSpace(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	mac := openRuntime(t, t.TempDir(), "gleb")
	defer mac.Close()
	setPersonalRelay(t, mac, addr)
	phone := pairChild(t, mac, now)
	defer phone.Close()
	setPersonalRelay(t, phone, addr)

	// The phone creates a space; the mac converges via a grant (G1).
	tid, err := phone.CreateSpace("комната")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 45*time.Second, "the mac never converged", func() bool {
		phone.relaySyncOnce(addr)
		mac.relaySyncOnce(addr)
		time.Sleep(700 * time.Millisecond)
		_, ok := mac.spaceForTest(tid)
		return ok
	})
	// Capture G1's exact sealed bytes — the stale artifact.
	phone.mu.Lock()
	g1 := append([]byte(nil), phone.grants.sealed[tid][mac.Device.ID]...)
	phone.mu.Unlock()
	if len(g1) == 0 {
		t.Fatal("no sealed grant to replay")
	}

	// The space moves on: the phone (its owner) rotates by inviting a
	// third party, then speaks under the newer epoch.
	third := openRuntime(t, t.TempDir(), "third")
	defer third.Close()
	invite, err := phone.MintInvite(tid, third.Device.ID, third.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.Say(tid, "под новой эпохой", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 45*time.Second, "the mac never read the newer epoch", func() bool {
		phone.relaySyncOnce(addr)
		mac.relaySyncOnce(addr)
		time.Sleep(700 * time.Millisecond)
		return countMsg(t, mac, tid, "под новой эпохой") == 1
	})

	// THE REPLAY: G1 arrives again, straight into the installer.
	if err := mac.installGrant(g1); err != nil {
		t.Fatalf("a stale replay must be a clean no-op, got: %v", err)
	}
	if n := countMsg(t, mac, tid, "под новой эпохой"); n != 1 {
		t.Fatalf("the replay disturbed the log: %d copies", n)
	}
	// And the mac still reads what comes AFTER the replay — epochs did
	// not regress.
	if _, err := phone.Say(tid, "и после реплея", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 45*time.Second, "epochs regressed after a stale replay", func() bool {
		phone.relaySyncOnce(addr)
		mac.relaySyncOnce(addr)
		time.Sleep(700 * time.Millisecond)
		return countMsg(t, mac, tid, "и после реплея") == 1
	})
}

// TestRevokedGrantorIsRefused — a grant may sit in a mailbox forever and
// its signature stays historically valid; what decides is the CURRENT
// trust state. Once the grantor is revoked, its paper installs nothing.
func TestRevokedGrantorIsRefused(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	mac := openRuntime(t, t.TempDir(), "gleb")
	defer mac.Close()
	// Deliberately NO relay for the mac: its background cycle must not
	// race the revocation by applying the grant early (it did — the first
	// run of this test proved the refusal works and the test's own
	// ordering did not). The grant is fed to the installer by hand.
	phone := pairChild(t, mac, now)
	defer phone.Close()
	setPersonalRelay(t, phone, addr)

	// The phone builds a real, valid grant for the mac…
	tid, err := phone.CreateSpace("комната")
	if err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	waitUntil(t, 30*time.Second, "the phone never sealed a grant", func() bool {
		phone.relaySyncOnce(addr)
		time.Sleep(700 * time.Millisecond)
		phone.mu.Lock()
		sealed = append([]byte(nil), phone.grants.sealed[tid][mac.Device.ID]...)
		phone.mu.Unlock()
		return len(sealed) > 0
	})

	// …but before the mac applies it, the phone is revoked at the root.
	if err := mac.RevokeDevice(phone.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := mac.spaceForTest(tid); ok {
		t.Fatal("the space arrived before the revocation — reorder the test")
	}
	err = mac.installGrant(sealed)
	if err == nil {
		t.Fatal("a revoked grantor's paper was accepted")
	}
	if _, ok := mac.spaceForTest(tid); ok {
		t.Fatal("a revoked grantor's grant installed a space")
	}
}

// TestSiblingSetConvergesTransitively — A pairs B; later A pairs C; B
// must learn C's certificate (and vice versa) even though A owns no
// space the three share. Then the proof the principal is not a hub:
// WITH A OFFLINE, B joins a new space and C converges on it anyway.
func TestSiblingSetConvergesTransitively(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	a := openRuntime(t, t.TempDir(), "gleb")
	setPersonalRelay(t, a, addr)
	b := pairChild(t, a, now)
	defer b.Close()
	setPersonalRelay(t, b, addr)
	c := pairChild(t, a, now)
	defer c.Close()
	setPersonalRelay(t, c, addr)

	// B learns C through the identity plane (A announces its grown set).
	waitUntil(t, 60*time.Second, "B never learned C's certificate", func() bool {
		a.relaySyncOnce(addr)
		b.relaySyncOnce(addr)
		c.relaySyncOnce(addr)
		time.Sleep(700 * time.Millisecond)
		b.mu.Lock()
		_, okC := b.ident.certificateFor(c.Device.ID)
		b.mu.Unlock()
		return okC
	})
	c.mu.Lock()
	_, okB := c.ident.certificateFor(b.Device.ID)
	c.mu.Unlock()
	if !okB {
		t.Fatal("C does not know B — the freight should have carried it")
	}

	// THE HUB GOES DARK. B joins a friend; C must converge without A.
	a.Close()
	friend := openRuntime(t, t.TempDir(), "friend")
	defer friend.Close()
	setPersonalRelay(t, friend, addr)
	tid, err := friend.CreateSpace("без хаба")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := friend.MintPass(tid, 2, 24, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := b.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, b, req, JoinReady)
	waitUntil(t, 60*time.Second, "C never converged while the hub was offline", func() bool {
		friend.relaySyncOnce(addr)
		b.relaySyncOnce(addr)
		c.relaySyncOnce(addr)
		time.Sleep(700 * time.Millisecond)
		_, ok := c.spaceForTest(tid)
		return ok
	})
}

// TestIdentityMailboxGrowthIsBounded — the restart edge: resealing makes
// new ciphertext, the mailbox is non-destructive, so the bound is the
// store's own per-hint quota plus TTL. Pin that the quota actually
// bounds it rather than trusting the comment.
func TestIdentityMailboxGrowthIsBounded(t *testing.T) {
	srv, addr := startRelay(t)
	defer srv.Close()
	now := uint64(time.Now().Unix())

	mac := openRuntime(t, t.TempDir(), "gleb")
	defer mac.Close()
	setPersonalRelay(t, mac, addr)
	phone := pairChild(t, mac, now)
	defer phone.Close()
	setPersonalRelay(t, phone, addr)
	if _, err := phone.CreateSpace("комната"); err != nil {
		t.Fatal(err)
	}
	// Simulate restarts: drop the seal cache so every cycle reseals a
	// byte-fresh ciphertext of the same logical grant. Nothing can drain
	// this mailbox (Fetch is non-destructive and no collect capability
	// exists for it), so the bound is the store's own per-hint quota plus
	// TTL — pin the quota half here, well past it.
	before := srv.Pending()
	for i := 0; i < 150; i++ {
		phone.mu.Lock()
		phone.grantsInit()
		phone.grants.sealed = map[id.TerminalID]map[id.DeviceID][]byte{}
		phone.grants.setSent = map[id.DeviceID]int{}
		phone.grants.setBytes = map[id.DeviceID][]byte{}
		phone.grants.tagChecked = map[id.DeviceID]bool{}
		phone.mu.Unlock()
		phone.offerGrants()
	}
	grew := srv.Pending() - before
	// With deterministic tags the sender skips logical messages already
	// present, so 150 simulated restarts leave a HANDFUL of physical
	// copies (one per logical message, plus the pre-tag ones), not a
	// quota's worth of reseals.
	if grew > 6 {
		t.Fatalf("restarts stacked reseals in the mailbox: +%d items", grew)
	}
	_ = storage.RouteAdvertised
	_ = crypto.EpochKey{}
}
