package node

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/identity"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/bundle"
)

// ---- minting frames, in the shape kernel/eventlog's own tests use ----

type testAuthor struct {
	priv ed25519.PrivateKey
	dev  id.DeviceID
	prin id.PrincipalID
	term id.TerminalID
	clk  uint64
	// Set by newCertifiedAuthor only: the real root and device behind the
	// ids, plus honest chain threading.
	root   *identity.Principal
	device *identity.Device
	seq    uint64
	tip    id.EventID
}

func newTestAuthor(t *testing.T, term id.TerminalID, seed byte) *testAuthor {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	a := &testAuthor{priv: priv, term: term}
	copy(a.dev[:], priv.Public().(ed25519.PublicKey))
	a.prin[0] = seed
	return a
}

// frameAt mints a VALID, correctly signed frame at an arbitrary sequence. A
// sequence past the chain's next is how "the journal accepted it but only into
// memory" is produced on purpose.
func (a *testAuthor) frameAt(t *testing.T, text string, seq uint64) []byte {
	t.Helper()
	a.clk++
	msg := &schemas.TextMessage{Text: text}
	payload, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	env := &signal.Envelope{
		Terminal:        a.term,
		Principal:       a.prin,
		Device:          a.dev,
		Sequence:        seq,
		Schema:          schemas.MessageText,
		LogicalClock:    a.clk,
		ProducedBy:      signal.AuthorshipHuman,
		PayloadEncoding: signal.PayloadCBOR,
		Payload:         payload,
		Priority:        signal.PriorityMessage,
	}
	if seq > 1 {
		prev := id.EventID{byte(seq)} // a predecessor that has not arrived
		env.Previous = &prev
	}
	frame, err := env.Sign(a.priv)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

// newCertifiedAuthor is a peer with a REAL root and a REAL device, so its
// certificate verifies and its chain threads honestly — the shape decision C's
// tests need, where frameAt's dangling-predecessor fakery would fork.
func newCertifiedAuthor(t *testing.T, term id.TerminalID) *testAuthor {
	t.Helper()
	root, err := identity.NewPrincipal(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	device, err := identity.NewDevice(identity.NewRand())
	if err != nil {
		t.Fatal(err)
	}
	return &testAuthor{
		priv: ed25519.NewKeyFromSeed(device.Seed()),
		dev:  device.ID, prin: root.ID, term: term,
		root: root, device: device,
	}
}

// next threads the chain for real: correct sequence, correct Previous.
func (a *testAuthor) next(t *testing.T, schema string, payload []byte) []byte {
	t.Helper()
	a.seq++
	a.clk++
	env := &signal.Envelope{
		Terminal:        a.term,
		Principal:       a.prin,
		Device:          a.dev,
		Sequence:        a.seq,
		Schema:          schema,
		LogicalClock:    a.clk,
		ProducedBy:      signal.AuthorshipHuman,
		PayloadEncoding: signal.PayloadCBOR,
		Payload:         payload,
		Priority:        signal.PriorityMessage,
	}
	if a.seq > 1 {
		prev := a.tip
		env.Previous = &prev
	}
	frame, err := env.Sign(a.priv)
	if err != nil {
		t.Fatal(err)
	}
	a.tip = id.EventIDOf(frame)
	return frame
}

func (a *testAuthor) nextText(t *testing.T, text string) []byte {
	t.Helper()
	payload, err := (&schemas.TextMessage{Text: text}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return a.next(t, schemas.MessageText, payload)
}

// certFrame is the device's own certificate as a log frame — PLAINTEXT
// payload, exactly as sealForEmit now sends it. corrupt flips a byte so the
// root signature no longer verifies.
func (a *testAuthor) certFrame(t *testing.T, corrupt bool) []byte {
	t.Helper()
	enc, err := a.root.Certify(a.device, 1, 0).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if corrupt {
		enc[len(enc)-1] ^= 0xFF
	}
	return a.next(t, schemas.DeviceCertified, enc)
}

func bundleOf(tid id.TerminalID, frames ...[]byte) []byte {
	return bundle.Encode(tid, frames)
}

// reopenHold is the RESTART: a second hold over the same directory, sharing no
// memory with the first. The hold is plaintext by design (the same material the
// event log stores), so this needs the data dir and nothing else.
func reopenHold(t *testing.T, dir string) *storage.IngressHold {
	t.Helper()
	root, err := storage.Open(dir, []byte("test passphrase"))
	if err != nil {
		t.Fatalf("reopen root: %v", err)
	}
	h, err := root.OpenIngressHold(ingressHoldTarget)
	if err != nil {
		t.Fatalf("reopen hold: %v", err)
	}
	return h
}

// ---- the three crash boundaries ----

// PHASE 1 IS PER RESPONSE. Collect returned three items and the relay has
// forgotten all three; if custody were taken one item at a time, a process
// dying after the first was judged would take the other two with it — gone
// from the relay, never on our disk.
func TestACollectedBatchIsFullyHeldBeforeAnyFrameIsJudged(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("the batch")
	if err != nil {
		t.Fatal(err)
	}
	a := newTestAuthor(t, tid, 7)

	items := [][]byte{
		bundle.Encode(tid, [][]byte{a.frameAt(t, "one", 4)}),
		bundle.Encode(tid, [][]byte{a.frameAt(t, "two", 5)}),
		bundle.Encode(tid, [][]byte{a.frameAt(t, "three", 6)}),
	}
	held, err := rt.takeIngressCustody(items, storage.IngressRelay)
	if err != nil {
		t.Fatalf("take custody: %v", err)
	}
	if len(held) != 3 {
		t.Fatalf("held %d items of a 3-item response", len(held))
	}

	// THE CRASH: judge exactly the first item, then stop dead.
	if _, release := rt.applyHeldRelayItem(nil, held[0]); release {
		rt.releaseIngress(held[0].ID)
	}

	after, err := reopenHold(t, dir).List()
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	for i, want := range items[1:] {
		found := false
		for _, got := range after {
			if bytes.Equal(got.Raw, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("item %d of the response was lost by the crash: it is gone "+
				"from the relay and absent from the hold", i+2)
		}
	}
}

// ADMIT IS NOT DELETE. A frame the journal accepted only into its in-memory
// pending map is not durably owned by anybody, so custody stays ours — this is
// the case Ingest reports as success and a restart would otherwise lose.
func TestCrashBeforeJournalOwnershipKeepsTheHold(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("the predecessor that never came")
	if err != nil {
		t.Fatal(err)
	}
	a := newTestAuthor(t, tid, 9)
	future := a.frameAt(t, "ahead of its predecessor", 5)
	item := bundle.Encode(tid, [][]byte{future})

	held, err := rt.takeIngressCustody([][]byte{item}, storage.IngressRelay)
	if err != nil {
		t.Fatalf("take custody: %v", err)
	}
	_, release := rt.applyHeldRelayItem(nil, held[0])
	if release {
		t.Fatal("the hold was released for a frame the journal holds only in " +
			"memory — a restart would lose it")
	}

	// THE CRASH, and then the restart.
	after, err := reopenHold(t, dir).List()
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	if len(after) != 1 || !bytes.Equal(after[0].Raw, item) {
		t.Fatalf("held items after restart = %d; the bytes must still be ours", len(after))
	}
	sp, ok := rt.spaceForTest(tid)
	if !ok {
		t.Fatal("lost the space")
	}
	if sp.Log.Has(id.EventIDOf(future)) {
		t.Fatal("the log claims durable ownership of a frame it only buffered")
	}
}

// THE OTHER SIDE OF THE SAME BOUNDARY, and the proof that no transaction
// between the two stores is needed: a crash after the journal owns the frame
// but before the hold is released replays the item, and EventID dedup makes
// that harmless.
func TestCrashAfterEventLogAcceptBeforeHoldDeleteIsHarmless(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("already durable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(tid, "the journal owns this", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	sp, ok := rt.spaceForTest(tid)
	if !ok {
		t.Fatal("lost the space")
	}
	mine := sp.Log.FramesInRange(rt.Device.ID, 1, 1000)
	if len(mine) == 0 {
		t.Fatal("nothing was written to this device's chain")
	}
	frame := mine[len(mine)-1]
	before := sp.Log.Len()

	// THE CRASH: the frame is durably in the log AND still in the hold,
	// because the process died between the append and the delete.
	hold, err := rt.ingressHold()
	if err != nil {
		t.Fatal(err)
	}
	item := bundle.Encode(tid, [][]byte{frame})
	hid, err := hold.Put(item, storage.HeldIngressMeta{ReceivedAt: 1, Source: storage.IngressRelay})
	if err != nil {
		t.Fatal(err)
	}

	// THE RESTART: the held item is offered again.
	_, release := rt.applyHeldRelayItem(nil, storage.HeldIngress{ID: hid, Raw: item})
	if !release {
		t.Fatal("a frame the journal already owns must release the hold")
	}
	rt.releaseIngress(hid)

	if after := sp.Log.Len(); after != before {
		t.Fatalf("the replay duplicated the event: log length %d → %d", before, after)
	}
	left, err := reopenHold(t, dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("the hold kept %d item(s) the journal owns", len(left))
	}
}
