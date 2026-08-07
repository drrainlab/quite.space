// Automatic relay mode is the DEFAULT, and in it Settings.Relay is empty.
//
// RR-3 keeps the measured selection in relays.json rather than in Settings,
// deliberately: a stale probe result sitting in a settings field would read
// as an explicit choice somebody made. RR-4 then introduced the resolvers
// and moved the sync loop onto them — and six call sites kept reading the
// raw field.
//
// The result was a node whose relay was healthy, selected and actively
// syncing, refusing to mint a quick link OR compose a share link, and
// telling the person to go and set the relay they had already set. Found
// live on the owner's own node; every test here fails against the code that
// read Settings.Relay directly.
package node

import (
	"strings"
	"testing"
)

// autoModeRuntime is a node configured the way the app configures itself:
// automatic mode, a working relay, and NOTHING in Settings.Relay.
func autoModeRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := openRuntime(t, t.TempDir(), "alice")
	srv, addr := setUpRelay(t, rt)
	t.Cleanup(srv.Close)

	// setUpRelay configures the address the old way. Put the node into the
	// state the product actually ships in: the selection is runtime state,
	// and the settings field is blank.
	s := rt.GetSettings()
	s.Relay = ""
	s.RelayMode = "automatic"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := rt.updateRelayState(func(st *RelayLocalState) {
		st.SelectedPrimary = "custom:tls://" + addr
	}); err != nil {
		t.Fatal(err)
	}

	if got := rt.GetSettings().Relay; got != "" {
		t.Fatalf("test premise wrong: Settings.Relay is %q, not empty", got)
	}
	if got := rt.ResolvePersonalRelay(); got == "" {
		t.Fatal("test premise wrong: the node has no resolvable relay at all")
	}
	return rt
}

// THE ONE THE OWNER HIT.
func TestAQuickLinkMintsWhenTheRelayIsAutomatic(t *testing.T) {
	rt := autoModeRuntime(t)
	defer rt.Close()

	info, err := rt.CreateQuickLink(QuickLinkOptions{Note: "for bob"})
	if err != nil {
		t.Fatalf("a node with a healthy selected relay refused to mint: %v", err)
	}
	if info.Phrase == "" || info.Link == "" {
		t.Fatalf("the link carries no words: %+v", info)
	}
}

// The same defect, one screen over: without this, Share link, + Add space
// and every directory link in CAT-0b refuse on a perfectly connected node.
func TestAShareLinkComposesWhenTheRelayIsAutomatic(t *testing.T) {
	rt := autoModeRuntime(t)
	defer rt.Close()

	space := newPublicSpace(t, rt, "a public room")
	link, err := rt.ComposePublicLink(space, nil)
	if err != nil {
		t.Fatalf("a public space refused to produce its own link: %v", err)
	}
	if link == "" {
		t.Fatal("the link is empty")
	}
	// And it round-trips to the space it names.
	_, tid, _, err := ParsePublicLink(link)
	if err != nil {
		t.Fatalf("the composed link does not parse: %v", err)
	}
	if tid != space {
		t.Fatal("the link names a different space")
	}
}

// The refusal that remains must be about the WORLD, not about a settings
// field the person cannot usefully fill in automatic mode.
func TestWithNoRelayAtAllTheRefusalStillSaysSo(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	s := rt.GetSettings()
	s.Relay = ""
	s.RelayMode = "automatic"
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	if _, err := rt.CreateQuickLink(QuickLinkOptions{}); err == nil {
		t.Fatal("a node with nowhere for a pass to wait minted a link anyway")
	} else if !strings.Contains(err.Error(), "wait") {
		t.Fatalf("the refusal changed its meaning: %v", err)
	}

	space := newPublicSpace(t, rt, "a public room")
	if _, err := rt.ComposePublicLink(space, nil); err == nil {
		t.Fatal("a link was composed with no relay to put in it")
	}
}
