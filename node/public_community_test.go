package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// PA-0.4C cold start: reader and owner have NEVER met. The reader joins an
// open community, its self manifest + message travel through the public
// ingress, the owner discovers the signer, materializes the message, and
// the next projection carries it to a third stranger. All hands-free via
// the relay-sync loops.
func TestCommunityColdStartJoinAndPublish(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Commons", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Say(tid, "welcome, whoever you are", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	joinerDir := t.TempDir()
	joiner := openRuntime(t, joinerDir, "joiner")
	if err := joiner.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	// The first fetch may race the owner's first auto-publish — wait for it.
	waitUntil(t, 20*time.Second, "joiner never saw the projection", func() bool {
		_ = joiner.fetchPublicProjection(addr, tid)
		return msgCount(joiner, tid) >= 1
	})
	if err := joiner.JoinPublicSpace(tid); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.Say(tid, "hi from a stranger", SayOptions{}); err != nil {
		t.Fatalf("joined contributor refused: %v", err)
	}
	if err := joiner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// Owner materializes the contribution; a third stranger reads BOTH.
	waitUntil(t, 25*time.Second, "owner never materialized the contribution", func() bool {
		return msgCount(owner, tid) == 2
	})
	third := openRuntime(t, t.TempDir(), "third")
	defer third.Close()
	if err := third.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "third stranger never saw both messages", func() bool {
		_ = third.fetchPublicProjection(addr, tid)
		return msgCount(third, tid) == 2
	})

	// I8 restart survival: the joiner restarts; its pending set re-pushes;
	// the owner dedups — no duplicates materialize anywhere.
	joiner.Close()
	joiner2 := openRuntime(t, joinerDir, "joiner")
	defer joiner2.Close()
	sp2, ok := joiner2.spaceForTest(tid)
	if !ok {
		t.Fatal("joiner lost the space across restart")
	}
	if sp2.ReadOnly {
		t.Fatal("contributor became read-only after restart")
	}
	if got := len(sp2.UnackedLocalFrames()); got == 0 {
		t.Fatal("restart lost the pending set (projSeen must reset, log persists)")
	}
	if err := joiner2.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if got := msgCount(owner, tid); got != 2 {
		t.Fatalf("re-pushed pending duplicated content: %d messages", got)
	}
}

// PA-0.4C curator path: a broadcast space has no Join — but a curator
// opening the public link is recognized from the VERIFIED signed policy and
// auto-activated; an ordinary reader on the same path stays read-only.
func TestCuratorActivationByPublicLink(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	curator := openRuntime(t, t.TempDir(), "curator")
	defer curator.Close()
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Label Feed", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
			Writers: []terminals.WriterBinding{{
				Principal: curator.Principal.ID, Device: curator.Device.ID,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Say(tid, "first release", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	// Curator opens by link → auto-activated from the signed policy.
	if err := curator.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if got := curator.ks.Spaces[tid].Role; got != "" {
		t.Fatalf("curator not auto-activated: role %q", got)
	}
	if _, err := curator.Say(tid, "b-side by the curator", SayOptions{}); err != nil {
		t.Fatalf("activated curator refused: %v", err)
	}
	if err := curator.pushPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.collectPublicIngress(addr, tid); err != nil {
		t.Fatal(err)
	}
	if got := msgCount(owner, tid); got != 2 {
		t.Fatalf("curator post not materialized by owner: %d", got)
	}

	// Ordinary reader: same path, stays reader, cannot write or join.
	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if got := reader.ks.Spaces[tid].Role; got != "reader" {
		t.Fatalf("stranger auto-activated in a curated space: role %q", got)
	}
	if _, err := reader.Say(tid, "nope", SayOptions{}); err == nil {
		t.Fatal("reader wrote into a broadcast space")
	}
	if err := reader.JoinPublicSpace(tid); err == nil {
		t.Fatal("broadcast space accepted a self-serve join")
	}
}

// PA-0.4D custody: a contributor's media publication enters the canonical
// log immediately (chain preserved — later text is public at once), stays
// OUT of the projection until the owner holds the verified blob, then
// appears. Wants ride the ingress; the answer lands in the requester inbox.
func TestCommunityMediaCustody(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid, err := owner.CreateSpaceWithOptions("Gallery", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityUnlisted,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Say(tid, "post your art", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	artist := openRuntime(t, t.TempDir(), "artist")
	defer artist.Close()
	if err := artist.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "artist never saw the projection", func() bool {
		_ = artist.fetchPublicProjection(addr, tid)
		return msgCount(artist, tid) >= 1
	})
	if err := artist.JoinPublicSpace(tid); err != nil {
		t.Fatal(err)
	}
	// Media A then text B: the chain must stay contiguous end-to-end.
	content := randBytes(t, 60_000)
	ref := emitVisual(t, artist, tid, content, 4096)
	if _, err := artist.Say(tid, "hope you like it", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := artist.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}

	// Owner materializes BOTH events (canonical log, chain intact)...
	waitUntil(t, 25*time.Second, "owner never materialized the contribution", func() bool {
		return msgCount(owner, tid) == 2 // "post your art" + "hope you like it"
	})
	// ...and eventually takes custody of the blob (wants → artist answers).
	waitUntil(t, 30*time.Second, "owner never took custody of the blob", func() bool {
		st, err := owner.AssetStatus(tid, ref.PublicIDHex())
		return err == nil && st.State == assets.StateComplete
	})

	// A reader now sees the visual entry (custody satisfied → projected).
	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 25*time.Second, "reader never saw the media publication", func() bool {
		_ = reader.fetchPublicProjection(addr, tid)
		return withSpace(reader, tid, func(s *terminals.Space) bool {
			for _, e := range s.State.Entries() {
				if e.Kind == "visual" {
					return true
				}
			}
			return false
		})
	})
}

func waitUntil(t *testing.T, d time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
