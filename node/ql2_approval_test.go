package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
)

func waitState(t *testing.T, rt *Runtime, reqID string, want JoinState) string {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var last JoinState
	var detail string
	for time.Now().Before(deadline) {
		last, detail = rt.JoinStatus(reqID)
		if last == want {
			return detail
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("never reached %q (stuck at %q)", want, last)
	return ""
}

// The sentence the interface has been making since UI-2 becomes true: on a
// link that asks for it, NOBODY is admitted until a person says so.
func TestNobodyEntersUntilTheHostSaysSo(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, _ := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}

	// Bob is told a person has to look — not merely that nothing has come
	// back, which is a different and less honest sentence.
	waitState(t, bob, req, JoinWaitingHost)

	queue := alice.EntryRequests()
	if len(queue) != 1 || queue[0].State != "pending" {
		t.Fatalf("the door queue does not show one person waiting: %+v", queue)
	}
	if got := len(bob.Spaces()); got != 0 {
		t.Fatalf("bob got in before anyone decided: %d spaces", got)
	}

	if err := alice.DecideEntry(queue[0].RequestID, true, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, req, JoinReady)
}

// The honesty test of the gate: a decline reaches the guest AS a decline.
// Not as an error, not as a timeout, and above all not as "unknown" — which
// is what a lost session says, and what a refusal must never be confused
// with.
func TestADeclineReachesTheGuestAndSaysSo(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, _ := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, req, JoinWaitingHost)

	queue := alice.EntryRequests()
	if err := alice.DecideEntry(queue[0].RequestID, false, "not this time"); err != nil {
		t.Fatal(err)
	}
	reason := waitState(t, bob, req, JoinDeclined)
	if reason != "not this time" {
		t.Fatalf("the host's own words did not reach the guest: %q", reason)
	}

	st, _ := bob.JoinStatus(req)
	if st == JoinRejected || st == JoinUnknown || st == JoinExpiredWaiting {
		t.Fatalf("a decline was rendered as %q", st)
	}
}

// Declining costs the host nothing: the use is spent on an admission, so
// the next person still has the place that was there all along.
func TestDecliningCostsNoUse(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	srv, _ := setUpRelay(t, alice, bob, carol)
	defer srv.Close()

	// One place, and a host who decides.
	info, err := alice.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	reqBob, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, reqBob, JoinWaitingHost)
	q := alice.EntryRequests()
	if err := alice.DecideEntry(q[0].RequestID, false, "another time"); err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, reqBob, JoinDeclined)

	// Carol uses the SAME pass link — the place was never spent.
	reqCarol, err := carol.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, carol, reqCarol, JoinWaitingHost)
	for _, e := range alice.EntryRequests() {
		if e.State == "pending" {
			if err := alice.DecideEntry(e.RequestID, true, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitState(t, carol, reqCarol, JoinReady)
}

// A pending request survives the host restarting, with the time it was
// actually asked — somebody who waited overnight should not appear to have
// just arrived.
func TestAPendingRequestSurvivesTheHostRestarting(t *testing.T) {
	dir := t.TempDir()
	alice := openRuntime(t, dir, "alice")
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, addr := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, req, JoinWaitingHost)
	before := alice.EntryRequests()
	if len(before) != 1 {
		t.Fatalf("nobody at the door: %+v", before)
	}
	alice.Close()

	alice2 := openRuntime(t, dir, "alice")
	defer alice2.Close()
	s := alice2.GetSettings()
	s.Relay = addr
	if err := alice2.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	after := alice2.EntryRequests()
	if len(after) != 1 {
		t.Fatalf("the queue did not survive: %+v", after)
	}
	if after[0].AskedAt != before[0].AskedAt {
		t.Fatalf("the time they knocked changed: %d → %d",
			before[0].AskedAt, after[0].AskedAt)
	}
	if err := alice2.DecideEntry(after[0].RequestID, true, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, req, JoinReady)
}

// An open link keeps admitting automatically. The gate adds a choice; it
// does not change what a link without that choice does.
func TestAnOpenLinkStillAdmitsAutomatically(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, _ := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
	if len(alice.EntryRequests()) != 0 {
		t.Fatal("an open link queued somebody at the door")
	}
}

// The tail of decided entries is bounded in count and in time. Without
// this, accepting people forever would grow a list nobody asked for — and
// the queue would stop being a queue.
func TestTheDoorKeepsAShortMemory(t *testing.T) {
	rec := &passRecord{handled: map[[32]byte]handledFate{}}
	now := uint64(time.Now().Unix())

	// Two people still waiting, and far more already decided than the tail
	// should keep.
	for i := range 2 {
		var id [32]byte
		id[0] = byte(i)
		rec.entries = append(rec.entries, storage.EntryRecord{
			Request: id, AskedAt: now, State: storage.EntryPending,
		})
	}
	for i := range maxRecentEntries * 3 {
		var id [32]byte
		id[0] = byte(100 + i)
		rec.entries = append(rec.entries, storage.EntryRecord{
			Request: id, AskedAt: now, DecidedAt: now,
			State: storage.EntryAdmitted, Outcome: storage.OutcomeGranted,
		})
	}
	pruneEntries(rec, now)

	pending, decided := 0, 0
	for _, e := range rec.entries {
		if e.State == storage.EntryPending {
			pending++
		} else {
			decided++
		}
	}
	if pending != 2 {
		t.Fatalf("somebody waiting was pruned: %d left", pending)
	}
	if decided > maxRecentEntries {
		t.Fatalf("the tail grew past its bound: %d", decided)
	}

	// And an old decision ages out even when the tail has room.
	rec.entries = []storage.EntryRecord{{
		Request: [32]byte{9}, AskedAt: 1, DecidedAt: 1,
		State: storage.EntryDeclined, Outcome: storage.OutcomeDeclinedByHost,
	}}
	pruneEntries(rec, now+uint64(recentEntryTTL/time.Second)+1)
	if len(rec.entries) != 0 {
		t.Fatalf("a day-old decision is still on the list: %+v", rec.entries)
	}
}

// Withdrawing a link tells whoever is waiting, instead of leaving them to
// time out against a door that no longer exists.
func TestWithdrawingALinkTellsWhoIsWaiting(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, _ := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, req, JoinWaitingHost)

	if err := alice.WithdrawQuickLink(info.Hint); err != nil {
		t.Fatal(err)
	}
	// Not a refusal by a person, and not a timeout: the door closed.
	reason := waitState(t, bob, req, JoinExpiredWaiting)
	if reason == "" {
		t.Fatal("the guest was not told why")
	}
	if st, _ := bob.JoinStatus(req); st == JoinDeclined {
		t.Fatal("a withdrawn link was reported as somebody refusing")
	}
}

// A pass that ran out while somebody waited fails honestly at the decision,
// and says which of the two things happened.
func TestAPassThatExpiresWhileWaitingSaysThat(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, _ := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	req, err := bob.JoinByPass(prev.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, req, JoinWaitingHost)

	// Time passes while the host thinks about it.
	q := alice.EntryRequests()
	alice.passes.mu.Lock()
	for _, rec := range alice.passes.byID {
		rec.pass.ExpiresAt = uint64(time.Now().Unix()) - 1
	}
	alice.passes.mu.Unlock()

	if err := alice.DecideEntry(q[0].RequestID, true, ""); err != nil {
		t.Fatal(err)
	}
	reason := waitState(t, bob, req, JoinExpiredWaiting)
	if reason == "" {
		t.Fatal("expiry reached the guest without saying so")
	}
	if len(bob.Spaces()) != 0 {
		t.Fatal("somebody got in on an expired pass")
	}
}
