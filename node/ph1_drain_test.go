package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// PH-1's reason for existing, stated as a test.
//
// A public space's read capability IS its space id, and device ids travel in
// the clear inside the signed projection. Before this gate, both of those
// facts added up to "any reader can compute — and therefore EMPTY — any
// other reader's media inbox and every contributor's ingress shard". That is
// silent denial of delivery by anyone holding a link.
//
// The test asserts the negative and the positive TOGETHER, because a drain
// gate that also broke delivery would pass a negative-only test.
func TestStrangerCannotDrainAnothersMailbox(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	reader := openRuntime(t, t.TempDir(), "reader")
	defer reader.Close()

	tid := id.TerminalID{9, 9, 9}
	victim := reader.Device.ID // harvested from a projection in real life

	// The reader mints its reply box and publishes only the HINT, exactly as
	// pushPublicIngress does.
	reader.mu.Lock()
	cap := reader.replyBoxCapLocked(tid, relay.Bucket(1))
	reader.mu.Unlock()
	if cap == nil {
		t.Fatal("no reply capability minted")
	}
	box := relay.CollectHint(cap)

	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// A holder answers into the box, addressing it by hint alone.
	answer := bundle.EncodeWithBlobs(tid, nil, [][]byte{[]byte("the media bytes")})
	if _, err := client.Put(box, 0, answer); err != nil {
		t.Fatal(err)
	}

	// The stranger knows everything public: the space id, the victim's device
	// id, and the box address it just watched go past on the wire.
	stranger, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()

	// 1. The old derivation — space id + device id — opens nothing.
	got, err := stranger.Collect([][]byte{relay.CapFor(tid, victim, relay.Bucket(1))})
	if err == nil && len(got) > 0 {
		t.Fatal("a stranger drained an inbox derived from public ids")
	}
	// 2. Nor does the ingress shard derivation.
	if got, err := stranger.Collect([][]byte{
		relay.CapPublicIngressLegacy(tid, relay.Bucket(1), 0)}); err == nil && len(got) > 0 {
		t.Fatal("a stranger drained an ingress shard")
	}
	// 3. Nor does the box address itself, padded to capability length — the
	//    difference between OBSERVING a hint and DERIVING it.
	padded := append(append([]byte(nil), box...), make([]byte, relay.CapLen-relay.HintLen)...)
	if got, err := stranger.Collect([][]byte{padded}); err == nil && len(got) > 0 {
		t.Fatal("the box hint doubled as its own capability")
	}

	// And the answer is still waiting for the reader who asked for it.
	mine, err := client.Collect([][]byte{cap})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 {
		t.Fatalf("the media answer was lost: %d items", len(mine))
	}
	parts, err := bundle.DecodeParts(mine[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(parts.Blobs) != 1 || string(parts.Blobs[0]) != "the media bytes" {
		t.Fatalf("wrong payload: %+v", parts.Blobs)
	}
}

// A public request without a reply box gets no answer rather than an answer
// posted to a mailbox everyone can empty. Silence is the honest outcome.
func TestPublicWantWithoutAReplyBoxIsNotAnswered(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	holder := openRuntime(t, t.TempDir(), "holder")
	defer holder.Close()

	tid, err := holder.CreateSpace("Public")
	if err != nil {
		t.Fatal(err)
	}
	content := randBytes(t, 200_000) // large enough to take the manifest path
	ref := emitVisual(t, holder, tid, content, 4096)
	if ref.ManifestWireID == nil {
		t.Fatal("test needs the manifest path")
	}

	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	wanter := id.DeviceID{4, 2}
	want := [][]byte{ref.ManifestWireID[:]}

	// public=true, no reply box → nothing is posted anywhere.
	holder.answerWants(client, tid, wanter[:], want, nil, true)

	got, err := client.Collect([][]byte{relay.CapFor(tid, wanter, relay.Bucket(uint64(time.Now().Unix())))})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("a public answer landed in a device-derived inbox anyone could drain")
	}
}
