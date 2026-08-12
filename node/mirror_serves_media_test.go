// THE WHOLE POINT OF A SEEDING MIRROR, end to end: the owner is off, and a
// stranger still gets the pictures.
//
// Everything around this was already tested in pieces — the mirror
// republishes the envelope, answerWants hands over only what it holds, a
// stranger cannot drain a mailbox. What had no test is the ONE PATH a person
// actually walks: open a public space whose owner is offline, tap a picture,
// and wait for a mirror to answer. Reported from a phone against the demo
// catalog, with the owner's machine switched off on purpose.
package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func TestAReaderGetsMediaFromAMirrorWhileTheOwnerIsOff(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// The owner publishes a public space with a picture in it.
	owner := openRuntime(t, t.TempDir(), "owner")
	tid := openPublicSpaceForMirror(t, owner, "somewhere, slowly")
	content := randBytes(t, 300_000)
	ref := emitVisual(t, owner, tid, content, 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}
	if err := owner.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := owner.publishPublicProjection(addr, tid); err != nil {
		t.Fatal(err)
	}

	// A mirror volunteers, and takes custody of the media.
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
	if err := mirror.SetSeed(tid, true); err != nil {
		t.Fatal(err)
	}
	aid := ref.PublicIDHex()
	waitUntil(t, 40*time.Second, "the mirror never took custody of the media", func() bool {
		_ = mirror.fetchPublicProjection(addr, tid)
		_ = mirror.mirrorKeepalive(addr, tid)
		s, err := mirror.AssetStatus(tid, aid)
		return err == nil && s.State == assets.StateComplete
	})

	// THE OWNER GOES OFF. From here the mirror is the only thing in the world
	// that has these bytes.
	owner.Close()

	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := reader.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "the reader never saw the post", func() bool {
		_ = reader.fetchPublicProjection(addr, tid)
		_, err := reader.AssetStatus(tid, aid)
		return err == nil
	})

	if err := reader.RequestAsset(tid, aid); err != nil {
		t.Fatal(err)
	}
	// Both sides are driven by hand rather than by their loops, so a failure
	// names the step that failed instead of "it timed out".
	waitUntil(t, 60*time.Second, "the mirror holds the media, seeds, and the reader still cannot get it", func() bool {
		_ = reader.pushPublicIngress(addr, tid) // the ask, with its reply box
		_ = mirror.seedForSpace(addr, tid)      // the answer
		_, _ = reader.PullFromRelay(addr)       // and the collection
		s, err := reader.AssetStatus(tid, aid)
		return err == nil && s.State == assets.StateComplete
	})

	got, _, err := reader.RetrieveAsset(tid, aid)
	if err != nil {
		t.Fatalf("the asset completed but will not read back: %v", err)
	}
	if len(got) != len(content) {
		t.Fatalf("got %d bytes, want %d", len(got), len(content))
	}
}
