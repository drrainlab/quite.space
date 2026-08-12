package node

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/quicklink"
	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// setUpRelay gives a runtime a relay and returns its address.
func setUpRelay(t *testing.T, rts ...*Runtime) (*relayserver.Server, string) {
	t.Helper()
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1:" + itoa(port)
	for _, rt := range rts {
		s := rt.GetSettings()
		s.Relay = addr
		if err := rt.SetSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	return srv, addr
}

// The whole point, end to end: one person says five words, the other types
// them, and they are in a space together — with no space agreed on in
// advance, because minting the link created the line.
func TestFiveWordsCarrySomebodyIntoYourLine(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()
	srv, _ := setUpRelay(t, rtA, rtB)
	defer srv.Close()

	before := len(rtA.Spaces())
	info, err := rtA.MintQuickLink(id.TerminalID{}, "for bob")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rtA.Spaces()); got != before+1 {
		t.Fatalf("minting a link should have created the line: %d spaces → %d", before, got)
	}
	if !strings.HasPrefix(info.Link, "quiet://") {
		t.Fatalf("link does not look like one: %q", info.Link)
	}
	if n := len(strings.Fields(info.Phrase)); n != quicklink.WordCount {
		t.Fatalf("phrase has %d words, expected %d: %q", n, quicklink.WordCount, info.Phrase)
	}

	// Bob has nothing but what Alice said out loud.
	preview, err := rtB.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	if preview.From != "alice" {
		t.Fatalf("preview should say who is inviting: %+v", preview)
	}
	if preview.PassLink == "" {
		t.Fatal("preview carried no pass")
	}

	// From here it is an ordinary pass join.
	reqID, err := rtB.JoinByPass(preview.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	if reqID == "" {
		t.Fatal("join produced no request")
	}
}

// Single use is not a policy this code enforces — it falls out of the relay's
// Collect draining what it reads. Worth pinning, because switching that call
// to Fetch would silently make every link replayable.
func TestAQuickLinkWorksExactlyOnce(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()
	srv, _ := setUpRelay(t, rtA, rtB)
	defer srv.Close()

	info, err := rtA.MintQuickLink(id.TerminalID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtB.ResolveQuickLink(info.Phrase); err != nil {
		t.Fatal(err)
	}
	_, err = rtB.ResolveQuickLink(info.Phrase)
	if err == nil {
		t.Fatal("the same words resolved twice")
	}
	// The refusal has to explain itself — and now that a link may be
	// personal OR shared, it must say WHICH kind was spent rather than
	// asserting that every link works once.
	if !strings.Contains(err.Error(), "spent by the first person") {
		t.Fatalf("the second attempt should explain itself, got: %v", err)
	}
}

// Wrong words must not be distinguishable from expired ones by the shape of
// the failure — and must certainly not resolve.
func TestWrongWordsResolveToNothing(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()
	srv, _ := setUpRelay(t, rtA, rtB)
	defer srv.Close()

	if _, err := rtA.MintQuickLink(id.TerminalID{}, ""); err != nil {
		t.Fatal(err)
	}
	other, err := quicklink.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rtB.ResolveQuickLink(other.Phrase()); err == nil {
		t.Fatal("a token nobody issued resolved")
	}
	if _, err := rtB.ResolveQuickLink("moss ember tide"); err == nil {
		t.Fatal("three words resolved")
	}
}

// The relay is a courier, not a confidant. If the pass ever reached it in the
// clear, the words would be decoration.
func TestTheRelayNeverSeesThePass(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	srv, addr := setUpRelay(t, rtA)
	defer srv.Close()

	info, err := rtA.MintQuickLink(id.TerminalID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tok, err := quicklink.Parse(info.Link)
	if err != nil {
		t.Fatal(err)
	}
	items, err := client.Fetch([][]byte{tok.Hint()})
	if err != nil || len(items) == 0 {
		t.Fatalf("nothing parked on the relay: %d %v", len(items), err)
	}
	stored := string(items[0])
	for _, leak := range append([]string{"alice", "quiet://"}, tok.Words[:]...) {
		if strings.Contains(stored, leak) {
			t.Fatalf("the relay is holding %q in the clear", leak)
		}
	}
}

// Handing out a link has to leave a trace, or "who did I let in" has no
// answer. The trace must not include the words themselves.
func TestIssuanceIsRecordedWithoutKeepingTheWords(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	srv, _ := setUpRelay(t, rtA)
	defer srv.Close()

	info, err := rtA.MintQuickLink(id.TerminalID{}, "for bob, at the studio")
	if err != nil {
		t.Fatal(err)
	}
	recs := rtA.QuickLinks()
	if len(recs) != 1 {
		t.Fatalf("expected one record, got %d", len(recs))
	}
	rec := recs[0]
	if rec.Note != "for bob, at the studio" || rec.Hint != info.Hint {
		t.Fatalf("record does not match what was issued: %+v", rec)
	}
	if rec.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("record expired on arrival: %+v", rec)
	}
	// The words are a bearer secret. A node that keeps them can hand out the
	// same access again without the issuer ever knowing.
	raw, err := os.ReadFile(rtA.quickLinkPath())
	if err != nil {
		t.Fatal(err)
	}
	blob := string(raw)
	for _, w := range strings.Fields(info.Phrase) {
		if strings.Contains(blob, w) {
			t.Fatalf("the issuance log kept the word %q", w)
		}
	}
	if strings.Contains(blob, info.Link) {
		t.Fatal("the issuance log kept the whole link")
	}

	if err := rtA.WithdrawQuickLink(rec.Hint); err != nil {
		t.Fatal(err)
	}
	if recs = rtA.QuickLinks(); !recs[0].Withdrawn {
		t.Fatal("withdrawal was not recorded")
	}
}

// Without a relay there is no quick link, and the refusal has to say why and
// point at the two paths that need no network at all.
func TestNoRelayIsRefusedWithTheReason(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	_, err := rt.MintQuickLink(id.TerminalID{}, "")
	if err == nil {
		t.Fatal("minted a quick link with no relay configured")
	}
	for _, want := range []string{"relay", "point at", "sound"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal should mention %q: %v", want, err)
		}
	}
}

// The exact inversion of what this file used to assert, and the accident
// the wave removes: every quicklink used to mint into ONE space per node,
// so a link made for Alice and a link made for Misha put two strangers in
// the same room. Two links, two places.
func TestTwoLinksNeverLandStrangersInOneRoom(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	srv, _ := setUpRelay(t, rt)
	defer srv.Close()

	before := len(rt.Spaces())
	a, err := rt.CreateQuickLink(QuickLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := rt.CreateQuickLink(QuickLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Space == b.Space {
		t.Fatalf("two links opened the same room: %s", a.Space)
	}
	if got := len(rt.Spaces()); got != before+2 {
		t.Fatalf("each link should open a place: %d → %d", before, got)
	}
	if a.Link == b.Link {
		t.Fatal("two links shared their words")
	}
}

// A conversation between two people can become a group without anyone
// re-creating it elsewhere: a second link points at the SAME space, on
// purpose and explicitly.
func TestALinkCanPointAtAnExistingSpace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	srv, _ := setUpRelay(t, rt)
	defer srv.Close()

	first, err := rt.CreateQuickLink(QuickLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tid, err := id.ParseTerminalID(first.Space)
	if err != nil {
		t.Fatal(err)
	}
	before := len(rt.Spaces())
	second, err := rt.InviteToSpace(tid, QuickLinkOptions{MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	if second.Space != first.Space {
		t.Fatalf("an invite opened a new room instead: %s", second.Space)
	}
	if got := len(rt.Spaces()); got != before {
		t.Fatalf("inviting into a space created another: %d → %d", before, got)
	}
}

// A personal link is a one-time door: the words are spent by opening them.
// A shared one stays readable until it expires, because a link for several
// people that only the first person can read is a lie in its first
// sentence.
func TestAPersonalDoorIsSpentAndASharedDoorIsNot(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	carol := openRuntime(t, t.TempDir(), "carol")
	defer carol.Close()
	srv, _ := setUpRelay(t, alice, bob, carol)
	defer srv.Close()

	personal, err := alice.CreateQuickLink(QuickLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.ResolveQuickLink(personal.Phrase); err != nil {
		t.Fatal(err)
	}
	if _, err := carol.ResolveQuickLink(personal.Phrase); err == nil {
		t.Fatal("a personal door opened twice")
	}

	shared, err := alice.CreateQuickLink(QuickLinkOptions{MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i, guest := range []*Runtime{bob, carol, bob} {
		if _, err := guest.ResolveQuickLink(shared.Phrase); err != nil {
			t.Fatalf("shared door refused reader %d: %v", i, err)
		}
	}
}

// The guest sees the intent BEFORE deciding to enter — how many may come,
// and whether a person will let them in.
func TestTheGuestSeesTheIntentBeforeEntering(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()
	srv, _ := setUpRelay(t, alice, bob)
	defer srv.Close()

	info, err := alice.CreateQuickLink(QuickLinkOptions{
		Title: "Support group", MaxUses: 5, Approval: "host",
	})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bob.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	if prev.MaxUses != 5 {
		t.Fatalf("the guest could not see how many may come: %d", prev.MaxUses)
	}
	if prev.Approval != "host" {
		t.Fatalf("the guest was not told a person decides: %q", prev.Approval)
	}
	if prev.Space != "Support group" {
		t.Fatalf("the named space did not travel: %q", prev.Space)
	}
}

// Backing out of the preview must not lose the entrance: the words are
// spent, but this device remembers the doorway until the pass expires.
func TestBackingOutDoesNotLoseTheEntrance(t *testing.T) {
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
	if _, err := bob.ResolveQuickLink(info.Phrase); err != nil {
		t.Fatal(err)
	}
	// The relay no longer holds it, but this device does.
	again, err := bob.ResolvedQuickLink(info.Phrase)
	if err != nil {
		t.Fatalf("the device forgot an entrance it had already opened: %v", err)
	}
	if again.PassLink == "" {
		t.Fatal("the cached entrance carries no way in")
	}
}

// Letting the same person in twice must not admit them twice. It is the same
// device and the same principal; a second link is the same person asking
// again, not a new member. Before this was handled, every repeat join rotated
// the epoch for everyone and wrote another member_added into a log that keeps
// everything forever.
func TestAdmittingTheSamePersonTwiceChangesNothing(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()
	srv, _ := setUpRelay(t, rtA, rtB)
	defer srv.Close()

	// Wait on the REQUEST, not on the member count: the count is already 2
	// after the first join, so waiting on it would let the second attempt
	// finish after the measurement and quietly prove nothing.
	join := func(which string) {
		info, err := rtA.MintQuickLink(id.TerminalID{}, "")
		if err != nil {
			t.Fatal(err)
		}
		p, err := rtB.ResolveQuickLink(info.Phrase)
		if err != nil {
			t.Fatal(err)
		}
		reqID, err := rtB.JoinByPass(p.PassLink)
		if err != nil {
			t.Fatal(err)
		}
		waitUntil(t, 20*time.Second, which+" join to be answered", func() bool {
			st, _ := rtB.JoinStatus(reqID)
			return st == JoinReady
		})
	}

	join("first")
	line, _ := rtA.LineSpace() // lazily created, so only after a first join

	// Observe under the runtime's own lock. spaceForTest hands back the *Space
	// and releases r.mu, so reading through it races with the relay-sync
	// goroutine, which writes under r.mu — -race says so, loudly. This test is
	// in package node and can simply take the same lock.
	observe := func() (members, events int, epoch uint64) {
		rtA.mu.Lock()
		defer rtA.mu.Unlock()
		st := rtA.spaces[line]
		return len(st.space.MemberCards(uint64(time.Now().Unix()))),
			st.space.Log.Len(), st.space.CurrentEpoch()
	}

	// A second barrier, and it has to be here rather than inside join(): the
	// request reaching JoinReady means B has been ANSWERED, which is enough to
	// know A has decided — the grant, the member_added and the epoch are all
	// written before the answer goes out. A's member CARD is not, because it
	// needs B's terminal manifest, which syncs separately and afterwards. So
	// the baseline below has to wait for the card as well, or it captures a
	// count of 1 and the first join's own card arriving late reads as the
	// second join admitting somebody. Only visible under -race, which is slow
	// enough to open the gap every time.
	waitUntil(t, 20*time.Second, "the first join's member card to arrive", func() bool {
		m, _, _ := observe()
		return m == 2
	})
	members, events, epoch := observe()

	join("second") // the same person, a second link

	gotMembers, gotEvents, gotEpoch := observe()
	if got := gotMembers; got != members {
		t.Fatalf("the second join changed the member count: %d → %d", members, got)
	}
	if got := gotEvents; got != events {
		t.Fatalf("the second join wrote %d event(s) for somebody already here",
			got-events)
	}
	if got := gotEpoch; got != epoch {
		t.Fatalf("the second join rotated the epoch (%d → %d), re-keying the "+
			"space for everyone because one member asked to come in again",
			epoch, got)
	}
}

// While it is unclear who is there, the line should say what it knows rather
// than repeat its own default title back at the person who made it.
func TestALineSaysWhatItKnows(t *testing.T) {
	rtA := openRuntime(t, t.TempDir(), "alice")
	defer rtA.Close()
	rtB := openRuntime(t, t.TempDir(), "bob")
	defer rtB.Close()
	srv, _ := setUpRelay(t, rtA, rtB)
	defer srv.Close()

	info, err := rtA.MintQuickLink(id.TerminalID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	line, _ := rtA.LineSpace()
	shown := func(rt *Runtime, tid id.TerminalID) string {
		for _, s := range rt.Spaces() {
			if s.ID == tid {
				return s.DisplayTitle
			}
		}
		return ""
	}
	if got := shown(rtA, line); got != "waiting for someone" {
		t.Fatalf("an empty line should say so, showed %q", got)
	}

	p, err := rtB.ResolveQuickLink(info.Phrase)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := rtB.JoinByPass(p.PassLink)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "the join to be answered", func() bool {
		st, _ := rtB.JoinStatus(reqID)
		return st == JoinReady
	})
	waitUntil(t, 20*time.Second, "bob's name to arrive", func() bool {
		return shown(rtA, line) == "bob"
	})
	// A named space is never overridden, however few people are in it.
	named, err := rtA.CreateSpace("Release notes")
	if err != nil {
		t.Fatal(err)
	}
	if got := shown(rtA, named); got != "Release notes" {
		t.Fatalf("a named space lost its name: %q", got)
	}
}
