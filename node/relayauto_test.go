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
	"time"
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

// Switching to automatic used to save the preference and do nothing: the
// only thing that ever ran a selection was Open, so a node that had never
// measured anything sat with NO personal relay until somebody restarted
// it — and every screen that needs one refused meanwhile, which is how the
// defect above stayed hidden.
//
// The state that matters is an EMPTY selection: with a last-known-good on
// disk the resolvers answer from relays.json whether or not anything ran,
// so seeding one would make this test pass against the broken code. It did,
// on the first attempt.
func TestSwitchingToAutomaticSelectsWithoutARestart(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	srv, addr := setUpRelay(t, rt)
	defer srv.Close()

	// Selection only ever probes REGISTRY entries, so point the registry at
	// the relay this test actually has.
	withRelayRegistry(t, RelayDescriptor{
		ID: "local-dev", Endpoint: addr, Region: "local", Priority: 100,
		ProtocolMin: 1, ProtocolMax: 1, Official: true,
		Roles: []string{RelayRoleBootstrap, RelayRolePersonalInbox},
	})

	// Custom mode, and NOTHING measured yet.
	s := rt.GetSettings()
	s.RelayMode = "custom"
	s.Relay = addr
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := rt.updateRelayState(func(st *RelayLocalState) {
		st.SelectedPrimary = ""
		st.SelectedBackup = ""
	}); err != nil {
		t.Fatal(err)
	}
	if rt.ResolvePersonalRelay() != addr {
		t.Fatal("test premise wrong: custom mode is not using the configured address")
	}

	s = rt.GetSettings()
	s.RelayMode = "automatic"
	s.Relay = ""
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	// The measurement runs in the background, exactly as at Open — nothing
	// here waits on a probe, so neither does a person pressing Save.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := rt.ResolvePersonalRelay(); got != "" {
			if got != addr {
				t.Fatalf("selected %q, wanted the only reachable relay %q", got, addr)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("switching to automatic left the node with no relay until a restart")
}

// The ladder is MEASURED, so the nearest reachable relay wins: a relay on
// this machine beats one across the internet whenever it is up, and the
// remote one is what a node falls to when it is not. Priority is only an
// administrative tie-break and must never override that.
func TestTheRegistryPrefersWhateverIsNearer(t *testing.T) {
	// The SHIPPED registry, not the empty one the suite runs against —
	// this test's subject is what the binary carries.
	var local, remote *RelayDescriptor
	for i := range shippedRelayRegistry.Relays {
		switch shippedRelayRegistry.Relays[i].ID {
		case "local-dev":
			local = &shippedRelayRegistry.Relays[i]
		case "staging-1":
			remote = &shippedRelayRegistry.Relays[i]
		}
	}
	if local == nil || remote == nil {
		t.Fatal("the registry lost one of its two entries")
	}
	if local.Priority <= remote.Priority {
		t.Fatal("the local relay stopped being the administrative first choice")
	}
	// Both must be candidates at all, or the ladder has only one rung.
	cands := shippedRelayRegistry.Compatible(RelayProtocolVersionMin, RelayProtocolVersionMax)
	if len(cands) < 2 {
		t.Fatalf("only %d relay is a candidate — nothing to fall back to", len(cands))
	}
	// A relay reached across the internet is PINNED; one on this machine is
	// the local-lan profile and deliberately is not.
	if len(remote.SPKIPins) == 0 {
		t.Fatal("a relay dialled over the internet must carry a pin")
	}
	if len(local.SPKIPins) != 0 {
		t.Fatal("the loopback relay gained a pin — that is the local-lan profile gone")
	}
	// A different region, so backup selection has real failure-domain
	// diversity to work with rather than two names for one place.
	if local.Region == remote.Region {
		t.Fatal("both relays claim one region — the backup would share its fate")
	}
}

// A FRESH INSTALL measures. "" has always meant legacy-custom, which is the
// right conservative reading for a node somebody already set up — but on a
// brand-new one it meant no relay, no probe, and an app that quietly does
// nothing until its owner goes looking for a settings field. That is the
// state every build handed to a friend starts in.
func TestAFreshInstallMeasuresRatherThanSittingIdle(t *testing.T) {
	dir := t.TempDir()

	// Stand a relay up first, and point the registry at it — selection only
	// ever probes registry entries.
	host := openRuntime(t, t.TempDir(), "host")
	defer host.Close()
	srv, addr := setUpRelay(t, host)
	defer srv.Close()
	withRelayRegistry(t, RelayDescriptor{
		ID: "local-dev", Endpoint: addr, Region: "local", Priority: 100,
		ProtocolMin: 1, ProtocolMax: 1, Official: true,
		Roles: []string{RelayRoleBootstrap, RelayRolePersonalInbox},
	})

	// A node opened for the very first time: no settings blob at all.
	fresh := openRuntime(t, dir, "newcomer")
	defer fresh.Close()
	if got := fresh.GetSettings().RelayMode; got != "" {
		t.Fatalf("test premise wrong: a fresh node reports mode %q", got)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fresh.ResolvePersonalRelay() == addr {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a fresh install never looked for a relay")
}

// ...and somebody who DID choose custom keeps exactly that, blank address
// included. A preference is not a gap to be helpfully filled in.
func TestADeliberateCustomChoiceIsNotOverridden(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	s := rt.GetSettings()
	s.RelayMode = "custom"
	s.Relay = ""
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	again := openRuntime(t, dir, "alice")
	defer again.Close()
	if got := again.GetSettings().RelayMode; got != "custom" {
		t.Fatalf("the chosen mode changed to %q", got)
	}
	time.Sleep(500 * time.Millisecond)
	if got := again.ResolvePersonalRelay(); got != "" {
		t.Fatalf("a deliberate custom-with-no-relay node was given %q anyway", got)
	}
}

// The settings screen reports the mode IN EFFECT. A fresh node measures, so
// showing it "Custom" — which the API did, because it mapped "" to custom
// unconditionally — would be a plain lie about what the node is doing.
func TestTheSettingsScreenSaysWhichModeIsActuallyInEffect(t *testing.T) {
	cases := []struct {
		name  string
		s     Settings
		wants string
	}{
		{"a fresh node", Settings{}, "automatic"},
		{"chose automatic", Settings{RelayMode: "automatic"}, "automatic"},
		{"chose custom, blank", Settings{RelayMode: "custom"}, "custom"},
		{"pre-modes, with an address", Settings{Relay: "203.0.113.7:7411"}, "custom"},
	}
	for _, c := range cases {
		if got := settingsJSON(c.s)["relay_mode"]; got != c.wants {
			t.Errorf("%s: screen says %q, node does %q", c.name, got, c.wants)
		}
	}
}
