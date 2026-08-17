package node

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// PA-0.2: a public broadcast space is plaintext end-to-end — no epochs, no
// sealing — and survives a restart in the same mode. The VERIFIED MANIFEST,
// not the SpaceMeta cache, decides the mode (I1): a tampered cache cannot
// flip a space's cryptographic runtime in either direction.
func TestPublicSpaceCreateRestartAndMetaTamper(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "owner")

	tid, err := rt.CreateSpaceWithOptions("Open Field", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	if sp.Private {
		t.Fatal("public space must not be private")
	}
	if _, err := rt.Say(tid, "plaintext by design", SayOptions{}); err != nil {
		t.Fatalf("owner (attested writer) refused: %v", err)
	}
	msgs := sp.State.Messages()
	if len(msgs) != 1 {
		t.Fatalf("message not materialized: %d", len(msgs))
	}
	if rt.ks.Spaces[tid].Visibility != string(terminals.VisibilityPublic) {
		t.Fatalf("visibility cache wrong: %q", rt.ks.Spaces[tid].Visibility)
	}
	if len(rt.ks.Epochs[tid]) != 0 {
		t.Fatal("public space minted epoch keys")
	}

	// TAMPER the cache: claim the space is private. The verified manifest
	// must win on reopen and the cache must be repaired.
	rt.mu.Lock()
	meta := rt.ks.Spaces[tid]
	meta.Visibility = "private"
	rt.ks.Spaces[tid] = meta
	_ = rt.saveKeystore()
	rt.mu.Unlock()
	rt.Close()

	rt2 := openRuntime(t, dir, "owner")
	defer rt2.Close()
	sp2, ok := rt2.spaceForTest(tid)
	if !ok {
		t.Fatal("space lost across restart")
	}
	if sp2.Private {
		t.Fatal("tampered cache flipped a public space to private — manifest must win (I1)")
	}
	if got := rt2.ks.Spaces[tid].Visibility; got != string(terminals.VisibilityPublic) {
		t.Fatalf("cache not repaired from manifest: %q", got)
	}
	if len(sp2.State.Messages()) != 1 {
		t.Fatal("plaintext history lost across restart")
	}
	if _, err := rt2.Say(tid, "still writable after restart", SayOptions{}); err != nil {
		t.Fatalf("owner write after restart: %v", err)
	}
	// Every frame in the log is plaintext (PayloadCBOR).
	assertAllPlaintext(t, sp2)
}

// A private space stays private even if the cache claims otherwise: the
// tamper direction that would DISABLE encryption must also lose to the
// verified manifest.
func TestPrivateSpaceSurvivesVisibilityTamper(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "owner")
	tid, err := rt.CreateSpace("Sanctum")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "sealed", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	meta := rt.ks.Spaces[tid]
	meta.Visibility = "public" // hostile/corrupt cache
	rt.ks.Spaces[tid] = meta
	_ = rt.saveKeystore()
	rt.mu.Unlock()
	rt.Close()

	rt2 := openRuntime(t, dir, "owner")
	defer rt2.Close()
	sp, _ := rt2.spaceForTest(tid)
	if !sp.Private {
		t.Fatal("tampered cache disabled encryption on a private space (I1 violation)")
	}
	if got := rt2.ks.Spaces[tid].Visibility; got != string(terminals.VisibilityPrivate) {
		t.Fatalf("cache not repaired to private: %q", got)
	}
	if _, err := rt2.Say(tid, "still sealed", SayOptions{}); err != nil {
		t.Fatalf("private write after restart: %v", err)
	}
}

// Reader replicas never emit: no manifest auto-publish on open, and Say is
// refused with the friendly error (the authoritative gate is terminals').
func TestReaderRoleNeverEmits(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "owner")
	tid, err := rt.CreateSpaceWithOptions("Feed", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityUnlisted,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Flip this replica to a reader (simulates an OpenPublicSpace replica —
	// the mechanics under test are role gating, not the open flow).
	rt.mu.Lock()
	meta := rt.ks.Spaces[tid]
	meta.Role = storage.RoleReader
	rt.ks.Spaces[tid] = meta
	_ = rt.saveKeystore()
	rt.mu.Unlock()
	rt.Close()

	rt2 := openRuntime(t, dir, "owner")
	defer rt2.Close()
	sp, _ := rt2.spaceForTest(tid)
	before := sp.Log.Len()
	if _, err := rt2.Say(tid, "readers cannot talk", SayOptions{}); err == nil {
		t.Fatal("reader Say accepted")
	} else if !errors.Is(err, terminals.ErrReadOnlyReplica) &&
		err.Error() != "node: join this space to write" {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if sp.Log.Len() != before {
		t.Fatal("reader emit reached the log")
	}
}

// A non-writer member of a curated (broadcast) space gets the friendly
// refusal from the node layer before the low-level gates even see it.
func TestCuratedSayRefusedForNonWriter(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "reader-ish")
	// Craft a curated space owned by SOMEONE ELSE: the local principal is
	// not among the attested writers.
	other := terminals.WriterBinding{}
	pol := terminals.SpacePolicy{
		Visibility: terminals.VisibilityPublic,
		Publish:    terminals.PublishCurated,
		Writers:    []terminals.WriterBinding{other},
	}
	s, err := terminals.NewSpaceWithPolicy("Their Broadcast", rt.PrincipalID,
		terminals.DefaultCharacter("radio_room"), pol)
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.attach(s.ID, s)
	rt.mu.Unlock()
	if _, err := rt.Say(s.ID, "not my stage", SayOptions{}); err == nil {
		t.Fatal("non-writer Say accepted in curated space")
	}
	rt.Close()
}

// Plaintext honesty: every frame a public space emits is PayloadCBOR.
func TestPublicSpaceFramesArePlaintext(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "owner")
	defer rt.Close()
	tid, err := rt.CreateSpaceWithOptions("Glasshouse", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityUnlisted,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "visible to anyone with the id", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	assertAllPlaintext(t, sp)
}

func assertAllPlaintext(t *testing.T, sp *terminals.Space) {
	t.Helper()
	if err := sp.Log.Replay(func(a eventlog.Applied) error {
		if a.Env.PayloadEncoding != signal.PayloadCBOR {
			t.Fatalf("public space frame %s is not plaintext (%d)",
				a.Env.Schema, a.Env.PayloadEncoding)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
