package eventlog

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

type author struct {
	priv ed25519.PrivateKey
	dev  id.DeviceID
	prin id.PrincipalID
	term id.TerminalID
	seq  uint64
	tip  id.EventID
	clk  uint64
}

func newAuthor(t *testing.T, term id.TerminalID, seed byte) *author {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	a := &author{priv: priv, term: term}
	copy(a.dev[:], priv.Public().(ed25519.PublicKey))
	a.prin[0] = seed
	return a
}

func (a *author) next(t *testing.T, text string) []byte {
	t.Helper()
	a.seq++
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
		Sequence:        a.seq,
		Schema:          schemas.MessageText,
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

func TestAppendAndDedup(t *testing.T) {
	term := id.TerminalID{0xAA}
	l := New(term, nil)
	a := newAuthor(t, term, 1)

	f1 := a.next(t, "one")
	applied, err := l.Ingest(f1)
	if err != nil || len(applied) != 1 {
		t.Fatalf("ingest: %v %d", err, len(applied))
	}
	// Duplicate is a silent no-op (idempotency).
	applied, err = l.Ingest(f1)
	if err != nil || len(applied) != 0 {
		t.Fatalf("duplicate not a no-op: %v %d", err, len(applied))
	}
	if l.Len() != 1 {
		t.Fatal("length wrong after dedup")
	}
}

func TestOutOfOrderArrival(t *testing.T) {
	term := id.TerminalID{0xAB}
	l := New(term, nil)
	a := newAuthor(t, term, 2)
	f1 := a.next(t, "one")
	f2 := a.next(t, "two")
	f3 := a.next(t, "three")

	// Arrive 3, 2, 1: nothing applies until 1 lands, then all drain.
	if applied, err := l.Ingest(f3); err != nil || len(applied) != 0 {
		t.Fatalf("f3: %v %d", err, len(applied))
	}
	if applied, err := l.Ingest(f2); err != nil || len(applied) != 0 {
		t.Fatalf("f2: %v %d", err, len(applied))
	}
	applied, err := l.Ingest(f1)
	if err != nil || len(applied) != 3 {
		t.Fatalf("f1 drain: %v %d", err, len(applied))
	}
	sum := l.Summary()
	if len(sum) != 1 || sum[0].ContiguousUntil != 3 {
		t.Fatalf("summary wrong: %+v", sum)
	}
}

func TestForkQuarantine(t *testing.T) {
	term := id.TerminalID{0xAC}
	l := New(term, nil)
	a := newAuthor(t, term, 3)
	f1 := a.next(t, "one")
	if _, err := l.Ingest(f1); err != nil {
		t.Fatal(err)
	}
	// Author "rewrites history": a different event at sequence 1.
	forkAuthor := newAuthor(t, term, 3)
	g1 := forkAuthor.next(t, "one, but different")
	_, err := l.Ingest(g1)
	if !errors.Is(err, ErrChainForked) {
		t.Fatalf("fork not detected: %v", err)
	}
	if !l.Forked(a.dev) {
		t.Fatal("chain not marked forked")
	}
	// Everything further from that device is quarantined, not applied.
	f2 := a.next(t, "two")
	if _, err := l.Ingest(f2); !errors.Is(err, ErrChainForked) {
		t.Fatalf("post-fork event not quarantined: %v", err)
	}
	if l.Len() != 1 {
		t.Fatal("fork mutated applied history")
	}
}

func TestBrokenPreviousIsFork(t *testing.T) {
	term := id.TerminalID{0xAD}
	l := New(term, nil)
	a := newAuthor(t, term, 4)
	f1 := a.next(t, "one")
	if _, err := l.Ingest(f1); err != nil {
		t.Fatal(err)
	}
	// Forge sequence 2 with a wrong previous hash.
	a.tip = id.EventID{0xEE}
	f2 := a.next(t, "two with bad link")
	if _, err := l.Ingest(f2); !errors.Is(err, ErrChainForked) {
		t.Fatalf("bad previous accepted: %v", err)
	}
}

func TestAdmissionFailClosed(t *testing.T) {
	term := id.TerminalID{0xAE}
	deny := func(env *signal.Envelope) error { return errors.New("not certified") }
	l := New(term, deny)
	a := newAuthor(t, term, 5)
	if _, err := l.Ingest(a.next(t, "hello")); err == nil {
		t.Fatal("unadmitted event applied")
	}
	if l.Len() != 0 {
		t.Fatal("denied event stored")
	}
}

func TestPersistAndRebuildIdentical(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	term := id.TerminalID{0xAF}

	l, replayed, err := Open(term, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 0 {
		t.Fatal("fresh log replayed events")
	}
	a := newAuthor(t, term, 6)
	b := newAuthor(t, term, 7)
	var ids []id.EventID
	for i := 0; i < 5; i++ {
		for _, au := range []*author{a, b} {
			applied, err := l.Ingest(au.next(t, "m"))
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, applied[0].ID)
		}
	}
	firstSummary := l.Summary()
	l.Close()

	// Reopen: replay must reproduce identical state (M0.4 acceptance).
	l2, replayed, err := Open(term, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if len(replayed) != 10 || l2.Len() != 10 {
		t.Fatalf("replay count %d, len %d", len(replayed), l2.Len())
	}
	for i, r := range replayed {
		if r.ID != ids[i] {
			t.Fatalf("replay order diverged at %d", i)
		}
	}
	secondSummary := l2.Summary()
	if len(firstSummary) != len(secondSummary) {
		t.Fatal("summaries differ")
	}
	for i := range firstSummary {
		if firstSummary[i] != secondSummary[i] {
			t.Fatalf("summary entry %d differs", i)
		}
	}
}

func TestTornTailTruncatedOnOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	term := id.TerminalID{0xB0}
	l, _, err := Open(term, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := newAuthor(t, term, 8)
	if _, err := l.Ingest(a.next(t, "good")); err != nil {
		t.Fatal(err)
	}
	l.Close()

	// Simulate a crash mid-write: append garbage half-record.
	seg := filepath.Join(dir, "000001.seg")
	f, err := os.OpenFile(seg, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0x00, 0x00, 0x01})
	f.Close()

	l2, replayed, err := Open(term, dir, nil)
	if err != nil {
		t.Fatalf("torn tail not tolerated: %v", err)
	}
	defer l2.Close()
	if len(replayed) != 1 {
		t.Fatalf("expected 1 replayed event, got %d", len(replayed))
	}
	// The log must still accept appends after truncation.
	if _, err := l2.Ingest(a.next(t, "after crash")); err != nil {
		t.Fatal(err)
	}
}

func TestFramesInRange(t *testing.T) {
	term := id.TerminalID{0xB1}
	l := New(term, nil)
	a := newAuthor(t, term, 9)
	for i := 0; i < 5; i++ {
		if _, err := l.Ingest(a.next(t, "m")); err != nil {
			t.Fatal(err)
		}
	}
	frames := l.FramesInRange(a.dev, 2, 4)
	if len(frames) != 3 {
		t.Fatalf("got %d frames", len(frames))
	}
	env, _ := signal.Decode(frames[0])
	if env.Sequence != 2 {
		t.Fatal("range start wrong")
	}
}

// A HOLE IN A CHAIN IS NEVER APPLIED — the load-bearing property every
// autonomous instrument must build on (QI-M, owner's amendment 1). If a
// device persists (seq, tip) and then loses power before its frame N
// leaves, frame N+1 references a predecessor nobody has: it is buffered
// as pending forever and everything after it piles up behind. There is
// no "fleeting frames may skip" exemption, and this test is here so that
// nobody adds one by accident: the cure is a persistent outbox on the
// device, not leniency in the log.
func TestAHoleInTheChainIsNeverApplied(t *testing.T) {
	var term id.TerminalID
	term[0] = 9
	log := New(term, nil)
	a := newAuthor(t, term, 0x0B)
	f1 := a.next(t, "one")
	_ = a.next(t, "two — lost to a power cut before it was ever sent")
	f3 := a.next(t, "three")
	f4 := a.next(t, "four")

	if _, err := log.Ingest(f1); err != nil {
		t.Fatal(err)
	}
	applied, err := log.Ingest(f3)
	if err != nil || len(applied) != 0 {
		t.Fatalf("frame 3 with a missing predecessor: applied=%d err=%v (want buffered, nothing applied)", len(applied), err)
	}
	applied, err = log.Ingest(f4)
	if err != nil || len(applied) != 0 {
		t.Fatalf("frame 4 behind the hole: applied=%d err=%v", len(applied), err)
	}
	if log.Len() != 1 {
		t.Fatalf("log holds %d applied frames, want exactly 1 — the hole stops the chain", log.Len())
	}
	seq, _, _ := log.ChainTip(a.dev)
	if seq != 1 {
		t.Fatalf("chain tip advanced past the hole: seq=%d", seq)
	}
}

// A hole in the chain is COUNTED, not merely respected. The frames parked
// behind it are the difference between an empty room and a room whose past
// is stuck one frame upstream, and a reader who cannot see that number
// cannot tell those apart.
func TestParkedFramesAreCountedAndTheHoleIsNamed(t *testing.T) {
	var term id.TerminalID
	term[0] = 9
	log := New(term, nil)
	a := newAuthor(t, term, 0x0C)
	f1 := a.next(t, "one")
	f2 := a.next(t, "two — too big to travel by relay, and everything after it waits")
	f3 := a.next(t, "three")
	f4 := a.next(t, "four")

	if log.Pending() != 0 || len(log.PendingGaps()) != 0 {
		t.Fatalf("a fresh log reports pending=%d gaps=%d", log.Pending(), len(log.PendingGaps()))
	}
	for _, f := range [][]byte{f1, f3, f4} {
		if _, err := log.Ingest(f); err != nil {
			t.Fatal(err)
		}
	}
	if got := log.Pending(); got != 2 {
		t.Fatalf("pending = %d, want 2 — three and four are held behind the hole", got)
	}
	gaps := log.PendingGaps()
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly one hole", gaps)
	}
	if gaps[0].Device != a.dev || gaps[0].WaitingFor != 2 || gaps[0].Held != 2 {
		t.Fatalf("the hole is misnamed: %+v (want device %s waiting for seq 2 with 2 held)",
			gaps[0], a.dev)
	}

	// The predecessor arrives: the buffer drains and the count says so.
	applied, err := log.Ingest(f2)
	if err != nil || len(applied) != 3 {
		t.Fatalf("seq 2 arrived: applied=%d err=%v (want 2, 3 and 4)", len(applied), err)
	}
	if log.Pending() != 0 || len(log.PendingGaps()) != 0 {
		t.Fatalf("drained log still reports pending=%d gaps=%d", log.Pending(), len(log.PendingGaps()))
	}
}
