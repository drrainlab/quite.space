package node

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/quicklink"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// setUpRelay gives a runtime a relay and returns its address.
func setUpRelay(t *testing.T, rts ...*Runtime) (*relay.Server, string) {
	t.Helper()
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
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
	if !strings.Contains(err.Error(), "works once") {
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

// The line is created once and reused; a second link must not spawn another.
func TestTheLineIsCreatedOnceAndReused(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	srv, _ := setUpRelay(t, rt)
	defer srv.Close()

	a, err := rt.MintQuickLink(id.TerminalID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	n := len(rt.Spaces())
	b, err := rt.MintQuickLink(id.TerminalID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Space != b.Space {
		t.Fatalf("two links led to different lines: %s vs %s", a.Space, b.Space)
	}
	if got := len(rt.Spaces()); got != n {
		t.Fatalf("a second link created another space: %d → %d", n, got)
	}
	if a.Link == b.Link {
		t.Fatal("two links reused the same words")
	}
}
