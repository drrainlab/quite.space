package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func openPublicSpaceForMirror(t *testing.T, owner *Runtime, title string) id.TerminalID {
	t.Helper()
	tid, err := owner.CreateSpaceWithOptions(title, CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Join:       terminals.JoinOpen,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tid
}

// The whole reason PH-3 exists: a public space outlives its owner going
// offline. Previously the projection sat at the relay for 48 hours, kept
// warm by the owner's heartbeat, and vanished when the heartbeat stopped —
// so a space with an offline owner ceased to exist for anyone who had not
// already read it.
func TestMirrorKeepsASpaceReadableAfterTheOwnerLeaves(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	tid := openPublicSpaceForMirror(t, owner, "Fieldnotes")
	for _, m := range []string{"first", "second", "third"} {
		if _, err := owner.Say(tid, m, SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	// A volunteer opens the space and offers to keep it alive.
	mirror := openRuntime(t, t.TempDir(), "mirror")
	defer mirror.Close()
	if err := mirror.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := mirror.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if err := mirror.SetMirror(tid, true); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "mirror never installed the projection", func() bool {
		_ = mirror.fetchPublicProjection(addr, tid)
		return msgCount(mirror, tid) == 3
	})
	if err := mirror.mirrorKeepalive(addr, tid); err != nil {
		t.Fatal(err)
	}

	// The owner goes away, and the relay forgets the space — which is what
	// a 48-hour expiry looks like without waiting 48 hours.
	owner.Close()
	b := relay.Bucket(uint64(time.Now().Unix()))
	srv.WipeForTest(relay.HintPublicOutbox(tid, b))

	// Nothing is there now: this is the state the wave set out to fix.
	stranded := openRuntime(t, t.TempDir(), "stranded")
	defer stranded.Close()
	if err := stranded.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := stranded.OpenPublicSpace(tid, addr); err == nil && msgCount(stranded, tid) > 0 {
		t.Fatal("the wipe did not take effect; the test proves nothing")
	}

	// The mirror republishes the owner's own signed bytes.
	if err := mirror.mirrorKeepalive(addr, tid); err != nil {
		t.Fatal(err)
	}

	// And a stranger who never met the owner reads the space in full.
	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := reader.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "reader never saw the mirrored space", func() bool {
		_ = reader.fetchPublicProjection(addr, tid)
		return msgCount(reader, tid) == 3
	})

	// The mirror never claims authorship: the publisher on record is still
	// the owner's device.
	reader.mu.Lock()
	pub := reader.ks.PublicPublish[tid].PublisherDevice
	reader.mu.Unlock()
	if pub == mirror.Device.ID {
		t.Fatal("the mirror published as itself")
	}
}

// A mirror holding an older envelope must never shadow a fresher one. This
// is why keepalive uses Put and the owner uses Replace.
func TestStaleMirrorCannotShadowTheOwner(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid := openPublicSpaceForMirror(t, owner, "Moving Target")
	if _, err := owner.Say(tid, "one", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	mirror := openRuntime(t, t.TempDir(), "mirror")
	defer mirror.Close()
	if err := mirror.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := mirror.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if err := mirror.SetMirror(tid, true); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "mirror never installed the first projection", func() bool {
		_ = mirror.fetchPublicProjection(addr, tid)
		return msgCount(mirror, tid) == 1
	})

	// The owner moves on; the mirror still holds seq N-1.
	if _, err := owner.Say(tid, "two", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}
	if err := mirror.mirrorKeepalive(addr, tid); err != nil {
		t.Fatal(err)
	}

	// A fresh reader must see the NEWER space, not the mirror's stale copy.
	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := reader.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "reader never converged on the newer projection", func() bool {
		_ = reader.fetchPublicProjection(addr, tid)
		return msgCount(reader, tid) == 2
	})
}

// A mirror cannot write, cannot re-sign, and cannot drain the owner's
// ingress. These are the promises that make the role safe to offer.
func TestMirrorHasNoAuthority(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid := openPublicSpaceForMirror(t, owner, "Read Only To You")
	if _, err := owner.Say(tid, "mine", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	mirror := openRuntime(t, t.TempDir(), "mirror")
	defer mirror.Close()
	if err := mirror.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := mirror.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if err := mirror.SetMirror(tid, true); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "mirror never installed the projection", func() bool {
		_ = mirror.fetchPublicProjection(addr, tid)
		return msgCount(mirror, tid) == 1
	})

	// 1. It cannot sign a projection: no space key, refused structurally.
	sp, ok := mirror.spaceForTest(tid)
	if !ok {
		t.Fatal("mirror lost the space")
	}
	if _, _, err := sp.BuildPublicProjection(99, mirror.Device.ID,
		uint64(time.Now().Unix()), terminals.DefaultProjectionLimits()); err == nil {
		t.Fatal("a mirror signed a projection")
	}
	// 2. It has no ingress root, so it cannot derive the owner's drain
	//    capability even though it knows the addresses.
	if _, ok := sp.IngressRoot(); ok {
		t.Fatal("a mirror derived the owner's ingress root")
	}
	// 3. What it republishes is byte-identical to what the owner signed.
	mirror.mu.Lock()
	wire := append([]byte(nil), mirror.spaces[tid].projWire...)
	mirror.mu.Unlock()
	env, err := projection.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Verify(env); err != nil {
		t.Fatalf("the retained envelope no longer verifies: %v", err)
	}
	if env.PublisherDevice != owner.Device.ID {
		t.Fatal("the retained envelope names someone other than the owner")
	}
}

// Mirroring one's own space is refused: an owner IS the origin, and calling
// that mirroring would blur who is answerable for the space.
func TestOwnCannotBeMirrored(t *testing.T) {
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid := openPublicSpaceForMirror(t, owner, "Mine")
	if err := owner.SetMirror(tid, true); err == nil {
		t.Fatal("an owner was allowed to mirror its own space")
	}
}

// The mirror and seed flags survive a restart — a volunteer should not have
// to re-volunteer every time the process starts.
func TestMirrorFlagsSurviveRestart(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	tid := openPublicSpaceForMirror(t, owner, "Persisted")
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	m1 := openRuntime(t, dir, "mirror")
	if err := m1.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := m1.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	if err := m1.SetMirror(tid, true); err != nil {
		t.Fatal(err)
	}
	if err := m1.SetSeed(tid, true); err != nil {
		t.Fatal(err)
	}
	m1.Close()

	m2 := openRuntime(t, dir, "mirror")
	defer m2.Close()
	m2.mu.Lock()
	meta := m2.ks.Spaces[tid]
	m2.mu.Unlock()
	if !meta.Mirror || !meta.Seed {
		t.Fatalf("volunteering did not survive a restart: mirror=%v seed=%v",
			meta.Mirror, meta.Seed)
	}
}

// Seeding answers only with what is ALREADY held, and never fetches in
// order to be able to answer. That distinction is the whole difference
// between volunteering bandwidth and volunteering storage.
func TestSeedingAnswersOnlyWhatItHolds(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	seeder := openRuntime(t, t.TempDir(), "seeder")
	defer seeder.Close()
	tid := openPublicSpaceForMirror(t, seeder, "Swarm")
	held := emitVisual(t, seeder, tid, randBytes(t, 200_000), 4096)
	if held.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}

	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	box, err := relay.NewReplyCap()
	if err != nil {
		t.Fatal(err)
	}
	hint := relay.CollectHint(box)

	// Asked for something it holds: answered.
	seeder.answerWants(client, tid, nil, [][]byte{held.ManifestWireID[:]}, hint, true)
	got, err := client.Collect([][]byte{box})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("a seeder withheld a blob it holds")
	}

	// Asked for something it does not hold: silent, and above all it does
	// not go and fetch it in order to become able to answer.
	absent := make([]byte, 32)
	absent[0] = 0xAB
	seeder.answerWants(client, tid, nil, [][]byte{absent}, hint, true)
	got2, err := client.Collect([][]byte{box})
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Fatal("a seeder answered for a blob it does not hold")
	}
}
