// The QL wave, end to end (the frozen plan's closing test).
//
// Every gate has its own tests; this one asserts the two sentences the
// wave exists for, in the order a real evening would produce them:
//
//	a link opens a place the host chose, and nobody enters it
//	unannounced when the host asked to be asked
//
//	every knock at that door gets exactly one true fate, and a restart
//	on either side loses nothing
//
// Steps 4, 5, 10 and 11 are the ones that failed before the wave.
package node

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// linkPassID finds the pass a quick link was minted over. The owner's
// record is the ONLY enforcement — the sealed payload merely describes —
// so this is what the capacity assertions read.
func linkPassID(t *testing.T, rt *Runtime, hint string) string {
	t.Helper()
	for _, q := range rt.QuickLinks() {
		if q.Hint == hint {
			return q.PassID
		}
	}
	t.Fatalf("no record of the link at %s", hint)
	return ""
}

// manifestFrameOf returns a space's manifest bytes, so a local rename can
// be proved to have touched nothing that travels.
func manifestFrameOf(rt *Runtime, tid id.TerminalID) []byte {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st := rt.spaces[tid]
	if st == nil {
		return nil
	}
	return append([]byte(nil), st.space.ManifestFrame...)
}

// nameSet is what a projected display actually promises: WHO is here, not
// the order a map happened to yield them in.
func nameSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func waitMembers(t *testing.T, rt *Runtime, tid id.TerminalID, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rt.mu.Lock()
		st := rt.spaces[tid]
		n := 0
		if st != nil {
			n = len(st.space.MemberCards(uint64(time.Now().Unix())))
		}
		rt.mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("space never reached %d members", want)
}

func TestTheDoorHoldsAcrossARestart(t *testing.T) {
	aliceDir := t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carolDir := t.TempDir()
	carol := openRuntime(t, carolDir, "carol")
	dave := openRuntime(t, t.TempDir(), "dave")
	defer dave.Close()
	erin := openRuntime(t, t.TempDir(), "erin")
	defer erin.Close()
	frank := openRuntime(t, t.TempDir(), "frank")
	defer frank.Close()
	grace := openRuntime(t, t.TempDir(), "grace")
	defer grace.Close()
	srv, addr := setUpRelay(t, alice, bob, carol, dave, erin, frank, grace)
	defer srv.Close()

	// 1 — a group link opens a NEW place, and that place has no name yet.
	info, err := alice.CreateQuickLink(QuickLinkOptions{
		MaxUses: 3, TTLHours: 24, Approval: "host",
	})
	if err != nil {
		t.Fatal(err)
	}
	spaces := alice.Spaces()
	if len(spaces) != 1 {
		t.Fatalf("minting one link should open exactly one place: %+v", spaces)
	}
	room := spaces[0].ID
	if spaces[0].Title == LineTitle {
		t.Fatal("the link reached for the legacy line instead of opening a place")
	}
	if spaces[0].Display.Key != "space.waiting" {
		t.Fatalf("an empty new place should say it is waiting: %+v", spaces[0].Display)
	}
	if spaces[0].DisplayTitle != "waiting for someone" {
		t.Fatalf("the English fallback reads %q", spaces[0].DisplayTitle)
	}

	// 2 — a shared door stays readable. Four people resolve the SAME words,
	// and resolving spends nothing: capacity lives in the owner's record,
	// not in the mailbox.
	previews := map[string]QuickLinkPreview{}
	for name, rt := range map[string]*Runtime{
		"bob": bob, "carol": carol, "dave": dave, "erin": erin,
	} {
		p, err := rt.ResolveQuickLink(info.Phrase)
		if err != nil {
			t.Fatalf("%s could not open a shared link: %v", name, err)
		}
		previews[name] = p
	}
	passA := linkPassID(t, alice, info.Hint)
	if u := passUsed(t, alice, passA); u != 0 {
		t.Fatalf("reading the words spent %d places", u)
	}

	// 3 — Bob knocks and waits. He is told a person has to look, and he is
	// not in the space.
	reqBob, err := bob.JoinByPass(previews["bob"].PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, reqBob, JoinWaitingHost)
	if q := alice.EntryRequests(); len(q) != 1 || q[0].State != "pending" {
		t.Fatalf("the door does not show one person waiting: %+v", q)
	}
	if len(bob.Spaces()) != 0 {
		t.Fatal("bob got in before anybody decided")
	}
	askedAt := alice.EntryRequests()[0].AskedAt

	// 4 — the host restarts. Somebody who waited is still waiting, at the
	// hour they actually knocked.
	alice.Close()
	alice = openRuntime(t, aliceDir, "alice")
	defer func() { alice.Close() }()
	s := alice.GetSettings()
	s.Relay = addr
	if err := alice.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	q := alice.EntryRequests()
	if len(q) != 1 || q[0].AskedAt != askedAt {
		t.Fatalf("the queue did not survive the restart intact: %+v", q)
	}

	// 5 — a refusal is a refusal, and it costs the host nothing.
	if err := alice.DecideEntry(q[0].RequestID, false, "not this time"); err != nil {
		t.Fatal(err)
	}
	if reason := waitState(t, bob, reqBob, JoinDeclined); reason != "not this time" {
		t.Fatalf("the host's own words did not reach bob: %q", reason)
	}
	if st, _ := bob.JoinStatus(reqBob); st == JoinRejected || st == JoinUnknown {
		t.Fatalf("a decline was rendered as %q", st)
	}
	if u := passUsed(t, alice, passA); u != 0 {
		t.Fatalf("declining spent %d places", u)
	}

	// 6 — three are let in, and the fourth is refused by CAPACITY at the
	// moment of admission, which is where the limit lives.
	admit := func(rt *Runtime, who string) string {
		t.Helper()
		req, err := rt.JoinByPass(previews[who].PassLink)
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, rt, req, JoinWaitingHost)
		for _, e := range alice.EntryRequests() {
			if e.State == "pending" {
				if err := alice.DecideEntry(e.RequestID, true, ""); err != nil {
					t.Fatal(err)
				}
			}
		}
		waitState(t, rt, req, JoinReady)
		return req
	}
	admit(carol, "carol")
	admit(dave, "dave")
	admit(erin, "erin")
	if u := passUsed(t, alice, passA); u != 3 {
		t.Fatalf("three admissions spent %d places", u)
	}

	// Frank reads the same words perfectly well — the door is not the
	// limit. He is turned away when the owner's record is consulted.
	pf, err := frank.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatalf("a full link stopped being readable: %v", err)
	}
	reqFrank, err := frank.JoinByPass(pf.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, frank, reqFrank, JoinWaitingHost)
	for _, e := range alice.EntryRequests() {
		if e.State == "pending" {
			if err := alice.DecideEntry(e.RequestID, true, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	reason := waitState(t, frank, reqFrank, JoinExpiredWaiting)
	if !strings.Contains(reason, "no places left") {
		t.Fatalf("frank was not told the real reason: %q", reason)
	}
	if len(frank.Spaces()) != 0 {
		t.Fatal("a fourth device got in through a link for three")
	}
	if u := passUsed(t, alice, passA); u != 3 {
		t.Fatalf("a refused admission moved the count to %d", u)
	}

	// 7 — an unnamed place is called after who is in it.
	waitMembers(t, alice, room, 4)
	disp := alice.Spaces()[0].Display
	if disp.Key != "space.with_many" {
		t.Fatalf("a four-person room reads as %+v", disp)
	}
	if got := nameSet(disp.Names); !got["carol"] || !got["dave"] || !got["erin"] {
		t.Fatalf("the names of the people here are %v", disp.Names)
	}

	// And it still reads that way with Carol on two devices. A second
	// device of the SAME PERSON cannot be built from two runtimes — each
	// has its own principal — so the duplicate is synthesized from
	// Carol's REAL card off the live space, which is exactly the input
	// the terminal-vs-principal fix has to survive.
	alice.mu.Lock()
	raw := alice.spaces[room].space.MemberCards(uint64(time.Now().Unix()))
	me := alice.PrincipalID
	alice.mu.Unlock()
	cards := make([]memberLike, 0, len(raw)+1)
	for _, c := range raw {
		cards = append(cards, memberLike{principal: c.Principal, name: c.Name})
		if c.Name == "carol" {
			cards = append(cards, memberLike{principal: c.Principal, name: "carol"})
		}
	}
	twoDevices := projectDisplay(cards, me)
	if got := nameSet(twoDevices.Names); len(got) != len(nameSet(disp.Names)) {
		t.Fatalf("carol's second device became a second person: %v", twoDevices.Names)
	}

	// 8 — a conversation grows without anybody re-creating it elsewhere:
	// a second link into the SAME place, not a new one.
	before := len(alice.Spaces())
	linkB, err := alice.InviteToSpace(room, QuickLinkOptions{MaxUses: 1, TTLHours: 24})
	if err != nil {
		t.Fatal(err)
	}
	if after := len(alice.Spaces()); after != before {
		t.Fatalf("inviting into a place opened another one: %d → %d", before, after)
	}
	pg, err := grace.ResolveQuickLink(linkB.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	reqGrace, err := grace.JoinByPass(pg.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, grace, reqGrace, JoinReady)
	if got := grace.Spaces(); len(got) != 1 || got[0].ID != room {
		t.Fatalf("grace landed somewhere else: %+v", got)
	}

	// 9 — a local name is local. It emits nothing and publishes nothing.
	eventsBefore := alice.Spaces()[0].Events
	frameBefore := manifestFrameOf(alice, room)
	if err := alice.SetLocalTitle(room, "Fieldnotes"); err != nil {
		t.Fatal(err)
	}
	mine := alice.Spaces()[0]
	if mine.Display.Text != "Fieldnotes" || mine.DisplayTitle != "Fieldnotes" {
		t.Fatalf("the name this device chose is not what it shows: %+v", mine)
	}
	if mine.Events != eventsBefore {
		t.Fatalf("renaming emitted %d events", mine.Events-eventsBefore)
	}
	if !bytes.Equal(frameBefore, manifestFrameOf(alice, room)) {
		t.Fatal("renaming locally changed the manifest")
	}
	// Carol sees the projection, because a local name is not a shared one.
	carolSide := carol.Spaces()[0]
	if carolSide.Display.Text == "Fieldnotes" {
		t.Fatal("a name that stays on one device reached another")
	}
	if carolSide.Display.Key == "" {
		t.Fatalf("carol has no way to call the space: %+v", carolSide.Display)
	}

	// Something to read after the restart, so membership and epochs are
	// proved by content rather than by bookkeeping.
	if _, err := alice.Say(room, "we are all here", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// 10 — both sides restart. The name, the membership, the epochs and
	// the spent places all survive.
	alice.Close()
	alice = openRuntime(t, aliceDir, "alice")
	s = alice.GetSettings()
	s.Relay = addr
	if err := alice.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	carol.Close()
	carol = openRuntime(t, carolDir, "carol")
	defer carol.Close()
	s = carol.GetSettings()
	s.Relay = addr
	if err := carol.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := alice.Spaces()[0].DisplayTitle; got != "Fieldnotes" {
		t.Fatalf("the local name did not survive: %q", got)
	}
	if u := passUsed(t, alice, passA); u != 3 {
		t.Fatalf("the spent places came back as %d", u)
	}
	waitMembers(t, alice, room, 5)
	// Waited for on CAROL'S side. Alice seeing five members says the
	// admissions landed; it says nothing about when the message reaches a
	// guest — that is one more hop, and reading carol's state the instant
	// alice's settles was a race the detector's slower schedule lost.
	waitUntil(t, 20*time.Second, "carol lost the epoch she was admitted under", func() bool {
		found := false
		carol.mu.Lock()
		for _, m := range carol.spaces[room].space.State.Messages() {
			if strings.Contains(m.Text, "we are all here") {
				found = true
			}
		}
		carol.mu.Unlock()
		return found
	})

	// 11 — the three deadlines are genuinely separate. A decision made
	// just before the pass expires still reaches a guest who only comes
	// back online afterwards: expiry forbids a NEW request, never the
	// completion of one already recorded.
	henryDir := t.TempDir()
	henry := openRuntime(t, henryDir, "henry")
	sh := henry.GetSettings()
	sh.Relay = addr
	if err := henry.SetSettings(sh); err != nil {
		t.Fatal(err)
	}
	linkC, err := alice.CreateQuickLink(QuickLinkOptions{Approval: "host"})
	if err != nil {
		t.Fatal(err)
	}
	ph, err := henry.ResolveQuickLink(linkC.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	reqHenry, err := henry.JoinByPass(ph.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, henry, reqHenry, JoinWaitingHost)

	// Henry closes his laptop. Alice decides while the pass is still
	// valid, seconds before it runs out.
	henry.Close()
	passC := linkPassID(t, alice, linkC.Hint)
	deadline := uint64(time.Now().Unix()) + 4
	alice.passes.mu.Lock()
	for _, rec := range alice.passes.byID {
		if strings.HasPrefix(hexShort(rec.pass.PassID[:]), passC) ||
			strings.HasPrefix(passC, hexShort(rec.pass.PassID[:])) {
			rec.pass.ExpiresAt = deadline
		}
	}
	alice.passes.mu.Unlock()
	var pending string
	for _, e := range alice.EntryRequests() {
		if e.State == "pending" {
			pending = e.RequestID
		}
	}
	if pending == "" {
		t.Fatal("henry is not at the door")
	}
	if err := alice.DecideEntry(pending, true, ""); err != nil {
		t.Fatal(err)
	}

	// The pass runs out while he is away.
	for uint64(time.Now().Unix()) <= deadline {
		time.Sleep(200 * time.Millisecond)
	}
	henry = openRuntime(t, henryDir, "henry")
	defer henry.Close()
	sh = henry.GetSettings()
	sh.Relay = addr
	if err := henry.SetSettings(sh); err != nil {
		t.Fatal(err)
	}
	waitState(t, henry, reqHenry, JoinReady)
	if got := henry.Spaces(); len(got) != 1 {
		t.Fatalf("the decision reached henry but the space did not: %+v", got)
	}
}

// The other door, kept separate because it behaves differently on purpose:
// a personal link is the one door, and opening it spends it — while the
// device that opened it keeps its own entrance.
func TestAPersonalLinkIsSpentButRemembered(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	srv, _ := setUpRelay(t, alice, bob, carol)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := carol.ResolveQuickLink(info.Phrase); err == nil {
		t.Fatal("a personal link opened twice")
	}
	// Bob backing out of the preview must not cost him the entrance.
	again, err := bob.ResolvedQuickLink(info.Phrase)
	if err != nil {
		t.Fatalf("bob lost his own entrance: %v", err)
	}
	if again.PassLink != first.PassLink {
		t.Fatal("bob's remembered entrance is a different one")
	}
	req, err := bob.JoinByPass(again.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitJoin(t, bob, req, JoinReady)
}
