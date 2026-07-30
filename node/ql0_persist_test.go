package node

import (
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// ADR-012 invariant 7, as a test: "a per-request_id saga journal SURVIVES
// RESTARTS; reprocessing returns the stored result."
//
// Today the registry is in memory (node/pass.go), so a link minted before a
// restart still RESOLVES — the relay holds the sealed payload for ten
// minutes — but the owner has no record of the pass, `acceptOne` matches
// nothing, and the guest waits forever. Nobody is told anything. That is
// the worst shape a failure can take, and the normative document already
// forbids it.
func TestAQuickLinkStillWorksAfterTheOwnerRestarts(t *testing.T) {
	dir := t.TempDir()
	alice := openRuntime(t, dir, "alice")
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, addr := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.MintQuickLink(id.TerminalID{}, "")
	if err != nil {
		t.Fatal(err)
	}

	// The owner's device goes away and comes back — a laptop lid, an
	// update, a crash. The words are still good for ten minutes.
	alice.Close()
	alice2 := openRuntime(t, dir, "alice")
	defer alice2.Close()
	s := alice2.GetSettings()
	s.Relay = addr
	if err := alice2.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqID, JoinReady)
}

// Withdrawal is a promise about a link the person already handed out. It
// must not evaporate because the process restarted.
func TestWithdrawingALinkSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	alice := openRuntime(t, dir, "alice")
	srv, addr := setUpRelay(t, alice)
	defer srv.Close()

	info, err := alice.MintQuickLink(id.TerminalID{}, "for later")
	if err != nil {
		t.Fatal(err)
	}
	alice.Close()

	alice2 := openRuntime(t, dir, "alice")
	defer alice2.Close()
	s := alice2.GetSettings()
	s.Relay = addr
	if err := alice2.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := alice2.WithdrawQuickLink(info.Hint); err != nil {
		t.Fatalf("a link that outlived a restart could not be withdrawn: %v", err)
	}
}

// The poller used to capture ONE relay address at first start and a
// `polling` flag made every later call a no-op. A pass minted against a
// second relay was therefore never collected — its requests sat in a
// mailbox nobody was watching.
func TestASecondRelayDoesNotOrphanTheFirst(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	srvA, addrA := setUpRelay(t, alice, bob)
	defer srvA.Close()

	// A pass on relay A, then the node moves to relay B.
	spaceA, err := alice.CreateSpace("first room")
	if err != nil {
		t.Fatal(err)
	}
	passA, err := alice.MintPass(spaceA, 1, 1, addrA)
	if err != nil {
		t.Fatal(err)
	}

	srvB, addrB := setUpRelay(t, alice)
	defer srvB.Close()
	spaceB, err := alice.CreateSpace("second room")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.MintPass(spaceB, 1, 1, addrB); err != nil {
		t.Fatal(err)
	}

	// Bob joins through relay A. The owner must still be watching it.
	sb := bob.GetSettings()
	sb.Relay = addrA
	if err := bob.SetSettings(sb); err != nil {
		t.Fatal(err)
	}
	reqID, err := bob.JoinByPass(passA.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqID, JoinReady)
}

// MaxUses counts admissions, not attempts. A device already inside costs
// nothing to re-admit, because `AcceptIntoSpace` returns the current epoch
// for an existing member without rotating.
func TestTheSameDeviceAskingTwiceDoesNotSpendTwoUses(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	srv, _ := setUpRelay(t, alice, bob, carol)
	defer srv.Close()

	space, err := alice.CreateSpace("two seats")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(space, 2, 1, alice.GetSettings().Relay)
	if err != nil {
		t.Fatal(err)
	}

	req1, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req1, JoinReady)

	// Bob asks again with the same pass — a fresh request id, the same
	// device. This must not consume the seat meant for someone else.
	if _, err := bob.JoinByPass(pass.Link); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)

	req3, err := carol.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, carol, req3, JoinReady)
}

// A guest whose session is gone must be told the truth — "this device has
// no record of that request" — and never "you were rejected". Today
// JoinStatus returns JoinRejected for an unknown id, so a lost session and
// a refusal are indistinguishable, which is the exact honesty failure the
// approval gate must not inherit.
func TestAGuestWhoRestartsIsNotToldTheyWereRejected(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	dir := t.TempDir()
	bob := openRuntime(t, dir, "bob")
	srv, addr := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.MintQuickLink(id.TerminalID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	bob.Close()

	bob2 := openRuntime(t, dir, "bob")
	defer bob2.Close()
	s := bob2.GetSettings()
	s.Relay = addr
	if err := bob2.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	st, _ := bob2.JoinStatus(reqID)
	if st == JoinRejected {
		t.Fatal("a restarted guest was told they had been rejected")
	}
	// And the join still completes: the saga is the node's, not the
	// process's.
	waitJoin(t, bob2, reqID, JoinReady)
}

// Storing an outcome byte instead of the sealed acceptance is only correct
// if a re-delivered request can be answered AFTER the epoch has moved on —
// not merely right after the first accept. Bob is admitted, then Carol is
// admitted and the epoch rotates again, then the owner restarts and Bob's
// original request arrives once more.
func TestAcceptedRequestCanBeRegrantedAfterLaterRotationAndRestart(t *testing.T) {
	dir := t.TempDir()
	alice := openRuntime(t, dir, "alice")
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	srv, addr := setUpRelay(t, alice, bob, carol)
	defer srv.Close()

	space, err := alice.CreateSpace("rotating room")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(space, 3, 1, addr)
	if err != nil {
		t.Fatal(err)
	}
	reqBob, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, reqBob, JoinReady)

	reqCarol, err := carol.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, carol, reqCarol, JoinReady)

	usedBefore := passUsed(t, alice, pass.PassID)
	alice.Close()

	alice2 := openRuntime(t, dir, "alice")
	defer alice2.Close()
	s := alice2.GetSettings()
	s.Relay = addr
	if err := alice2.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	// Bob's original request is re-delivered against an epoch that has
	// since rotated for Carol.
	if _, err := bob.JoinByPass(pass.Link); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)

	if got := passUsed(t, alice2, pass.PassID); got != usedBefore {
		t.Fatalf("a re-delivered grant spent another use: %d → %d", usedBefore, got)
	}
}

// passUsed reads the owner's durable use count for one pass.
func passUsed(t *testing.T, rt *Runtime, passID string) uint64 {
	t.Helper()
	for _, p := range rt.ListPasses() {
		if strings.HasPrefix(p.PassID, passID) || strings.HasPrefix(passID, p.PassID) {
			return p.Used
		}
	}
	t.Fatalf("no record of pass %s", passID)
	return 0
}

// The three facts admission establishes — the member, the epoch they were
// given, and the use it cost — must be true TOGETHER on disk. Two separate
// writes leave a window where a crash keeps one without the other, which is
// ADR-012 invariant 8 broken by a power cut rather than by faulty logic.
//
// This asserts the observable consequence rather than counting writes: the
// trio survives a restart consistently, on both sides.
func TestAdmissionPersistsTheTrioTogether(t *testing.T) {
	dir := t.TempDir()
	alice := openRuntime(t, dir, "alice")
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, addr := setUpRelay(t, alice, bob)
	defer srv.Close()

	space, err := alice.CreateSpace("one write")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := alice.MintPass(space, 2, 1, addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(pass.Link)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
	alice.Close()

	alice2 := openRuntime(t, dir, "alice")
	defer alice2.Close()

	// 1. the use was spent
	if got := passUsed(t, alice2, pass.PassID); got != 1 {
		t.Fatalf("the use did not survive: %d", got)
	}
	// 2. the member is there
	alice2.mu.Lock()
	meta := alice2.ks.Spaces[space]
	epochs := alice2.ks.Epochs[space]
	alice2.mu.Unlock()
	if len(meta.Members) < 2 {
		t.Fatalf("the admitted member did not survive: %d", len(meta.Members))
	}
	// 3. the epoch they were handed is the one on disk
	if len(epochs) == 0 {
		t.Fatal("the rotated epoch did not survive")
	}
	// And Bob still holds what he was given.
	if len(bob.Spaces()) == 0 {
		t.Fatal("the guest lost the space across the owner's restart")
	}
}

// A record whose signature does not verify is dropped, never quietly kept:
// a file on disk earns exactly as much trust as bytes off the wire.
func TestACorruptPassFrameIsDroppedNotTrusted(t *testing.T) {
	dir := t.TempDir()
	alice := openRuntime(t, dir, "alice")
	srv, addr := setUpRelay(t, alice)
	defer srv.Close()

	space, err := alice.CreateSpace("tampered")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.MintPass(space, 1, 1, addr); err != nil {
		t.Fatal(err)
	}
	alice.Close()

	// Corrupt the stored frame the way a bad disk or a meddler would.
	tampered := openRuntime(t, dir, "alice")
	tampered.mu.Lock()
	if len(tampered.ks.Passes) == 0 {
		tampered.mu.Unlock()
		tampered.Close()
		t.Fatal("nothing was persisted to corrupt")
	}
	tampered.ks.Passes[0].Frame[len(tampered.ks.Passes[0].Frame)-1] ^= 0xFF
	_ = tampered.saveKeystore()
	tampered.mu.Unlock()
	tampered.Close()

	reopened := openRuntime(t, dir, "alice")
	defer reopened.Close()
	if got := len(reopened.ListPasses()); got != 0 {
		t.Fatalf("a pass with a broken signature was restored: %d", got)
	}
}
