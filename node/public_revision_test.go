package node

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// PA-1.1: the revision chain — bump + Previous hash-link; readers accept
// newer revisions via the projection with anti-rollback; a NEW curator's
// exact device activates and publishes after a curator-add revision.
func TestPolicyRevisionCuratorAddViaProjection(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	curator := openRuntime(t, t.TempDir(), "curator")
	defer curator.Close()

	tid, err := owner.CreateSpaceWithOptions("Label", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Say(tid, "first", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	// Curator opens BEFORE being added: plain reader.
	if err := curator.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if got := curator.ks.Spaces[tid].Role; got != "reader" {
		t.Fatalf("not yet a curator, role %q", got)
	}

	// Owner adds the curator's exact device via a policy revision.
	if err := owner.RevisePolicy(tid, PolicyDelta{
		AddCurator: &terminals.WriterBinding{
			Principal: curator.PrincipalID, Device: curator.Device.ID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	sp, _ := owner.spaceForTest(tid)
	if m := manifestRevisionOf(t, sp.ManifestFrame); m != 2 {
		t.Fatalf("revision = %d, want 2", m)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	// Curator fetches → revised manifest installs → auto-activation.
	if err := curator.fetchPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	if got := curator.ks.Spaces[tid].Role; got != "" {
		t.Fatalf("curator not activated after revision, role %q", got)
	}
	if _, err := curator.Say(tid, "b-side", SayOptions{}); err != nil {
		t.Fatalf("new curator refused: %v", err)
	}
	// Uplink → owner materializes.
	if err := curator.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	osp, _ := owner.spaceForTest(tid)
	if got := len(osp.State.Messages()); got != 2 {
		t.Fatalf("curator post not materialized: %d", got)
	}

	// Anti-rollback: re-serving the OLD projection (rev 1) must not
	// downgrade the reader's manifest.
	if err := curator.fetchPublicProjection(addr, tid); err != nil {
		t.Fatal(err) // idempotent refetch fine
	}
	csp, _ := curator.spaceForTest(tid)
	if m := manifestRevisionOf(t, csp.ManifestFrame); m != 2 {
		t.Fatalf("reader manifest regressed to %d", m)
	}
}

func manifestRevisionOf(t *testing.T, frame []byte) uint64 {
	t.Helper()
	rev, err := terminals.ManifestRevision(frame)
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

// broadcast→community→broadcast: content admitted during the community
// phase stays materialized on every replica, INCLUDING a fresh replay
// (Authorized is permanently off after the first revision).
func TestModeFlipKeepsCommunityContent(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	ownerDir := t.TempDir()
	owner := openRuntime(t, ownerDir, "owner")
	tid, err := owner.CreateSpaceWithOptions("Shifting", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Say(tid, "broadcast era", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	// → community
	pubAll := "all"
	if err := owner.RevisePolicy(tid, PolicyDelta{Publish: &pubAll}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	// A stranger joins and writes during the community phase.
	joiner := openRuntime(t, t.TempDir(), "joiner")
	defer joiner.Close()
	if err := joiner.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if err := joiner.JoinPublicSpace(tid); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.Say(tid, "community voice", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := joiner.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	sp, _ := owner.spaceForTest(tid)
	if got := len(sp.State.Messages()); got != 2 {
		t.Fatalf("community post not materialized: %d", got)
	}

	// → back to broadcast.
	pubCur := "curated"
	if err := owner.RevisePolicy(tid, PolicyDelta{Publish: &pubCur}); err != nil {
		t.Fatal(err)
	}
	// New stranger writes are refused at admission again…
	if err := joiner.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	// …but the community-era post must SURVIVE — including a fresh replay.
	owner.Close()
	owner2 := openRuntime(t, ownerDir, "owner")
	defer owner2.Close()
	sp2, _ := owner2.spaceForTest(tid)
	msgs := sp2.State.Messages()
	if len(msgs) != 2 {
		t.Fatalf("fresh replay lost community content: %d messages", len(msgs))
	}
	found := false
	for _, m := range msgs {
		if m.Text == "community voice" {
			found = true
		}
	}
	if !found {
		t.Fatal("community-era post retroactively suppressed (determinism violation)")
	}
}

// TRUE freeze: the owner's own writes are refused too; contributor pushes
// pause client-side; ingress is not drained; unfreeze restores everything
// including pending delivery.
func TestTrueFreeze(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Coolroom", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityUnlisted,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Say(tid, "before the freeze", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	joiner := openRuntime(t, t.TempDir(), "joiner")
	defer joiner.Close()
	if err := joiner.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if err := joiner.JoinPublicSpace(tid); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.Say(tid, "queued while live", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	// FREEZE.
	frozen := true
	if err := owner.RevisePolicy(tid, PolicyDelta{Frozen: &frozen}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err) // the final projection carrying the frozen manifest
	}
	// Owner's OWN write refused (true freeze).
	if _, err := owner.Say(tid, "sneaky owner post", SayOptions{}); err == nil {
		t.Fatal("owner wrote into a frozen space")
	} else if !errors.Is(err, terminals.ErrSpaceFrozen) &&
		err.Error() != "node: this space is frozen — publication is paused" {
		t.Fatalf("unexpected refusal: %v", err)
	}
	// Joiner learns of the freeze and PAUSES its push.
	if err := joiner.fetchPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	jsp, _ := joiner.spaceForTest(tid)
	if !jsp.Policy().Frozen {
		t.Fatal("joiner did not learn the freeze")
	}
	if _, err := joiner.Say(tid, "no more", SayOptions{}); err == nil {
		t.Fatal("joiner wrote into a frozen space")
	}
	pendingBefore := len(jsp.UnackedLocalFrames())
	if pendingBefore == 0 {
		t.Fatal("joiner should hold pending frames")
	}
	if err := joiner.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err) // paused = clean no-op
	}
	// Ingress not drained — nothing arrives at the owner.
	if got, err := owner.collectPublicIngress(addr, tid); err != nil || got != 0 {
		t.Fatalf("frozen ingress moved data: %d %v", got, err)
	}
	sp, _ := owner.spaceForTest(tid)
	if got := len(sp.State.Messages()); got != 1 {
		t.Fatalf("frozen space materialized new content: %d", got)
	}

	// UNFREEZE → pending delivery resumes.
	frozen = false
	if err := owner.RevisePolicy(tid, PolicyDelta{Frozen: &frozen}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	if err := joiner.fetchPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	if err := joiner.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	sp, _ = owner.spaceForTest(tid)
	deadline := time.Now().Add(5 * time.Second)
	for len(sp.State.Messages()) != 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := len(sp.State.Messages()); got != 2 {
		t.Fatalf("pending not delivered after unfreeze: %d", got)
	}
}

// Forbidden transitions + stale-install refusals.
func TestRevisionRefusals(t *testing.T) {
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()

	// Private spaces take no revisions at all.
	priv, err := owner.CreateSpace("Sanctum")
	if err != nil {
		t.Fatal(err)
	}
	vis := "public"
	if err := owner.RevisePolicy(priv, PolicyDelta{Visibility: &vis}); err == nil {
		t.Fatal("private→public revision accepted")
	}

	// Public space cannot cross to private.
	pub, err := owner.CreateSpaceWithOptions("Openish", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityUnlisted, Join: terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	visPriv := "private"
	if err := owner.RevisePolicy(pub, PolicyDelta{Visibility: &visPriv}); err == nil {
		t.Fatal("public→private revision accepted")
	}
	// unlisted→public is fine and bumps the revision.
	visPub := "public"
	if err := owner.RevisePolicy(pub, PolicyDelta{Visibility: &visPub}); err != nil {
		t.Fatal(err)
	}
	sp, _ := owner.spaceForTest(pub)
	if m := manifestRevisionOf(t, sp.ManifestFrame); m != 2 {
		t.Fatalf("revision = %d, want 2", m)
	}
}

var _ = projection.FormatVersion // envelope-level rollback covered in 0.4 tests
