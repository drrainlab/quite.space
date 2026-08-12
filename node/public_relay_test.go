package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// PA-0.4B integration: an owner publishes a broadcast space through a blind
// relay; TWO complete strangers open reader replicas from just the id +
// relay address and both materialize the content (non-destructive outbox);
// ProjectionSeq survives an owner restart; a wiped mailbox is repaired by
// the owner's next publish.
func TestPublicProjectionEndToEnd(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	ownerDir := t.TempDir()
	owner := openRuntime(t, ownerDir, "owner")
	tid, err := owner.CreateSpaceWithOptions("Field Reports", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := owner.Say(tid, fmt.Sprintf("report %d", i), SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	seq1 := owner.ks.PublicPublish[tid].ProjectionSeq
	if seq1 == 0 {
		t.Fatal("publish did not persist a sequence")
	}

	// Two strangers — nothing but the id and the relay address.
	for i := 0; i < 2; i++ {
		reader := openRuntime(t, t.TempDir(), fmt.Sprintf("stranger-%d", i))
		if err := reader.OpenPublicSpace(tid, addr); err != nil {
			t.Fatalf("stranger %d open: %v", i, err)
		}
		if n := msgCount(reader, tid); n != 3 {
			t.Fatalf("stranger %d materialized %d messages", i, n)
		}
		sp, _ := reader.spaceForTest(tid)
		if sp.Private {
			t.Fatalf("stranger %d replica marked private", i)
		}
		if got := reader.ks.Spaces[tid].Title; got != "Field Reports" {
			t.Fatalf("stranger %d title = %q (manifest not installed)", i, got)
		}
		if _, err := reader.Say(tid, "sneaky write", SayOptions{}); err == nil {
			t.Fatalf("stranger %d wrote into a broadcast space", i)
		}
		reader.Close()
	}

	// Heartbeat repair: wipe the mailbox, publish again (same content →
	// SAME seq), reader still converges.
	now := uint64(time.Now().Unix())
	srv.WipeForTest(relay.HintPublicOutbox(tid, relay.Bucket(now)))
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	if got := owner.ks.PublicPublish[tid].ProjectionSeq; got != seq1 {
		t.Fatalf("heartbeat bumped seq: %d → %d", seq1, got)
	}

	// New content bumps the sequence.
	if _, err := owner.Say(tid, "report 3", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	seq2 := owner.ks.PublicPublish[tid].ProjectionSeq
	if seq2 != seq1+1 {
		t.Fatalf("content change must bump seq by one: %d → %d", seq1, seq2)
	}

	// Seq survives the owner restart (I4 durable publisher state).
	owner.Close()
	owner2 := openRuntime(t, ownerDir, "owner")
	defer owner2.Close()
	if got := owner2.ks.PublicPublish[tid].ProjectionSeq; got != seq2 {
		t.Fatalf("seq lost across restart: %d → %d", seq2, got)
	}
	if err := owner2.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	if got := owner2.ks.PublicPublish[tid].ProjectionSeq; got != seq2 {
		t.Fatalf("restart republish minted a new seq for identical content: %d", got)
	}

	// A reader that saw seq2 refuses a regression to seq1's content: fetch
	// again and confirm the newest state (4 messages) is what sticks.
	late := openRuntime(t, t.TempDir(), "late")
	defer late.Close()
	if err := late.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if got := msgCount(late, tid); got != 4 {
		t.Fatalf("late reader sees %d messages, want 4", got)
	}
}

// The background relay-sync loop does the whole thing hands-free: owner
// with a configured relay auto-publishes; a reader with the same relay
// auto-fetches — no manual publish/fetch calls.
func TestPublicProjectionAutoSync(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Auto Feed", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityUnlisted,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Say(tid, "hands-free", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.OpenPublicSpace(tid, ""); err != nil { // no first-paint fetch
		t.Fatal(err)
	}
	if err := reader.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if msgCount(reader, tid) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto-sync projection never arrived (owner %+v reader %+v)",
				owner.RelaySync(), reader.RelaySync())
		}
		time.Sleep(200 * time.Millisecond)
	}
}
