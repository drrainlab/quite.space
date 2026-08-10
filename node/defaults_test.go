// A build may SUGGEST. It may not subscribe.
//
// The whole value of the file under test is a negative: a fresh node that
// ships with a directory address must not touch the network because of it.
// An app that quietly opened a space nobody asked for would tell the relay
// that this device is alive, and the space that somebody arrived, the first
// time it ran — before the person had agreed to anything.
package node

import (
	"encoding/base64"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// shareLinkTo builds a link in the shape the parser expects, without a
// publishing flow: these tests are about what the node does with a STRING.
func shareLinkTo(tid id.TerminalID, relay string) string {
	raw := relay + "\n" + "space:" + tid.Hex()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestAnUnbrandedBuildSuggestsNothing(t *testing.T) {
	// A build may ship with no official home at all — a fork, a private
	// deployment, somebody's own relay — and that has to stay a supported
	// shape rather than a broken one.
	old := DefaultDirectoryLink
	t.Cleanup(func() { DefaultDirectoryLink = old })
	DefaultDirectoryLink = ""

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	if got := rt.SuggestedDirectoryFor(); got.Link != "" {
		t.Fatalf("a build with no default offered %+v", got)
	}
}

// And the shipped one is real: a link this build cannot parse would mean
// every first press of Discover reaches nothing, which is the kind of
// packaging mistake nobody notices until a tester says the app is empty.
func TestTheShippedOfficialLinkIsWellFormed(t *testing.T) {
	if DefaultDirectoryLink == "" {
		t.Skip("this build ships no official directory")
	}
	relay, tid, _, err := ParsePublicLink(DefaultDirectoryLink)
	if err != nil {
		t.Fatalf("the official directory link does not parse: %v", err)
	}
	if relay == "" || tid == (id.TerminalID{}) {
		t.Fatalf("the official link carries no relay or no space: %q %v", relay, tid)
	}
}

func TestTheSuggestionIsNotOpenedByLookingAtIt(t *testing.T) {
	// A link to a relay that does not exist. If anything dialled, this test
	// would be slow or noisy; the assertion is that the node stays still and
	// still has no spaces afterwards.
	old := DefaultDirectoryLink
	oldT, oldN := DefaultDirectoryTitle, DefaultDirectoryNote
	t.Cleanup(func() {
		DefaultDirectoryLink, DefaultDirectoryTitle, DefaultDirectoryNote = old, oldT, oldN
	})

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	// Build a real link shape against an address nobody is listening on.
	tid, err := rt.CreateSpace("somewhere")
	if err != nil {
		t.Fatal(err)
	}
	link := shareLinkTo(tid, "198.51.100.9:7411")
	// A second node, which has never seen it.
	other := openRuntime(t, t.TempDir(), "bob")
	defer other.Close()

	DefaultDirectoryLink = link
	DefaultDirectoryTitle = "Quiet's own directory"
	DefaultDirectoryNote = "places other people keep"

	before := len(other.Spaces())
	got := other.SuggestedDirectoryFor()
	if got.Link != link || got.Title == "" {
		t.Fatalf("the build's own suggestion did not survive the trip: %+v", got)
	}
	if got.Held {
		t.Fatal("a node that has never seen the directory says it holds it")
	}
	if after := len(other.Spaces()); after != before {
		t.Fatalf("asking what this build suggests OPENED something: %d spaces "+
			"before, %d after", before, after)
	}
}

func TestOnceItIsHeldThereIsNothingToOffer(t *testing.T) {
	old := DefaultDirectoryLink
	t.Cleanup(func() { DefaultDirectoryLink = old })

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("the directory")
	if err != nil {
		t.Fatal(err)
	}
	DefaultDirectoryLink = shareLinkTo(tid, "198.51.100.9:7411")
	if got := rt.SuggestedDirectoryFor(); !got.Held {
		t.Fatal("the node holds this very space and was still offered it")
	}
}

func TestALinkNobodyCanParseIsNotShownToAnybody(t *testing.T) {
	old := DefaultDirectoryLink
	t.Cleanup(func() { DefaultDirectoryLink = old })
	DefaultDirectoryLink = "this is not a share link"

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	if got := rt.SuggestedDirectoryFor(); got.Link != "" {
		t.Fatalf("a packaging mistake reached the screen as %+v — the person "+
			"holding the phone cannot fix it and should not be asked to", got)
	}
}
