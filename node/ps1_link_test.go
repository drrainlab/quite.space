// The post address (PS-1): space:<tid>[:<doc>] — the same envelope as
// ever, with a landing hint. The address never encodes what the holder
// does with it; reading and following are choices made later.
package node

import (
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
)

func TestPostAddressRoundTrip(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	s := rt.GetSettings()
	s.Relay = "relay.example:7411"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	tid, err := rt.CreateSpaceWithOptions("field notes", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	doc := [16]byte{1, 2, 3, 4}
	link, err := rt.ComposePublicLink(tid, &doc)
	if err != nil {
		t.Fatal(err)
	}
	relayAddr, gotTid, gotDoc, err := ParsePublicLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if relayAddr != "relay.example:7411" || gotTid != tid {
		t.Fatalf("the address changed in transit: %q %s", relayAddr, gotTid.Hex()[:8])
	}
	if gotDoc == nil || *gotDoc != doc {
		t.Fatalf("the landing hint was lost: %v", gotDoc)
	}

	// Without a document the third field is simply absent — yesterday's
	// space link, parsed by the same grammar.
	link2, err := rt.ComposePublicLink(tid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, d, err := ParsePublicLink(link2); err != nil || d != nil {
		t.Fatalf("a plain space link grew a document: %v %v", d, err)
	}
}

func TestAMangledDocumentFieldIsRefusedNotIgnored(t *testing.T) {
	// A link whose third field is not 16 hex bytes is malformed, and
	// malformed is an answer — not something to interpret generously.
	link := composeShare("relay.example:7411",
		"space:"+strings.Repeat("ab", 32)+":zzzz")
	if _, _, _, err := ParsePublicLink(link); err == nil {
		t.Fatal("a mangled document id parsed")
	}
}

// The relay in a composed reference prefers the address a projection
// actually ARRIVED from: a reader forwarding somebody else's post must
// not mint a link pointing at their own relay.
func TestReferencePrefersTheObservedSourceRelay(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	s := rt.GetSettings()
	s.Relay = "my-own-relay:7411"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	tid, err := rt.CreateSpaceWithOptions("theirs", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate what fetchPublicProjection records when a projection lands.
	rt.mu.Lock()
	meta := rt.ks.Spaces[tid]
	meta.SourceRelay = "publishers-relay:7411"
	rt.ks.Spaces[tid] = meta
	rt.mu.Unlock()

	link, err := rt.ComposePublicLink(tid, nil)
	if err != nil {
		t.Fatal(err)
	}
	relayAddr, _, _, err := ParsePublicLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if relayAddr != "publishers-relay:7411" {
		t.Fatalf("the reference carries the wrong relay: %q", relayAddr)
	}
}

// The observation survives a restart — it lives in SpaceMeta, element 15,
// behind the trailing skip loop that keeps older builds able to open it.
func TestSourceRelaySurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpaceWithOptions("observed", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	meta := rt.ks.Spaces[tid]
	meta.SourceRelay = "observed-relay:7411"
	rt.ks.Spaces[tid] = meta
	err = rt.saveKeystore()
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	if got := rt2.ks.Spaces[tid].SourceRelay; got != "observed-relay:7411" {
		t.Fatalf("the observed relay did not survive: %q", got)
	}
}

func TestLocalOnlyIsNeverReferenceable(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	ai, err := rt.EnsureAISpace()
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	ok := rt.canReferenceByPublicLinkLocked(ai)
	rt.mu.Unlock()
	if ok {
		t.Fatal("a space that must never leave the device can be pointed at from outside it")
	}
	// And an unknown space is not referenceable either.
	rt.mu.Lock()
	ok = rt.canReferenceByPublicLinkLocked(id.TerminalID{9})
	rt.mu.Unlock()
	if ok {
		t.Fatal("an unknown space is referenceable")
	}
}
