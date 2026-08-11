// The Space-free swarm core (PM-0): wants out, expected bytes in, and a
// holder answers each reply box once per cycle however many duplicate
// bundles carried the same wants.
package node

import (
	"bytes"
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/relay"
)

var errNotHeld = errors.New("not held")

// memSink is the session store's shape: three methods, no disk, no space.
type memSink struct{ m map[id.Hash][]byte }

func newMemSink() *memSink { return &memSink{m: map[id.Hash][]byte{}} }
func (s *memSink) PutBlob(data []byte) (id.Hash, error) {
	h := id.HashOf(data)
	s.m[h] = append([]byte(nil), data...)
	return h, nil
}
func (s *memSink) GetBlob(h id.Hash) ([]byte, error) {
	b, ok := s.m[h]
	if !ok {
		return nil, errNotHeld
	}
	return b, nil
}
func (s *memSink) HasBlob(h id.Hash) bool { _, ok := s.m[h]; return ok }

// A hostile holder fills the box with garbage beside the real answer:
// only the expected hash lands, and the garbage never touches the budget.
func TestSwarmCollectDropsUnsolicitedBlobs(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	srv, addr := setUpRelay(t, rt)
	defer srv.Close()

	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	cap, err := relay.NewReplyCap()
	if err != nil {
		t.Fatal(err)
	}
	hint := relay.CollectHint(cap)
	wanted := []byte("the real chunk bytes")
	wantedHash := id.HashOf(wanted)
	garbage := bytes.Repeat([]byte{0xEE}, 4096)

	var tid id.TerminalID
	body := bundle.EncodeWithBlobs(tid, nil, [][]byte{garbage, wanted, bytes.Repeat([]byte{0xDD}, 2048)})
	if _, err := client.Put(hint, uint64(1<<62), body); err != nil {
		t.Fatal(err)
	}

	sink := newMemSink()
	budgetSpent := 0
	stored := swarmCollect(client, cap,
		func(h id.Hash) bool { return h == wantedHash },
		func(n int) bool { budgetSpent += n; return true },
		sink)
	if stored != 1 {
		t.Fatalf("stored %d blobs, want exactly the expected one", stored)
	}
	if !sink.HasBlob(wantedHash) {
		t.Fatal("the expected blob did not land")
	}
	if budgetSpent != len(wanted) {
		t.Fatalf("garbage touched the budget: spent %d, want %d", budgetSpent, len(wanted))
	}
}

// Duplicate want bundles in one drain cycle earn ONE answer per reply
// box, with the wants deduped — not one full answer per item.
func TestDrainCycleAnswersEachBoxOnce(t *testing.T) {
	acc := newWantAccumulator()
	box := bytes.Repeat([]byte{0xAB}, relay.HintLen)
	w1, w2 := bytes.Repeat([]byte{1}, id.Size), bytes.Repeat([]byte{2}, id.Size)
	acc.add(nil, [][]byte{w1, w2}, box)
	acc.add(nil, [][]byte{w2, w1}, box) // a duplicate bundle
	acc.add(nil, [][]byte{w1}, box)     // and another
	if len(acc.byBox) != 1 {
		t.Fatalf("%d boxes for one reply hint", len(acc.byBox))
	}
	bw := acc.byBox[string(box)]
	if len(bw.wants) != 2 {
		t.Fatalf("wants not deduped: %d", len(bw.wants))
	}
	// A second box stays separate.
	acc.add(nil, [][]byte{w1}, bytes.Repeat([]byte{0xCD}, relay.HintLen))
	if len(acc.byBox) != 2 {
		t.Fatal("distinct boxes merged")
	}
}

// The whole loop on real machinery: a session-shaped wanter (random
// identity, no space) pushes wants against the owner's published ingress
// hints; the owner drains and answers; the wanter collects exactly its
// bytes into a memory sink.
func TestSwarmRoundTripWithoutASpace(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	srv, addr := setUpRelay(t, alice)
	defer srv.Close()

	src, err := alice.CreateSpaceWithOptions("public", CreateOptions{
		Policy: terminals.SpacePolicy{
			Visibility: terminals.VisibilityPublic,
			Publish:    terminals.PublishCurated,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Alice publishes a real asset into the space, and her projection so
	// the ingress hints exist.
	//
	// A REAL asset, not a bare PutBlob, and the difference is the point.
	// answerWants only answers for blobs the space's own asset graph
	// references (assetIdx.allowed) — otherwise a member of one space
	// could ask on its ingress for a hash they learned in another and
	// learn from the answer whether this node holds it. A blob dropped
	// straight into the node-global store belongs to no space, so it is
	// exactly what the gate refuses; the fixture used to be one, which
	// made this test assert the leak rather than the round trip.
	ref := emitVisual(t, alice, src, randBytes(t, 4096), 16384)
	wire := ref.WireIDs()
	if len(wire) == 0 {
		t.Fatal("the asset carries no wire ids to ask for")
	}
	blobHash := wire[0]
	blob, err := alice.root.GetBlob(blobHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.publishPublicProjection(addr, src); err != nil {
		t.Fatal(err)
	}
	// The wanter reads the hints off the wire like a preview would.
	client, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	env, _, err := bestProjectionFor(client, src)
	if err != nil {
		t.Fatal(err)
	}

	wanter, err := newSwarmIdentity()
	if err != nil {
		t.Fatal(err)
	}
	replyCap, err := relay.NewReplyCap()
	if err != nil {
		t.Fatal(err)
	}
	if err := swarmPushWants(client, src, env.IngressHints, wanter,
		[][]byte{blobHash[:]}, replyCap); err != nil {
		t.Fatal(err)
	}

	// The owner's ordinary drain answers the want.
	if _, err := alice.collectPublicIngress(addr, src); err != nil {
		t.Fatal(err)
	}

	sink := newMemSink()
	stored := swarmCollect(client, replyCap,
		func(h id.Hash) bool { return h == blobHash },
		func(int) bool { return true }, sink)
	if stored != 1 {
		t.Fatalf("the answer did not arrive: stored %d", stored)
	}
	got, err := sink.GetBlob(blobHash)
	if err != nil || !bytes.Equal(got, blob) {
		t.Fatal("the bytes changed in transit")
	}
}
