package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
)

// originOf reads the provenance off the newest message in a space.
func originOf(t *testing.T, rt *Runtime, tid id.TerminalID) (*schemas.ShareOrigin, string) {
	t.Helper()
	sp, _ := rt.spaceForTest(tid)
	entries := sp.State.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if c := entries[i].Content.Text; c != nil && c.Origin != nil {
			return c.Origin, c.Text
		}
	}
	return nil, ""
}

func textsOf(t *testing.T, rt *Runtime, tid id.TerminalID) []string {
	t.Helper()
	sp, _ := rt.spaceForTest(tid)
	var out []string
	for _, e := range sp.State.Entries() {
		if e.Content.Text != nil {
			out = append(out, e.Content.Text.Text)
		}
	}
	return out
}

// The headline: a message goes somewhere else, and what arrives says whose
// words they are AND that it is only somebody's word for it.
func TestAMessageCanBeQuotedIntoAnotherSpace(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, err := rt.CreateSpace("where it was said")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := rt.CreateSpace("where it goes")
	if err != nil {
		t.Fatal(err)
	}
	eid, err := rt.Say(src, "the tide comes in twice a day", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}

	res, err := rt.Share(src, eid, []id.TerminalID{dst},
		ShareOptions{Comment: "look at this", NameAuthor: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("it did not arrive: %+v", res)
	}

	// TWO events, in order: the comment as an ordinary message of your own,
	// then the quotation.
	texts := textsOf(t, rt, dst)
	if len(texts) != 2 {
		t.Fatalf("expected a comment and a copy, got %d: %q", len(texts), texts)
	}
	if texts[0] != "look at this" {
		t.Fatalf("the comment is not a plain message of its own: %q", texts[0])
	}
	o, composed := originOf(t, rt, dst)
	if o == nil {
		t.Fatal("the copy carries no provenance")
	}
	if o.AuthorLabel != "alice" {
		t.Fatalf("the author claim is %q", o.AuthorLabel)
	}
	// A client that has never heard of key 4 still sees the quotation.
	if !strings.Contains(composed, "> the tide comes in twice a day") {
		t.Fatalf("the composed form does not carry the quote: %q", composed)
	}
	if !strings.Contains(composed, "> alice") {
		t.Fatalf("the composed form does not name the author: %q", composed)
	}
}

// The provenance minimum is a TEST, not an intention.
func TestTheQuotationCarriesNoWayBack(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, _ := rt.CreateSpace("a private room")
	dst, _ := rt.CreateSpace("somewhere else")
	eid, err := rt.Say(src, "something said in private", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Share(src, eid, []id.TerminalID{dst},
		ShareOptions{NameAuthor: true}); err != nil {
		t.Fatal(err)
	}
	o, composed := originOf(t, rt, dst)
	if o == nil {
		t.Fatal("no provenance at all")
	}
	// Not the source space id, not the original event id — either would be
	// a permanent cross-space correlator for anyone holding both.
	for _, forbidden := range []string{src.Hex(), eid.Hex(), src.Hex()[:16], eid.Hex()[:16]} {
		if strings.Contains(composed, forbidden) {
			t.Fatalf("the copy leaks an identifier: %q", forbidden)
		}
	}
	// The source NAME is off unless asked for, even here where the sender
	// owns the space: it discloses that THEY are in it.
	if o.SourceLabel != "" {
		t.Fatalf("the source space was named without being asked: %q", o.SourceLabel)
	}
	if _, err := rt.Share(src, eid, []id.TerminalID{dst},
		ShareOptions{NameAuthor: true, NameSource: true}); err != nil {
		t.Fatal(err)
	}
	if o2, _ := originOf(t, rt, dst); o2.SourceLabel != "a private room" {
		t.Fatalf("asking for the source name did not carry it: %q", o2.SourceLabel)
	}
}

// A space that promised the past stays with those who lived it does not get
// quoted out of — and the refusal writes nothing anywhere.
func TestPrivateHistoryIsNotQuotedOutOf(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	ch := terminals.DefaultCharacter("campfire")
	ch.Memory = "private_history"
	src, err := rt.CreateSpaceWithOptions("the closed room", CreateOptions{Character: ch})
	if err != nil {
		t.Fatal(err)
	}
	dst, _ := rt.CreateSpace("somewhere else")
	eid, err := rt.Say(src, "what was said in there", SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	beforeSrc := len(textsOf(t, rt, src))
	beforeDst := len(textsOf(t, rt, dst))

	_, err = rt.Share(src, eid, []id.TerminalID{dst},
		ShareOptions{Comment: "look", NameAuthor: true})
	if err == nil {
		t.Fatal("a private-history space was quoted out of")
	}
	if !strings.Contains(err.Error(), "past stays with those who lived it") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	if len(textsOf(t, rt, src)) != beforeSrc {
		t.Fatal("the refusal wrote into the source")
	}
	if len(textsOf(t, rt, dst)) != beforeDst {
		t.Fatal("the refusal wrote into the destination — including the comment")
	}
}

// Media is not refused, it is deferred — and the two sentences are
// different. Also: a thing that may never be quoted says so without "yet".
func TestWhatCannotBeQuotedSaysWhichKindOfNo(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, _ := rt.CreateSpace("source")
	dst, _ := rt.CreateSpace("dest")

	// A voice message: shareable one day, not in this build.
	payload, err := (&schemas.VoiceBlock{
		Original: &schemas.AssetRef{AssetID: [16]byte{1}, MediaType: "audio/ogg",
			Size: 10, ChunkSize: schemas.AllowedChunkSizes[0],
			InlineChunks: []id.Hash{{3}},
			Version:      2, ContentID: id.Hash{2}},
		DurationMS: 1200,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	veid, err := rt.EmitBlock(src, schemas.BlockVoice, payload)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.Share(src, veid, []id.TerminalID{dst}, ShareOptions{NameAuthor: true})
	if err == nil || !strings.Contains(err.Error(), "cannot be forwarded yet") {
		t.Fatalf("a deferred kind did not say 'yet': %v", err)
	}
	if n := len(textsOf(t, rt, dst)); n != 0 {
		t.Fatalf("a refused share still wrote %d messages", n)
	}
}

// Partial success is the normal case. One frozen destination must not cost
// the others their copy.
func TestOneClosedDestinationDoesNotStopTheRest(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, _ := rt.CreateSpace("source")
	good, _ := rt.CreateSpace("open")
	frozen, err := rt.CreateSpaceWithOptions("frozen", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic, Publish: terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := rt.RevisePolicy(frozen, PolicyDelta{Frozen: &yes}); err != nil {
		t.Fatal(err)
	}
	eid, _ := rt.Say(src, "one thing", SayOptions{})

	res, err := rt.Share(src, eid, []id.TerminalID{frozen, good, src},
		ShareOptions{NameAuthor: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("every target needs its own row: %+v", res)
	}
	byID := map[string]ShareResult{}
	for _, x := range res {
		byID[x.Space] = x
	}
	if byID[frozen.Hex()].OK || byID[frozen.Hex()].Error == "" {
		t.Fatalf("a frozen space accepted a copy: %+v", byID[frozen.Hex()])
	}
	if !byID[good.Hex()].OK {
		t.Fatalf("one closed destination cost another its copy: %+v", byID[good.Hex()])
	}
	if byID[src.Hex()].OK || !strings.Contains(byID[src.Hex()].Error, "already in that space") {
		t.Fatalf("sending a message back into its own space: %+v", byID[src.Hex()])
	}
}

// The copies must share nothing that ties them together. Recipients learn
// about each other structurally or not at all.
func TestTheCopiesShareNoCorrelator(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, _ := rt.CreateSpace("source")
	a, _ := rt.CreateSpace("first")
	b, _ := rt.CreateSpace("second")
	eid, _ := rt.Say(src, "the same words to two places", SayOptions{})

	res, err := rt.Share(src, eid, []id.TerminalID{a, b}, ShareOptions{NameAuthor: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].OK || !res[1].OK {
		t.Fatalf("both should have arrived: %+v", res)
	}
	if res[0].Copy == res[1].Copy {
		t.Fatal("the two copies are the same event")
	}
	// The TEXT is deliberately identical — it is the same quotation — but
	// neither copy names the other destination or the source.
	_, ta := originOf(t, rt, a)
	_, tb := originOf(t, rt, b)
	if ta != tb {
		t.Fatalf("the same quotation rendered differently:\n%q\n%q", ta, tb)
	}
	for _, body := range []string{ta, tb} {
		for _, forbidden := range []string{a.Hex(), b.Hex(), src.Hex(), "first", "second"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("a copy names something it should not: %q in %q", forbidden, body)
			}
		}
	}
}

// A share of a share quotes what is in front of you, attributed to whoever
// put it there. Structured provenance never copies onward.
func TestAShareOfAShareDoesNotLaunderTheClaim(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, _ := rt.CreateSpace("first room")
	mid, _ := rt.CreateSpace("second room")
	last, _ := rt.CreateSpace("third room")
	eid, _ := rt.Say(src, "the original sentence", SayOptions{})

	if _, err := rt.Share(src, eid, []id.TerminalID{mid},
		ShareOptions{NameAuthor: true, NameSource: true}); err != nil {
		t.Fatal(err)
	}
	// Find the copy that landed in mid, and pass IT on.
	sp, _ := rt.spaceForTest(mid)
	var second id.EventID
	for _, e := range sp.State.Entries() {
		if e.Content.Text != nil && e.Content.Text.Origin != nil {
			second = e.ID
		}
	}
	if _, err := rt.Share(mid, second, []id.TerminalID{last},
		ShareOptions{NameAuthor: true}); err != nil {
		t.Fatal(err)
	}
	o, body := originOf(t, rt, last)
	if o == nil {
		t.Fatal("the second share carries no provenance")
	}
	// The inner claim survives only as PROSE inside the quotation, never as
	// a structured field that a renderer would present as this share's own.
	if o.SourceLabel != "" {
		t.Fatalf("the first share's source label copied onward: %q", o.SourceLabel)
	}
	if !strings.Contains(body, "the original sentence") {
		t.Fatalf("the words did not survive two hops: %q", body)
	}
}

// A quote is cut on a rune boundary and says that it was cut.
func TestALongQuoteIsCutHonestly(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, _ := rt.CreateSpace("source")
	dst, _ := rt.CreateSpace("dest")
	long := strings.Repeat("тишина ", 400) // multi-byte on purpose
	eid, err := rt.Say(src, long, SayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Share(src, eid, []id.TerminalID{dst},
		ShareOptions{NameAuthor: true}); err != nil {
		t.Fatal(err)
	}
	o, body := originOf(t, rt, dst)
	if !o.Truncated {
		t.Fatal("a cut quote does not say it was cut")
	}
	if !strings.Contains(body, "…") {
		t.Fatalf("the composed form hides the cut: %q", body[len(body)-40:])
	}
	if strings.Contains(body, "�") {
		t.Fatal("the cut landed inside a character")
	}
}

// A revised message is quoted as it reads NOW; a deleted one is not quoted
// at all, and the two are different sentences from "unknown".
func TestADeletedMessageIsNotQuoted(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	src, _ := rt.CreateSpace("source")
	dst, _ := rt.CreateSpace("dest")
	var missing id.EventID
	missing[0] = 0xAB
	_, err := rt.Share(src, missing, []id.TerminalID{dst}, ShareOptions{NameAuthor: true})
	if err == nil || !strings.Contains(err.Error(), "not here any more") {
		t.Fatalf("a message that is not there gave: %v", err)
	}
	if n := len(textsOf(t, rt, dst)); n != 0 {
		t.Fatal("a refused share still wrote something")
	}
}

// Found live, and it is the kind that misattributes quietly: the local
// assistant is an Agent Terminal controlled by YOUR principal, so a name
// map keyed by principal alone let its card rename you — a message alice
// forwarded read "Quite AI says this came from alice". Which name is right
// depends on who WROTE the event, not on whose principal signs for it.
func TestTheAssistantDoesNotRenameYou(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	ai, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	src, _ := rt.CreateSpace("source")
	eid, _ := rt.Say(src, "the tide comes in twice a day", SayOptions{})
	if _, err := rt.Share(src, eid, []id.TerminalID{ai},
		ShareOptions{NameAuthor: true}); err != nil {
		t.Fatal(err)
	}
	o, _ := originOf(t, rt, ai)
	if o.AuthorLabel != "alice" {
		t.Fatalf("the quotation attributes alice's words to %q", o.AuthorLabel)
	}
}
