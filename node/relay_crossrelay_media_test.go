// A PUBLIC SPACE'S MEDIA WHEN ITS RELAY IS NOT YOUR RELAY.
//
// A public space carries its own relay in signed policy. A reader's media
// want therefore travels to THAT relay, and a holder answers into a reply box
// on it — while the collect ran against the reader's PERSONAL relay, asking
// one address about every space it knows.
//
// While the whole world shared one relay those were the same address and
// nothing was wrong. The day a second official relay was added they came
// apart: the answer is written to one machine and waited for on another, both
// sides report healthy, and a picture never arrives from a mirror that
// demonstrably holds it.
package node

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestMediaArrivesWhenTheSpacesRelayIsNotTheReadersOwn(t *testing.T) {
	// Two relays: the space lives on one, the reader's personal inbox on the
	// other. Nothing else differs.
	spaceSrv, spacePort, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer spaceSrv.Close()
	spaceAddr := fmt.Sprintf("127.0.0.1:%d", spacePort)

	otherSrv, otherPort, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer otherSrv.Close()
	otherAddr := fmt.Sprintf("127.0.0.1:%d", otherPort)

	// The owner publishes on the space's relay.
	owner := openRuntime(t, t.TempDir(), "owner")
	defer owner.Close()
	if err := owner.SetSettings(Settings{Relay: spaceAddr}); err != nil {
		t.Fatal(err)
	}
	tid := openPublicSpaceForMirror(t, owner, "a space with a home of its own")
	content := randBytes(t, 200_000)
	ref := emitVisual(t, owner, tid, content, 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}
	if err := owner.publishPublicProjection(spaceAddr, tid); err != nil {
		t.Fatal(err)
	}

	// The reader's own relay is the OTHER one. It still opens the space by
	// its link, which names the space's relay — the ordinary way anybody
	// arrives at somebody else's public space.
	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.SetSettings(Settings{Relay: otherAddr}); err != nil {
		t.Fatal(err)
	}
	if err := reader.OpenPublicSpace(tid, spaceAddr); err != nil {
		t.Fatal(err)
	}
	aid := ref.PublicIDHex()
	waitUntil(t, 30*time.Second, "the reader never saw the post", func() bool {
		_ = reader.fetchPublicProjection(spaceAddr, tid)
		_, err := reader.AssetStatus(tid, aid)
		return err == nil
	})

	if err := reader.RequestAsset(tid, aid); err != nil {
		t.Fatal(err)
	}
	// NOTHING IS DRIVEN BY HAND HERE. Both nodes have their own relay
	// configured, so their background loops are running — and the loop is
	// what had the hole: it collected reply boxes from the personal relay
	// only. Waiting is the assertion.
	waitUntil(t, 90*time.Second, "the answer was written to the space's relay and looked for on the reader's own", func() bool {
		s, err := reader.AssetStatus(tid, aid)
		return err == nil && s.State == assets.StateComplete
	})

	got, _, err := reader.RetrieveAsset(tid, aid)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("the asset completed but the bytes are wrong: %v", err)
	}
}
