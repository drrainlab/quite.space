package node

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/projection"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// The PH wave in one test: the two sentences it exists for, asserted
// together because each is worthless without the other.
//
//	A public space survives its owner going offline.
//	A stranger holding the link cannot empty anyone's mailbox.
//
// Asserted together on purpose. A drain gate that also broke delivery would
// pass a negative-only test, and an availability mechanism that leaked
// mailboxes would pass a positive-only one.
func TestPublicHardeningEndToEnd(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// ---- the owner publishes a space with media -------------------------
	owner := openRuntime(t, t.TempDir(), "owner")
	tid := openPublicSpaceForMirror(t, owner, "Commons")
	for _, m := range []string{"first", "second", "third"} {
		if _, err := owner.Say(tid, m, SayOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	content := randBytes(t, 200_000)
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

	// ---- a volunteer takes custody --------------------------------------
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
	waitUntil(t, 25*time.Second, "mirror never installed the projection", func() bool {
		_ = mirror.fetchPublicProjection(addr, tid)
		return msgCount(mirror, tid) == 3
	})
	// Greedy custody: a mirror fetches media so someone ELSE can have it.
	waitUntil(t, 40*time.Second, "mirror never took custody of the media", func() bool {
		_ = mirror.mirrorKeepalive(addr, tid)
		_, _ = owner.collectPublicIngress(addr, tid)
		_, _ = mirror.PullFromRelay(addr)
		st, err := mirror.AssetStatus(tid, ref.PublicIDHex())
		return err == nil && st.State == "complete"
	})

	// ---- the owner leaves and the relay forgets the space ---------------
	owner.Close()
	b := relay.Bucket(uint64(time.Now().Unix()))
	srv.WipeForTest(relay.HintPublicOutbox(tid, b))

	if err := mirror.mirrorKeepalive(addr, tid); err != nil {
		t.Fatal(err)
	}

	// ---- a stranger who never met the owner reads everything ------------
	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()
	if err := reader.SetSettings(Settings{Relay: addr}); err != nil {
		t.Fatal(err)
	}
	if err := reader.OpenPublicSpace(tid, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 25*time.Second, "reader never saw the mirrored space", func() bool {
		_ = reader.fetchPublicProjection(addr, tid)
		return msgCount(reader, tid) == 3
	})

	// And the media arrives from the mirror, into a box only the reader can
	// drain. Assert the BYTES: a state of "complete" over the wrong content
	// would be the worst possible pass.
	if err := reader.RequestAsset(tid, ref.PublicIDHex()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 60*time.Second, "media never arrived from the mirror", func() bool {
		_ = reader.pushPublicIngress(addr, tid)
		_ = mirror.seedForSpace(addr, tid)
		_, _ = reader.PullFromRelay(addr)
		st, err := reader.AssetStatus(tid, ref.PublicIDHex())
		return err == nil && st.State == "complete"
	})
	got, _, err := reader.RetrieveAsset(tid, ref.PublicIDHex())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("the reader got %d bytes that are not the file", len(got))
	}

	// ---- a hostile node harvests everything public and drains nothing ----
	// It gathers device ids exactly as an attacker would: straight out of
	// the projection every reader can fetch.
	wire, env, err := mirror.loadProjection(tid)
	if err != nil {
		t.Fatal(err)
	}
	_ = wire
	devices := []id.DeviceID{env.PublisherDevice}
	for _, c := range env.CutPoints {
		devices = append(devices, c.Device)
	}

	hostile, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer hostile.Close()

	// Everything a stranger can derive or observe, tried at once: device
	// inboxes from public ids, ingress shards from the space id, and the
	// published ingress addresses padded to capability length — the
	// difference between OBSERVING an address and being able to DERIVE it.
	var guesses [][]byte
	for _, dev := range devices {
		guesses = append(guesses, relay.CapFor(tid, dev, b), relay.CapFor(tid, dev, b-1))
	}
	for _, bk := range []uint64{b, b + 1, b - 1} {
		for sh := byte(0); sh < relay.IngressShards; sh++ {
			guesses = append(guesses,
				relay.CapPublicIngressLegacy(tid, bk, sh),
				relay.CapPublicIngress([32]byte{}, bk, sh))
		}
	}
	for _, hint := range env.IngressHints {
		padded := append(append([]byte(nil), hint...), make([]byte, relay.CapLen-relay.HintLen)...)
		guesses = append(guesses, padded)
	}
	if items, err := hostile.Collect(guesses); err == nil && len(items) > 0 {
		t.Fatalf("a stranger with only public knowledge drained %d items", len(items))
	}

	// Delivery still works after all that: a drain gate that also broke
	// ordinary delivery would have passed every assertion above.
	if err := reader.pushPublicIngress(addr, tid); err != nil {
		t.Fatalf("the drain gate broke ordinary delivery: %v", err)
	}

	// ---- the mirror never became an author ------------------------------
	if env.PublisherDevice == mirror.Device.ID {
		t.Fatal("the mirror published as itself")
	}
	if err := projection.Verify(env); err != nil {
		t.Fatalf("what the mirror served no longer verifies: %v", err)
	}
}
