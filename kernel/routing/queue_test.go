package routing

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func testMeta(dest byte, pri signal.Priority, expires uint64) FrameMeta {
	var d id.TerminalID
	d[0] = dest
	return FrameMeta{Destination: d, Priority: pri, ExpiresAt: expires,
		IngressLink: "mesh:test", Scope: signal.CustodyAllowed}
}

func TestQueueCrashSimReopenDrain(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	q, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	frame := []byte("signed-frame-bytes-stand-in")
	rid, err := q.Enqueue(testMeta(1, signal.PriorityMessage, 0), frame, "dom-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(testMeta(2, signal.PrioritySecurity, 0), frame, "dom-a", now); err != nil {
		t.Fatal(err)
	}
	// "Crash": close without acks, reopen — both records survive with
	// their INGRESS ORIGIN intact (split-horizon survives restart).
	q.Close()
	q2, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	if q2.Len() != 2 {
		t.Fatalf("custody lost across restart: %d", q2.Len())
	}
	rec, ok := q2.Next("relay:x", "relay-x", nil, now)
	if !ok {
		t.Fatal("nothing to send")
	}
	// Security lane first.
	if rec.Priority != signal.PrioritySecurity {
		t.Fatalf("lane order wrong: %v", rec.Priority)
	}
	if rec.IngressLink != "mesh:test" || rec.IngressDomain != "dom-a" {
		t.Fatalf("ingress origin lost: %+v", rec)
	}
	// Same-domain egress is suppressed even after restart.
	if _, ok := q2.Next("mesh:other", "dom-a", nil, now); ok {
		t.Fatal("split-horizon must survive restart")
	}
	q2.Ack(rec.ID)
	_ = rid
	if q2.Len() != 1 {
		t.Fatalf("ack failed: %d", q2.Len())
	}
}

func TestQueueTornTailTruncated(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	q, _ := OpenQueue(dir, DefaultQueueCaps())
	if _, err := q.Enqueue(testMeta(1, signal.PriorityMessage, 0), []byte("frame-1"), "d", now); err != nil {
		t.Fatal(err)
	}
	q.Close()
	// Append garbage (a torn write).
	path := filepath.Join(dir, "custody.seg")
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	f.Write([]byte{0x00, 0x00, 0x00, 0xFF, 0xDE, 0xAD})
	f.Close()

	q2, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	if q2.Len() != 1 {
		t.Fatalf("torn tail broke replay: %d", q2.Len())
	}
	// The truncated segment accepts new appends cleanly.
	if _, err := q2.Enqueue(testMeta(2, signal.PriorityMessage, 0), []byte("frame-2"), "d", now); err != nil {
		t.Fatal(err)
	}
	q2.Close()
	q3, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q3.Close()
	if q3.Len() != 2 {
		t.Fatalf("append after truncation lost: %d", q3.Len())
	}
}

func TestQueueTTLSweepAndScope(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	caps := DefaultQueueCaps()
	caps.OperatorTTL = time.Hour
	q, _ := OpenQueue(dir, caps)
	defer q.Close()

	// NoCustody refused outright.
	m := testMeta(1, signal.PriorityMessage, 0)
	m.Scope = signal.NoCustody
	if _, err := q.Enqueue(m, []byte("presence"), "d", now); err != ErrNoCustody {
		t.Fatalf("NoCustody must be refused: %v", err)
	}
	// Already-expired refused.
	if _, err := q.Enqueue(testMeta(1, signal.PriorityMessage, uint64(now.Unix())-10),
		[]byte("stale"), "d", now); err != ErrExpired {
		t.Fatalf("expired must be refused: %v", err)
	}
	// Author expiry sweeps before operator TTL.
	if _, err := q.Enqueue(testMeta(1, signal.PriorityMessage, uint64(now.Add(10*time.Minute).Unix())),
		[]byte("short-lived"), "d", now); err != nil {
		t.Fatal(err)
	}
	// Operator TTL sweeps long-idle records.
	if _, err := q.Enqueue(testMeta(2, signal.PriorityMessage, 0), []byte("no-expiry"), "d", now); err != nil {
		t.Fatal(err)
	}
	if n := q.Sweep(now.Add(30 * time.Minute)); n != 1 {
		t.Fatalf("author-expiry sweep: %d", n)
	}
	if n := q.Sweep(now.Add(2 * time.Hour)); n != 1 {
		t.Fatalf("operator-TTL sweep: %d", n)
	}
	if q.Len() != 0 {
		t.Fatalf("queue not empty: %d", q.Len())
	}
}

func TestQueueCapsEvictLowestLaneOldest(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	caps := QueueCaps{MaxTotalBytes: 300, MaxPerDestBytes: 300}
	q, _ := OpenQueue(dir, caps)
	defer q.Close()
	frame := make([]byte, 100)

	if _, err := q.Enqueue(testMeta(1, signal.PrioritySecurity, 0), frame, "d", now); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(testMeta(2, signal.PriorityBlob, 0), frame, "d", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(testMeta(3, signal.PriorityMessage, 0), frame, "d", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Fourth 100B record: the blob-lane record (lowest lane) is evicted,
	// never the security one.
	if _, err := q.Enqueue(testMeta(4, signal.PriorityMessage, 0), frame, "d", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 3 {
		t.Fatalf("eviction count: %d", q.Len())
	}
	seen := map[signal.Priority]bool{}
	for {
		rec, ok := q.Next("relay:x", "relay-x", nil, now.Add(4*time.Second))
		if !ok {
			break
		}
		seen[rec.Priority] = true
		q.Ack(rec.ID)
	}
	if seen[signal.PriorityBlob] {
		t.Fatal("blob record must have been evicted")
	}
	if !seen[signal.PrioritySecurity] {
		t.Fatal("security record must survive eviction")
	}
}

func TestQueueCompaction(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	q, _ := OpenQueue(dir, DefaultQueueCaps())
	big := make([]byte, 1<<20)
	var keep uint64
	for i := 0; i < 4; i++ {
		rid, err := q.Enqueue(testMeta(byte(i), signal.PriorityMessage, 0), big, "d",
			now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			keep = rid
		} else {
			q.Ack(rid) // triggers compaction once dead > live
		}
	}
	fi, err := os.Stat(filepath.Join(dir, "custody.seg"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 3<<20 {
		t.Fatalf("compaction did not shrink the segment: %d bytes", fi.Size())
	}
	q.Close()
	q2, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	if q2.Len() != 1 {
		t.Fatalf("post-compaction replay: %d", q2.Len())
	}
	if rec, ok := q2.Next("x", "y", nil, now.Add(time.Minute)); !ok || rec.ID != keep {
		t.Fatal("live record lost in compaction")
	}
}

func TestQueueAgingPreventsStarvation(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	q, _ := OpenQueue(dir, DefaultQueueCaps())
	defer q.Close()
	// An OLD manifest-lane record vs a FRESH message-lane record: aging
	// promotes the old one past the fresher, higher lane.
	if _, err := q.Enqueue(testMeta(1, signal.PriorityManifest, 0), []byte("old-manifest"), "d", now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(30 * time.Minute)
	if _, err := q.Enqueue(testMeta(2, signal.PriorityMessage, 0), []byte("fresh-msg"), "d", later); err != nil {
		t.Fatal(err)
	}
	rec, ok := q.Next("x", "y", nil, later.Add(time.Second))
	if !ok {
		t.Fatal("nothing to send")
	}
	if rec.Priority != signal.PriorityManifest {
		t.Fatalf("aging failed — starved lane not promoted: %v", rec.Priority)
	}
}

func TestRegistryAndPolicy(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate scheme must panic")
		}
	}()
	RegisterScheme("test-x", nil)
	// Reserved seam compiles and is distinct.
	if SchemeReticulum != "reticulum" {
		t.Fatal("reticulum seam renamed")
	}
	if !RadioAdmits("message.text.v1", 200) {
		t.Fatal("text must ride radio")
	}
	if RadioAdmits("message.text.v1", RadioFrameCap+1) {
		t.Fatal("oversized frame must not ride radio")
	}
	if RadioAdmits("weird.future.v1", 100) {
		t.Fatal("unknown family must wait for a fast path")
	}
	now := time.Unix(1_784_000_000, 0)
	b := NewTokenBucket(600, 100, now) // 10 B/s
	if !b.Take(100, now) {
		t.Fatal("burst must be available")
	}
	if b.Take(50, now.Add(time.Second)) {
		t.Fatal("bucket must be empty after burst (only ~10 refilled)")
	}
	if !b.Take(50, now.Add(6*time.Second)) {
		t.Fatal("refill must restore tokens")
	}
	RegisterScheme("test-x", nil) // panics (deferred check)
}

// The head-of-line failure, pinned. A record held back by backoff must be
// SKIPPED, not treated as the end of the queue: before NextEligibleAt, a
// bridge asked for its single best record, found it was in its resend gap,
// and stopped the entire pass — so four frames waiting for four different
// people went out one every 45 seconds while the queue sat full.
func TestBackoffSkipsRatherThanBlocks(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	q, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	// Four destinations, same lane, enqueued oldest-first.
	var ids []uint64
	for d := byte(1); d <= 4; d++ {
		rid, err := q.Enqueue(testMeta(d, signal.PriorityMessage, 0),
			[]byte("frame"), "dom-a", now.Add(time.Duration(d)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rid)
	}

	// Send the oldest and hold it back for 45s, as a radio push would.
	first, ok := q.Next("relay:x", "relay-x", nil, now)
	if !ok {
		t.Fatal("nothing eligible")
	}
	q.Requeue(first.ID, now.Add(45*time.Second))

	// The very next pick must be a DIFFERENT record, not a refusal.
	second, ok := q.Next("relay:x", "relay-x", nil, now)
	if !ok {
		t.Fatal("a record in backoff blocked the whole queue")
	}
	if second.ID == first.ID {
		t.Fatal("a record in backoff was handed out again")
	}

	// And a batch drain sees every record that is genuinely ready.
	batch := q.NextBatch("relay:x", "relay-x", nil, now, 10)
	if len(batch) != 3 {
		t.Fatalf("batch saw %d of 3 eligible records", len(batch))
	}
	for _, rec := range batch {
		if rec.ID == first.ID {
			t.Fatal("batch included a record still in backoff")
		}
	}

	// Backoff is durable: a bridge that restarts must not immediately
	// re-air everything it just sent.
	q.Close()
	q2, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	if got := len(q2.NextBatch("relay:x", "relay-x", nil, now, 10)); got != 3 {
		t.Fatalf("backoff lost across restart: %d eligible, want 3", got)
	}
	// Once the gap passes, it comes back on its own.
	later := now.Add(46 * time.Second)
	if got := len(q2.NextBatch("relay:x", "relay-x", nil, later, 10)); got != 4 {
		t.Fatalf("record never became eligible again: %d of 4", got)
	}
	_ = ids
}
