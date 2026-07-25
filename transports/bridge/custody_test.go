package bridge

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/loopback"
	"github.com/drrainlab/quiet_places/transports/relay"
)

// readReceipts pulls every custody receipt off a carrier end.
func readReceipts(t *testing.T, ep *loopback.End) []*CustodyReceipt {
	t.Helper()
	var out []*CustodyReceipt
	reasm := kernelsync.NewReassembler()
	for _, pkt := range ep.Poll() {
		raw, err := reasm.Feed(pkt)
		if err != nil || raw == nil {
			continue
		}
		receipt, ok := kernelsync.ExtractCustodyReceipt(raw)
		if !ok {
			continue
		}
		rec, err := DecodeReceipt(receipt)
		if err != nil {
			t.Fatalf("bridge emitted an unverifiable receipt: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

// The ACK that TN-B designed and never sent. A node hands frames to a
// gateway over the carrier and hears back, in writing, that they are held.
func TestCustodyAckReachesTheSender(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xA1
	pair := loopback.NewPair(loopback.Faults{Seed: 11})
	b := testBridge(t, pair.B, "127.0.0.1:1", []Subscription{serving(dest, 0x71, 0x72)})
	now := time.Now()

	f := mkFrame(t, dest, 0x71, 1, nil, "did you get this")
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	if got := b.PumpRadio(now); got != 1 {
		t.Fatalf("radio intake: %d", got)
	}
	if sent := b.PushAcks(now); sent != 1 {
		t.Fatalf("acks sent: %d", sent)
	}
	got := readReceipts(t, pair.A)
	if len(got) != 1 {
		t.Fatalf("sender heard %d receipts, wanted 1", len(got))
	}
	if len(got[0].FrameIDs) != 1 || got[0].FrameIDs[0] != id.EventIDOf(f) {
		t.Fatalf("receipt covers the wrong frames: %+v", got[0].FrameIDs)
	}
	if got[0].Kind.Lapsed() {
		t.Fatal("a fresh custody claim must not be marked lapsed")
	}
	if !bytes.Equal(got[0].PublicKey, b.CustodianPub()) {
		t.Fatal("receipt signed by something other than the custodian key")
	}

	// A repeat of the same frame — the sender never heard the first ACK —
	// is answered again, with the SAME acceptance time. A fresh timestamp
	// would be a new promise for an old frame, and a silent drop would
	// leave a lost ACK unrepairable forever.
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	b.PumpRadio(now.Add(time.Minute))
	if sent := b.PushAcks(now.Add(time.Minute)); sent != 1 {
		t.Fatalf("a repeat was not re-acknowledged: %d", sent)
	}
	again := readReceipts(t, pair.A)
	if len(again) != 1 {
		t.Fatalf("repeat produced %d receipts", len(again))
	}
	if again[0].AcceptedAt != got[0].AcceptedAt {
		t.Fatalf("re-ACK invented a new acceptance time: %d then %d",
			got[0].AcceptedAt, again[0].AcceptedAt)
	}
	if b.QueueLen() != 1 {
		t.Fatalf("a repeat created new custody: %d records", b.QueueLen())
	}
}

// Custody is refused rather than promised when the store is full of
// promises, and the sender is not told anything reassuring.
func TestNoAckWithoutRoom(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xA2
	pair := loopback.NewPair(loopback.Faults{Seed: 12})
	b, err := New(Config{
		DataDir: t.TempDir(), Instance: "tight",
		Radio: pair.B, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: "127.0.0.1:1", RelayDomain: "relay:none",
		Subscriptions: []Subscription{serving(dest, 0x73, 0x74)},
		AirtimePerMin: 1e9,
		QueueCaps: routing.QueueCaps{
			MaxTotalBytes: 700, MaxPerDestBytes: 1 << 20, OperatorTTL: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	now := time.Now()

	accepted, refused := 0, 0
	for seq := uint64(1); seq <= 6; seq++ {
		f := mkFrame(t, dest, 0x73, seq, prevOf(t, dest, 0x73, seq),
			fmt.Sprintf("frame %d with enough text to take up real room", seq))
		if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
			t.Fatal(err)
		}
		b.PumpRadio(now)
		n := b.PushAcks(now)
		for _, r := range readReceipts(t, pair.A) {
			accepted += len(r.FrameIDs)
		}
		if n == 0 {
			refused++
		}
	}
	if accepted == 0 {
		t.Fatal("nothing was ever accepted: the test proves nothing about refusal")
	}
	if refused == 0 {
		t.Fatal("a full store kept promising: nothing was ever refused")
	}
	if s := b.Stats(); s.NoRoom == 0 {
		t.Fatal("refusals were not attributed to a full store")
	}
	// Everything that WAS promised is still held. That is the whole point:
	// the bridge would rather say no than say yes and quietly drop.
	if b.QueueLen() != accepted {
		t.Fatalf("promised %d frames, still holding %d", accepted, b.QueueLen())
	}
}

// When custody genuinely ends, the sender is told. A gateway that simply
// went quiet would leave a node believing its message was still travelling.
func TestExpiredCustodyIsWithdrawnNotForgotten(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xA3
	pair := loopback.NewPair(loopback.Faults{Seed: 13})
	b, err := New(Config{
		DataDir: t.TempDir(), Instance: "short-ttl",
		Radio: pair.B, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: "127.0.0.1:1", RelayDomain: "relay:none",
		Subscriptions: []Subscription{serving(dest, 0x75, 0x76)},
		AirtimePerMin: 1e9,
		QueueCaps: routing.QueueCaps{
			MaxTotalBytes: 1 << 20, MaxPerDestBytes: 1 << 20, OperatorTTL: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	now := time.Now()

	f := mkFrame(t, dest, 0x75, 1, nil, "hold this for me")
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	b.PumpRadio(now)
	b.PushAcks(now)
	if got := readReceipts(t, pair.A); len(got) != 1 || got[0].Kind.Lapsed() {
		t.Fatalf("no custody claim to withdraw later: %+v", got)
	}

	// Time passes; the operator TTL runs out.
	later := now.Add(2 * time.Minute)
	if dropped := b.Sweep(later); dropped != 1 {
		t.Fatalf("sweep dropped %d", dropped)
	}
	if sent := b.PushAcks(later); sent != 1 {
		t.Fatalf("expiry produced %d messages: custody ended in silence", sent)
	}
	got := readReceipts(t, pair.A)
	if len(got) != 1 {
		t.Fatalf("withdrawal not sent: %d receipts", len(got))
	}
	if !got[0].Kind.Lapsed() {
		t.Fatal("the withdrawal is indistinguishable from a custody claim")
	}
	if got[0].FrameIDs[0] != id.EventIDOf(f) {
		t.Fatal("withdrawal names the wrong frame")
	}
	if s := b.Stats(); s.CustodyLapsed != 1 {
		t.Fatalf("lapse not counted: %+v", s)
	}
}

// A frame nobody could put on the air is released, not hoarded. Holding it
// would mean it sat in custody until it expired with no one told why.
func TestUnairableFrameIsReleased(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xA4
	pair := loopback.NewPair(loopback.Faults{Seed: 14})
	b := testBridge(t, pair.B, "127.0.0.1:1", []Subscription{serving(dest, 0x77, 0x78)})
	now := time.Now()

	// Under the decode cap — a parser would happily read it — but far over
	// what is polite to broadcast at 2000 bytes a minute.
	big := mkFrame(t, dest, 0x78, 1, nil, string(make([]byte, 4000)))
	if len(big) > routing.RadioDecodeCap {
		t.Skipf("test frame %d exceeds the decode cap", len(big))
	}
	if len(big) <= routing.BetaOutboundCap {
		t.Fatalf("test frame %d is not actually oversized for the air", len(big))
	}
	// Arrives from the relay side, so the radio is its only way out.
	if !b.takeCustody(big, "relay:127.0.0.1:1", "relay:none", now) {
		t.Fatal("custody refused for a frame within the decode cap")
	}
	if sent := b.PushRadio(now); sent != 0 {
		t.Fatalf("an oversized frame was broadcast anyway: %d", sent)
	}
	if b.QueueLen() != 0 {
		t.Fatal("an unairable frame is being hoarded until it expires")
	}
	if s := b.Stats(); s.Unairable != 1 {
		t.Fatalf("release not attributed: %+v", s)
	}
}

// One undeliverable destination must not stall the others. Before the
// batch drain, a single stuck record could stop an entire pass.
func TestOneBadDestinationDoesNotStallTheRest(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	relayAddr := fmt.Sprintf("127.0.0.1:%d", port)

	var good, bad id.TerminalID
	good[0], bad[0] = 0xB1, 0xB2
	pair := loopback.NewPair(loopback.Faults{Seed: 15})
	// The second subscription has a radio mailbox but NO internet mailbox:
	// nothing the bridge takes for it can ever be delivered.
	b := testBridge(t, pair.B, relayAddr, []Subscription{
		serving(good, 0x79, 0x7A),
		{NetworkID: "test-mesh", Terminal: bad, RadioDevices: []id.DeviceID{deviceOf(0x7B)}},
	})
	now := time.Now()

	for _, tid := range []id.TerminalID{bad, good} {
		f := mkFrame(t, tid, 0x79, 1, nil, "please forward me")
		if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(tid, [][]byte{f})); err != nil {
			t.Fatal(err)
		}
		b.PumpRadio(now)
	}
	pushed, err := b.PushRelay(now)
	if err != nil {
		t.Fatal(err)
	}
	if pushed != 1 {
		t.Fatalf("the deliverable destination was stalled by the undeliverable "+
			"one: pushed %d", pushed)
	}
	if s := b.Stats(); s.NoMailbox == 0 {
		t.Fatal("the undeliverable destination was not reported as such")
	}
}

// Blindness, asserted against the artifacts the bridge WRITES rather than
// against the frames it carries. Custody is byte-exact by design — the
// frame must come back out exactly as it went in — so the meaningful
// question is whether anything the bridge derives for itself contains
// payload content. Its ledgers, snapshots and diagnostics must not.
func TestBridgeDerivesNothingFromPayload(t *testing.T) {
	const marker = "MERIDIAN-QUARTZ-77"
	dir := t.TempDir()
	var dest id.TerminalID
	dest[0] = 0xC5
	pair := loopback.NewPair(loopback.Faults{Seed: 16})
	b, err := New(Config{
		DataDir: dir, Instance: "blind",
		Radio: pair.B, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: "127.0.0.1:1", RelayDomain: "relay:none",
		Subscriptions: []Subscription{serving(dest, 0x7C, 0x7D)},
		AirtimePerMin: 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := mkFrame(t, dest, 0x7C, 1, nil, "the passphrase is "+marker)
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	b.PumpRadio(now)
	b.PushAcks(now)
	b.WakeRadio(now)
	diagnostics := fmt.Sprintf("%v %+v", b.String(), b.Stats())
	b.Close()

	if bytes.Contains([]byte(diagnostics), []byte(marker)) {
		t.Fatal("payload content leaked into diagnostics")
	}
	custodySeg := filepath.Join(dir, "queue", "custody.seg")
	var carried bool
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if path == custodySeg {
			// Custody holds the frame verbatim — that IS the job. Assert
			// the opposite here: a bridge that "cleaned up" what it stored
			// would hand back something the author never signed.
			carried = bytes.Contains(data, []byte(marker))
			return nil
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Fatalf("payload content leaked into %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !carried {
		t.Fatal("custody did not store the frame verbatim")
	}
}

// prevOf builds the previous-event link for a chain, so multi-frame test
// chains stay valid envelopes.
func prevOf(t *testing.T, term id.TerminalID, seed byte, seq uint64) *id.EventID {
	t.Helper()
	if seq <= 1 {
		return nil
	}
	var prev *id.EventID
	for s := uint64(1); s < seq; s++ {
		f := mkFrame(t, term, seed, s, prev, fmt.Sprintf("frame %d with enough text to take up real room", s))
		e := id.EventIDOf(f)
		prev = &e
	}
	return prev
}

// reopen simulates a power cut: no Close, just drop the handle and open the
// same data directory again. Anything the bridge only knew in memory is
// gone, exactly as it would be on a Pi that lost power.
func reopen(t *testing.T, b *Bridge, radio *loopback.End) *Bridge {
	t.Helper()
	cfg := b.cfg
	cfg.Radio = radio
	b.queue.Close()
	nb, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nb.Close() })
	return nb
}

// Crash window one: the frame is fsynced into custody and the power goes
// out BEFORE the ACK is sent. The sender, having heard nothing, retransmits.
// The restarted bridge must answer for the custody it already has, with the
// acceptance time it already recorded — not take the frame again, and not
// invent a fresh promise for an old frame.
func TestCrashBeforeAckAnswersWithOriginalAcceptance(t *testing.T) {
	dir := t.TempDir()
	var dest id.TerminalID
	dest[0] = 0xE1
	pair := loopback.NewPair(loopback.Faults{Seed: 21})
	b, err := New(Config{
		DataDir: dir, Instance: "crash-1",
		Radio: pair.B, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: "127.0.0.1:1", RelayDomain: "relay:none",
		Subscriptions: []Subscription{serving(dest, 0x81, 0x82)},
		AirtimePerMin: 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	f := mkFrame(t, dest, 0x81, 1, nil, "taken but never confirmed")
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	if got := b.PumpRadio(now); got != 1 {
		t.Fatalf("radio intake: %d", got)
	}
	rec, held := b.queue.Held(id.EventIDOf(f))
	if !held {
		t.Fatal("frame not in custody before the crash")
	}
	acceptedAt := rec.EnqueuedAt
	// Power cut here: PushAcks never ran.
	pair.A.Poll()
	b2 := reopen(t, b, pair.B)

	// The sender heard nothing and retransmits.
	later := now.Add(3 * time.Minute)
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	b2.PumpRadio(later)
	if sent := b2.PushAcks(later); sent != 1 {
		t.Fatalf("a restarted bridge did not answer for custody it holds: %d", sent)
	}
	got := readReceipts(t, pair.A)
	if len(got) != 1 {
		t.Fatalf("receipts after restart: %d", len(got))
	}
	if got[0].AcceptedAt != uint64(acceptedAt) {
		t.Fatalf("restart invented a new acceptance time: %d, custody says %d",
			got[0].AcceptedAt, acceptedAt)
	}
	if b2.QueueLen() != 1 {
		t.Fatalf("the retransmission created a second custody record: %d", b2.QueueLen())
	}
}

// Crash window two: the ACK goes out and the power fails before the send is
// recorded. On restart the bridge sends it again — this is at-least-once by
// choice. Recording the send first would turn a crash into a promise the
// sender never hears, which is the failure that cannot be repaired; a
// duplicate receipt is one the node collapses on its own.
func TestCrashAfterAckResendsIdempotently(t *testing.T) {
	dir := t.TempDir()
	var dest id.TerminalID
	dest[0] = 0xE2
	pair := loopback.NewPair(loopback.Faults{Seed: 22})
	b, err := New(Config{
		DataDir: dir, Instance: "crash-2",
		Radio: pair.B, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: "127.0.0.1:1", RelayDomain: "relay:none",
		Subscriptions: []Subscription{serving(dest, 0x83, 0x84)},
		AirtimePerMin: 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	f := mkFrame(t, dest, 0x83, 1, nil, "confirmed into a blackout")
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	b.PumpRadio(now)
	// PumpRadio persisted the obligation; the crash lands before the send is
	// recorded, so restore the on-disk state to that moment.
	snapshot, err := os.ReadFile(b.acksPath())
	if err != nil {
		t.Fatal(err)
	}
	if sent := b.PushAcks(now); sent != 1 {
		t.Fatalf("first ack: %d", sent)
	}
	first := readReceipts(t, pair.A)
	if len(first) != 1 {
		t.Fatalf("first receipt count: %d", len(first))
	}
	if err := os.WriteFile(b.acksPath(), snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	b2 := reopen(t, b, pair.B)

	// The obligation survived, so it goes out again.
	if sent := b2.PushAcks(now.Add(time.Minute)); sent != 1 {
		t.Fatalf("an unrecorded ack was not repeated after restart: %d", sent)
	}
	again := readReceipts(t, pair.A)
	if len(again) != 1 {
		t.Fatalf("repeat receipt count: %d", len(again))
	}
	// Same claim, byte for byte in the fields that matter: a node applying
	// it twice learns nothing new and records nothing twice.
	if again[0].AcceptedAt != first[0].AcceptedAt ||
		again[0].ExpiresAt != first[0].ExpiresAt ||
		again[0].Kind != first[0].Kind ||
		len(again[0].FrameIDs) != 1 || again[0].FrameIDs[0] != first[0].FrameIDs[0] {
		t.Fatalf("the repeat is a different claim:\n first %+v\n again %+v",
			first[0], again[0])
	}
}

// A withdrawal is repeated until it is believed or until the horizon it
// reports has passed — and it always reports the horizon the ORIGINAL claim
// promised. The two mechanisms are layered: the withdrawal makes a sender
// retry sooner, the expiry guarantees responsibility cannot hang forever
// even if every withdrawal is lost on the air.
func TestWithdrawalRepeatsAndCarriesTheOriginalHorizon(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xE3
	pair := loopback.NewPair(loopback.Faults{Seed: 23})
	b, err := New(Config{
		DataDir: t.TempDir(), Instance: "lapsing",
		Radio: pair.B, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: "127.0.0.1:1", RelayDomain: "relay:none",
		Subscriptions: []Subscription{serving(dest, 0x85, 0x86)},
		AirtimePerMin: 1e9,
		QueueCaps: routing.QueueCaps{
			MaxTotalBytes: 1 << 20, MaxPerDestBytes: 1 << 20, OperatorTTL: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	now := time.Now()

	// An author expiry two hours out; the operator TTL would end custody
	// sooner, which is exactly the early withdrawal being tested.
	f := mkFrame(t, dest, 0x85, 1, nil, "hold this a while")
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f})); err != nil {
		t.Fatal(err)
	}
	b.PumpRadio(now)
	b.PushAcks(now)
	claim := readReceipts(t, pair.A)
	if len(claim) != 1 {
		t.Fatalf("no claim to withdraw: %d", len(claim))
	}
	horizon := claim[0].ExpiresAt

	later := now.Add(2 * time.Hour)
	b.Sweep(later)
	b.PushAcks(later)
	first := readReceipts(t, pair.A)
	if len(first) != 1 || !first[0].Kind.Lapsed() {
		t.Fatalf("withdrawal not sent: %+v", first)
	}
	if first[0].ExpiresAt != horizon {
		t.Fatalf("the withdrawal reports %d, the claim promised %d: "+
			"a sender cannot tell how long responsibility was covered",
			first[0].ExpiresAt, horizon)
	}

	// Lost on the air. It comes back on the retry schedule, unprompted.
	pair.A.Poll()
	if sent := b.PushAcks(later.Add(lapseRetry + time.Second)); sent != 1 {
		t.Fatalf("withdrawal never repeated: %d", sent)
	}
	repeat := readReceipts(t, pair.A)
	if len(repeat) != 1 || !repeat[0].Kind.Lapsed() {
		t.Fatalf("repeat is not a withdrawal: %+v", repeat)
	}

	// It does not repeat forever. Past the attempt limit the expiry the
	// receipt carries is what tells the sender the promise is over.
	for i := range lapseMaxTries + 2 {
		b.PushAcks(later.Add(time.Duration(i+2) * (lapseRetry + time.Second)))
	}
	pair.A.Poll()
	if sent := b.PushAcks(later.Add(24 * time.Hour)); sent != 0 {
		t.Fatalf("withdrawal is still on the air long after the horizon: %d", sent)
	}
}
