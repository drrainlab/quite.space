package routing

import (
	"errors"
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
	if n, _ := q.Sweep(now.Add(30 * time.Minute)); n != 1 {
		t.Fatalf("author-expiry sweep: %d", n)
	}
	if n, _ := q.Sweep(now.Add(2 * time.Hour)); n != 1 {
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

// A promise, once signed, outranks the eviction policy. The queue may drop
// the oldest lowest-lane record to make room — but not one whose custody
// was already acknowledged to its sender, because that sender was told it
// could stop retrying. When the only way to fit a new frame is to break
// that promise, the new frame is refused instead.
func TestGuaranteedCustodySurvivesPressure(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	frame := make([]byte, 256)
	caps := QueueCaps{MaxTotalBytes: 600, MaxPerDestBytes: 1 << 20, OperatorTTL: time.Hour}
	q, err := OpenQueue(dir, caps)
	if err != nil {
		t.Fatal(err)
	}

	// Two acknowledged records fill the store.
	promised := make([]uint64, 0, 2)
	for d := byte(1); d <= 2; d++ {
		rid, err := q.EnqueueGuaranteed(testMeta(d, signal.PriorityMessage, 0),
			frame, "dom-a", now, []byte("attempt-token"))
		if err != nil {
			t.Fatalf("guaranteed enqueue %d: %v", d, err)
		}
		promised = append(promised, rid)
	}

	// A third frame would have to displace one of them. It is refused, and
	// the refusal is specific enough for an operator to act on.
	_, err = q.Enqueue(testMeta(3, signal.PriorityMessage, 0), frame, "dom-a", now)
	if !errors.Is(err, ErrNoRoom) {
		t.Fatalf("a promised record was evictable: %v", err)
	}
	for _, rid := range promised {
		if _, ok := q.live[rid]; !ok {
			t.Fatal("an acknowledged record was dropped to make room")
		}
	}

	// Guarantees are durable: a restart must not turn them back into
	// ordinary evictable records.
	q.Close()
	q2, err := OpenQueue(dir, caps)
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	if _, err := q2.Enqueue(testMeta(3, signal.PriorityMessage, 0), frame, "dom-a", now); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("guarantee lost across restart: %v", err)
	}

	// Expiry is the one way a promise ends, and it is REPORTED: the sender
	// has to learn that custody is over rather than discover it by silence.
	dropped, lapsed := q2.Sweep(now.Add(2 * time.Hour))
	if dropped != 2 || len(lapsed) != 2 {
		t.Fatalf("expiry of promised custody unreported: dropped %d, lapsed %d",
			dropped, len(lapsed))
	}
}

// An ordinary record is still evictable — the protection is for promises,
// not a way to make the queue unbounded.
func TestUnpromisedCustodyStillEvicts(t *testing.T) {
	now := time.Unix(1_784_000_000, 0)
	frame := make([]byte, 256)
	q, err := OpenQueue(t.TempDir(), QueueCaps{
		MaxTotalBytes: 600, MaxPerDestBytes: 1 << 20, OperatorTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	for d := byte(1); d <= 4; d++ {
		if _, err := q.Enqueue(testMeta(d, signal.PriorityMessage, 0),
			frame, "dom-a", now.Add(time.Duration(d)*time.Second)); err != nil {
			t.Fatalf("enqueue %d: %v", d, err)
		}
	}
	if q.Len() > 2 {
		t.Fatalf("cap not enforced without guarantees: %d records", q.Len())
	}
}

// Soak: a bridge that retries the same stuck record for a very long time
// must not grow its store without limit. The old design kept "when did I
// last send this" in a map that nothing ever pruned; moving the schedule
// into the record fixed the leak in memory and could have recreated it on
// disk, since every deferral appends. Superseded deferrals count as dead
// weight, so compaction eventually reclaims them.
func TestRequeueSoakStaysBounded(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	q, err := OpenQueue(dir, DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	rid, err := q.Enqueue(testMeta(1, signal.PriorityMessage, 0),
		make([]byte, 512), "dom-a", now)
	if err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(dir, "custody.seg")

	// ~45s of backoff per attempt: months of a genuinely stuck record.
	const attempts = 200_000
	for i := range attempts {
		q.Requeue(rid, now.Add(time.Duration(i)*45*time.Second))
	}
	st, err := os.Stat(seg)
	if err != nil {
		t.Fatal(err)
	}
	// Without reclamation this would be tens of megabytes of superseded
	// scheduling. The bound is generous — the point is that it EXISTS.
	t.Logf("segment after %d retries: %d bytes", attempts, st.Size())
	if st.Size() > 4<<20 {
		t.Fatalf("custody segment grew to %d bytes over %d retries: "+
			"a long-lived gateway would fill its disk with scheduling",
			st.Size(), attempts)
	}
	// The record itself is intact and still carries its latest schedule.
	if q.Len() != 1 {
		t.Fatalf("soak lost the record: %d live", q.Len())
	}
	if len(q.NextBatch("relay:x", "relay-x", nil, now, 10)) != 0 {
		t.Fatal("a record deep in backoff came back early")
	}
	final := now.Add(time.Duration(attempts) * 45 * time.Second)
	if len(q.NextBatch("relay:x", "relay-x", nil, final, 10)) != 1 {
		t.Fatal("the record never became eligible again after the soak")
	}
}

// Every optional custody field, written and read back field for field.
//
// This exists because of how the last one broke: encodePut declared a fixed
// map header and then appended optional keys after it, so the attempt token
// and the lease were written to disk and never read again. The decoder
// stopped at the declared count. Nothing failed, nothing logged — a gateway
// simply came back up unable to name the custody it was still holding.
//
// A round-trip test that only checks the fields it happens to think of
// would have missed it too. This one compares the whole record.
func TestCustodyRecordRoundTripsEveryField(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	q, err := OpenQueue(dir, QueueCaps{
		MaxTotalBytes: 1 << 20, MaxPerDestBytes: 1 << 20, OperatorTTL: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	meta := testMeta(7, signal.PrioritySecurity, uint64(now.Add(time.Hour).Unix()))
	meta.IngressLink = "mesh:radio0"
	frame := []byte("signed-frame-bytes-stand-in")
	attempt := []byte("attempt-token-16b")

	rec, _, err := q.AcceptCustody(meta, frame, "meshtastic-quiet@beta", now, attempt)
	if err != nil {
		t.Fatal(err)
	}
	eid := id.EventIDOf(frame)
	// Give the retry schedule a non-zero value too, so no field is left at
	// its zero value where a dropped write would be invisible.
	q.Requeue(rec.ID, now.Add(45*time.Second))
	before, ok := q.Held(eid)
	if !ok {
		t.Fatal("record missing before restart")
	}
	q.Close()

	q2, err := OpenQueue(dir, QueueCaps{
		MaxTotalBytes: 1 << 20, MaxPerDestBytes: 1 << 20, OperatorTTL: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	after, ok := q2.Held(eid)
	if !ok {
		t.Fatal("record lost across restart")
	}

	for _, f := range []struct {
		name      string
		got, want any
	}{
		{"ID", after.ID, before.ID},
		{"Frame", string(after.Frame), string(before.Frame)},
		{"Destination", after.Destination, before.Destination},
		{"Priority", after.Priority, before.Priority},
		{"ExpiresAt", after.ExpiresAt, before.ExpiresAt},
		{"IngressLink", after.IngressLink, before.IngressLink},
		{"IngressDomain", after.IngressDomain, before.IngressDomain},
		{"EnqueuedAt", after.EnqueuedAt, before.EnqueuedAt},
		{"Attempts", after.Attempts, before.Attempts},
		{"NextEligibleAt", after.NextEligibleAt, before.NextEligibleAt},
		{"Guaranteed", after.Guaranteed, before.Guaranteed},
		{"Attempt", string(after.Attempt), string(before.Attempt)},
		{"Lease", after.Lease, before.Lease},
	} {
		if f.got != f.want {
			t.Errorf("%s did not survive the restart: got %v, want %v",
				f.name, f.got, f.want)
		}
	}
	// And the values are actually meaningful, not all zero.
	if len(after.Attempt) == 0 || after.Lease.Zero() || after.NextEligibleAt == 0 {
		t.Fatalf("the test asserted equality between empty values: %+v", after)
	}
}
