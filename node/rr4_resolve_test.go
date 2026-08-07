// RR-4: per-purpose relay resolution. The one property the whole gate
// exists for: a PUBLIC WRITE never falls back to the personal relay for
// a non-owner — publishing where the members do not look is the silent
// failure mode a universal fallback ladder would have shipped.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func TestPublicWriteNeverFallsBackToThePersonalRelay(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "writer")
	defer r.Close()

	s := r.GetSettings()
	s.Relay = "127.0.0.1:7411" // the personal relay
	if err := r.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	// A followed space with NO recorded source relay: the write resolver
	// must HOLD (empty), not leak to the personal address.
	tid := id.TerminalID{9, 9, 9}
	r.mu.Lock()
	r.ks.Spaces[tid] = storage.SpaceMeta{Title: "followed", Role: storage.RoleReader}
	r.mu.Unlock()
	if got := r.ResolvePublicWriteRelay(tid); got != "" {
		t.Fatalf("a non-owner write resolved to %q — the silent wrong-mailbox publish", got)
	}
	// READS may probe the personal relay — a wrong guess misleads nobody.
	if got := r.ResolvePublicReadRelay(tid); got != "127.0.0.1:7411" {
		t.Fatalf("read fallback = %q", got)
	}

	// Once the source relay is OBSERVED, both purposes use it.
	r.mu.Lock()
	meta := r.ks.Spaces[tid]
	meta.SourceRelay = "203.0.113.5:7411"
	r.ks.Spaces[tid] = meta
	r.mu.Unlock()
	if got := r.ResolvePublicWriteRelay(tid); got != "203.0.113.5:7411" {
		t.Fatalf("write ignored the source relay: %q", got)
	}
	if got := r.ResolvePublicReadRelay(tid); got != "203.0.113.5:7411" {
		t.Fatalf("read ignored the source relay: %q", got)
	}

	// An OWNED space pre-RR-5: the owner's personal relay IS the implicit
	// relay set.
	own := id.TerminalID{7, 7, 7}
	r.mu.Lock()
	r.ks.Spaces[own] = storage.SpaceMeta{Title: "mine", Owned: true}
	r.mu.Unlock()
	if got := r.ResolvePublicWriteRelay(own); got != "127.0.0.1:7411" {
		t.Fatalf("owner write = %q", got)
	}
}

func TestQuicklinkCandidatesAreBoundedAndOrdered(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "cands")
	defer r.Close()

	s := r.GetSettings()
	s.Relay = "10.0.0.1:7411"
	if err := r.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	cands := r.quicklinkCandidates()
	if len(cands) == 0 || cands[0] != "10.0.0.1:7411" {
		t.Fatalf("personal relay must lead: %v", cands)
	}
	if len(cands) > 3 {
		t.Fatalf("fan-out unbounded: %d candidates", len(cands))
	}
	// The registry's local-dev entry rides behind it, deduped.
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c] {
			t.Fatalf("duplicate candidate %q", c)
		}
		seen[c] = true
	}
}

func TestPersonalResolverModes(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "personal")
	defer r.Close()

	// Custom mode: exactly the configured address.
	s := r.GetSettings()
	s.Relay, s.RelayMode = "10.1.1.1:7411", "custom"
	if err := r.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := r.ResolvePersonalRelay(); got != "10.1.1.1:7411" {
		t.Fatalf("custom = %q", got)
	}

	// Automatic with nothing measured: no relay, no silent fallback to
	// the stale custom field.
	s = r.GetSettings()
	s.RelayMode = "automatic"
	if err := r.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := r.ResolvePersonalRelay(); got != "" {
		t.Fatalf("automatic-unmeasured = %q, want empty", got)
	}

	// Automatic with a measured selection: the registry-resolved endpoint.
	// The suite runs with an empty registry (see relayreg_testmain_test.go),
	// so the name this test resolves is installed here rather than borrowed
	// from whatever the binary happens to ship.
	withRelayRegistry(t, RelayDescriptor{
		ID: "local-dev", Endpoint: "127.0.0.1:7411", Region: "local",
		ProtocolMin: 1, ProtocolMax: 1, Official: true,
	})
	if err := r.updateRelayState(func(st *RelayLocalState) {
		st.SelectedPrimary = "official:local-dev"
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.ResolvePersonalRelay(); got != "127.0.0.1:7411" {
		t.Fatalf("automatic-selected = %q", got)
	}
}
