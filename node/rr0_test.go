// RR-0: settings hygiene, the pass-relay decision fix, the registry
// skeleton and the RelayRef grammar.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/attention"
)

// TestDecisionGoesToThePassRelayNotTheGlobalOne — before RR-0, DecideEntry
// dialed whatever relay Settings held at DECISION time. Mint on R1, point
// the global setting at a dead address, decide: the guest waiting on R1
// must still get their answer.
func TestDecisionGoesToThePassRelayNotTheGlobalOne(t *testing.T) {
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

	// The host's GLOBAL relay moves to a dead address. The pass record
	// still remembers where it was minted.
	s := alice.GetSettings()
	s.Relay = "127.0.0.1:1"
	if err := alice.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	queue := alice.EntryRequests()
	if len(queue) != 1 {
		t.Fatalf("expected one person at the door: %+v", queue)
	}
	if err := alice.DecideEntry(queue[0].RequestID, true, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, bob, req, JoinReady)
}

// TestSettingsOmitGuards — before RR-0, any settings write that did not
// carry the relay or the attention policy silently erased them.
func TestSettingsOmitGuards(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "guards")
	defer r.Close()

	s := r.GetSettings()
	s.Relay = "127.0.0.1:7411"
	s.RelayMode = "custom"
	pol := attention.DefaultPolicy()
	s.Attention = &pol
	if err := r.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	// A write from a screen that shows neither relay nor attention.
	blank := r.GetSettings()
	blank.Theme = "dark"
	blank.Attention = nil // omitted by the caller
	if err := r.SetSettings(blank); err != nil {
		t.Fatal(err)
	}
	got := r.GetSettings()
	if got.Relay != "127.0.0.1:7411" || got.RelayMode != "custom" {
		t.Fatalf("relay config was preserved wrong: %q mode %q", got.Relay, got.RelayMode)
	}
	if got.Attention == nil {
		t.Fatal("an omitted attention policy erased the stored one")
	}
	if got.Theme != "dark" {
		t.Fatal("the actual change did not land")
	}
}

func TestUnknownRelayModeIsRefusedAtTheDoor(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "badmode")
	defer r.Close()
	s := r.GetSettings()
	s.RelayMode = "turbo"
	if err := r.SetSettings(s); err == nil {
		t.Fatal("an unreadable relay mode reached storage")
	} else if _, ok := err.(ErrBadRelayMode); !ok {
		t.Fatalf("wrong error type: %v", err)
	}
}

// ---- RelayRef grammar ----

func TestRelayRefGrammar(t *testing.T) {
	ok := []struct{ in, out string }{
		{"official:eu-1", "official:eu-1"},
		{"custom:tls://relay.example.org:7411", "custom:tls://relay.example.org:7411"},
		{"custom:tls://127.0.0.1:7411", "custom:tls://127.0.0.1:7411"},
		{"  official:local-dev  ", "official:local-dev"},
	}
	for _, c := range ok {
		ref, err := ParseRelayRef(c.in)
		if err != nil {
			t.Fatalf("%q refused: %v", c.in, err)
		}
		if ref.String() != c.out {
			t.Fatalf("%q round-tripped to %q", c.in, ref.String())
		}
	}
	bad := []string{
		"", "eu-1", "official:", "official:has space", "official:a/b",
		"custom:relay.example.org:7411", "custom:tls://", "custom:tls://noport",
		"custom:tls://host:", "custom:tls://host:notnum",
		"http://relay.example.org:7411",
	}
	for _, in := range bad {
		if _, err := ParseRelayRef(in); err == nil {
			t.Fatalf("%q was accepted", in)
		}
	}
}

// An unknown official id resolves to NOTHING — never a custom endpoint,
// never anyone's personal relay.
func TestUnknownOfficialIDIsUnavailable(t *testing.T) {
	ref, err := ParseRelayRef("official:atlantis-1")
	if err != nil {
		t.Fatal(err)
	}
	if ep, ok := ref.Resolve(BuiltinRelayRegistry); ok || ep != "" {
		t.Fatalf("an unknown official id resolved to %q", ep)
	}
	// The known one does resolve.
	known, _ := ParseRelayRef("official:local-dev")
	if ep, ok := known.Resolve(BuiltinRelayRegistry); !ok || ep != "127.0.0.1:7411" {
		t.Fatalf("local-dev resolved to %q %v", ep, ok)
	}
}

func TestRegistryCompatibilityFilter(t *testing.T) {
	if got := BuiltinRelayRegistry.Compatible(1, 1); len(got) != 1 {
		t.Fatalf("expected the local-dev entry, got %d", len(got))
	}
	if got := BuiltinRelayRegistry.Compatible(2, 9); len(got) != 0 {
		t.Fatal("a protocol-2 client matched a protocol-1 relay")
	}
}

func TestLocalLANProfile(t *testing.T) {
	d, _ := BuiltinRelayRegistry.ByID("local-dev")
	if !d.LocalLAN() {
		t.Fatal("loopback without pins must be the local-lan profile")
	}
	pinned := RelayDescriptor{Endpoint: "127.0.0.1:7411", SPKIPins: []string{"x"}}
	if pinned.LocalLAN() {
		t.Fatal("a pinned descriptor is never local-lan")
	}
	remote := RelayDescriptor{Endpoint: "eu-1.quite.space:7411"}
	if remote.LocalLAN() {
		t.Fatal("an unpinned REMOTE endpoint is not local-lan")
	}
}

// ---- relays.json ----

func TestRelayStateRoundTrip(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "relstate")
	defer r.Close()

	err := r.updateRelayState(func(st *RelayLocalState) {
		st.SelectedPrimary = "official:local-dev"
		st.Stats["official:local-dev"] = &RelayProbeStats{RTTEWMAMs: 12.5, SuccessCount: 3}
		st.Trust = append(st.Trust, RelayTrust{
			Endpoint: "relay.example.org:7411", SPKIPin: "abc", ConfirmedUnix: 1,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	st := r.loadRelayState()
	if st.SelectedPrimary != "official:local-dev" {
		t.Fatalf("primary lost: %q", st.SelectedPrimary)
	}
	if s := st.Stats["official:local-dev"]; s == nil || s.RTTEWMAMs != 12.5 {
		t.Fatal("stats lost")
	}
	if pin, ok := st.TrustedPin("relay.example.org:7411"); !ok || pin != "abc" {
		t.Fatal("trust record lost")
	}
	if _, ok := st.TrustedPin("other:1"); ok {
		t.Fatal("phantom trust")
	}
}
