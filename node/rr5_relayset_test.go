// RR-5: the signed relay set drives resolution end to end on the node.
package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/terminals"
)

func TestPolicyRelaySetDrivesResolution(t *testing.T) {
	r := openRuntime(t, t.TempDir(), "owner")
	defer r.Close()
	s := r.GetSettings()
	s.Relay = "127.0.0.1:7411" // personal
	if err := r.SetSettings(s); err != nil {
		t.Fatal(err)
	}

	tid, err := r.CreateSpaceWithOptions("сад", CreateOptions{
		Character: terminals.DefaultCharacter("campfire"),
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen, Publish: terminals.PublishAll,
			Relays: []string{"custom:tls://203.0.113.9:7411"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The SIGNED set outranks the owner's personal relay for both purposes.
	if got := r.ResolvePublicWriteRelay(tid); got != "203.0.113.9:7411" {
		t.Fatalf("write = %q", got)
	}
	if got := r.ResolvePublicReadRelay(tid); got != "203.0.113.9:7411" {
		t.Fatalf("read = %q", got)
	}

	// A revision moves the set — anti-rollback and distribution are the
	// manifest's own machinery.
	next := []string{"custom:tls://203.0.113.10:7411"}
	if err := r.RevisePolicy(tid, PolicyDelta{Relays: &next}); err != nil {
		t.Fatal(err)
	}
	if got := r.ResolvePublicWriteRelay(tid); got != "203.0.113.10:7411" {
		t.Fatalf("write after revision = %q", got)
	}

	// An unknown official id: the set EXISTS but resolves to nothing —
	// unavailable, never the personal relay, for BOTH purposes.
	ghost := []string{"official:atlantis-1"}
	if err := r.RevisePolicy(tid, PolicyDelta{Relays: &ghost}); err != nil {
		t.Fatal(err)
	}
	if got := r.ResolvePublicWriteRelay(tid); got != "" {
		t.Fatalf("unresolvable set routed a WRITE to %q", got)
	}
	if got := r.ResolvePublicReadRelay(tid); got != "" {
		t.Fatalf("unresolvable set routed a READ to %q", got)
	}

	// A malformed ref is refused at the revision door.
	bad := []string{"not-a-ref"}
	if err := r.RevisePolicy(tid, PolicyDelta{Relays: &bad}); err == nil {
		t.Fatal("a malformed relay ref was signed into policy")
	}
}
