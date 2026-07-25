package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func eid(b byte) id.EventID {
	var e id.EventID
	e[0] = b
	return e
}

func tid(b byte) id.TerminalID {
	var t id.TerminalID
	t[0] = b
	return t
}

// Responsibility survives a power cut, and re-walking the log after that cut
// does not multiply it. Both halves matter: a lost intent means a message
// silently abandoned, a duplicated one means the same event queued twice.
func TestLedgerSurvivesCrashAndEnqueueIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	l, err := OpenLedger(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(1); i <= 3; i++ {
		if _, err := l.Enqueue(eid(i), tid(9), now); err != nil {
			t.Fatal(err)
		}
	}
	// A second pass over the same events — what a restart's log replay does.
	for i := byte(1); i <= 3; i++ {
		if _, err := l.Enqueue(eid(i), tid(9), now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if l.Len() != 3 {
		t.Fatalf("re-enqueue multiplied responsibility: %d intents", l.Len())
	}

	// "Crash": no Close, just reopen the directory.
	l2, err := OpenLedger(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Len() != 3 {
		t.Fatalf("responsibility lost across restart: %d", l2.Len())
	}
	in, ok := l2.Get(eid(2))
	if !ok {
		t.Fatal("intent missing after restart")
	}
	if in.Space != tid(9) || in.State != IntentPending {
		t.Fatalf("intent restored wrong: %+v", in)
	}
	if in.Proof != claims.DeliveryCreatedLocal {
		t.Fatalf("proof restored wrong: %v", in.Proof)
	}
}

// A torn tail costs the last record, never the file. The ledger truncates
// to the last clean edge exactly as the event log does.
func TestLedgerTornTailTruncated(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	l, err := OpenLedger(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(1); i <= 3; i++ {
		if _, err := l.Enqueue(eid(i), tid(9), now); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()

	path := filepath.Join(dir, "delivery.ledger")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Half a record: the machine lost power mid-write.
	if err := os.WriteFile(path, append(data, data[:len(data)/4]...), 0o600); err != nil {
		t.Fatal(err)
	}
	l2, err := OpenLedger(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Len() != 3 {
		t.Fatalf("torn tail cost more than the torn record: %d intents", l2.Len())
	}
	// The file is clean again — the next append starts from a good edge.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(data) {
		t.Fatalf("tail not truncated: %d bytes, want %d", len(after), len(data))
	}
	if _, err := l2.Enqueue(eid(4), tid(9), now); err != nil {
		t.Fatal(err)
	}
	l2.Close()
	l3, err := OpenLedger(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l3.Close()
	if l3.Len() != 4 {
		t.Fatalf("append after truncation lost: %d", l3.Len())
	}
}

// Custody suspends retry, and the promised horizon is what un-suspends it.
// A withdrawal can be lost on the air; the expiry is the backstop that
// stops responsibility hanging forever on a gateway that went quiet.
func TestCustodySuspendsRetryUntilTheHorizonPasses(t *testing.T) {
	now := time.Unix(1_784_000_000, 0)
	l, err := OpenLedger(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Enqueue(eid(1), tid(9), now); err != nil {
		t.Fatal(err)
	}
	// Fresh intents are due immediately.
	if got := l.Due(now, 10); len(got) != 1 {
		t.Fatalf("a new intent is not due: %d", len(got))
	}

	horizon := now.Add(time.Hour)
	if _, changed, err := l.Update(eid(1), now, func(in *DeliveryIntent) bool {
		in.State = IntentCustody
		in.Proof = claims.DeliveryAcceptedByRelay
		in.Lease = "gw0/17"
		in.LeaseExpires = horizon.Unix()
		return true
	}); err != nil || !changed {
		t.Fatalf("update: changed=%v err=%v", changed, err)
	}
	if got := l.Due(now.Add(time.Minute), 10); len(got) != 0 {
		t.Fatal("an intent in gateway custody is still being retried")
	}
	if got := l.Due(horizon.Add(time.Second), 10); len(got) != 1 {
		t.Fatal("custody expired and the intent never came back: " +
			"responsibility would hang forever if a withdrawal were lost")
	}

	// Settling ends it for good.
	if err := l.Settle(eid(1)); err != nil {
		t.Fatal(err)
	}
	if got := l.Due(horizon.Add(time.Hour), 10); len(got) != 0 {
		t.Fatal("a settled intent came back")
	}
	if l.Len() != 0 {
		t.Fatalf("settled intent still tracked: %d", l.Len())
	}
}

// A failed write must not leave memory claiming something disk does not.
func TestLedgerUpdateRollsBackWhenTheWriteFails(t *testing.T) {
	now := time.Unix(1_784_000_000, 0)
	l, err := OpenLedger(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Enqueue(eid(1), tid(9), now); err != nil {
		t.Fatal(err)
	}
	// Close the file underneath: the next append fails.
	l.f.Close()
	_, changed, err := l.Update(eid(1), now, func(in *DeliveryIntent) bool {
		in.State = IntentCustody
		in.Proof = claims.DeliveryAcceptedByRelay
		return true
	})
	if err == nil {
		t.Fatal("a failed durable write reported success")
	}
	if changed {
		t.Fatal("a failed write reported the intent as changed")
	}
	in, _ := l.Get(eid(1))
	if in.State != IntentPending || in.Proof != claims.DeliveryCreatedLocal {
		t.Fatalf("memory kept a change that never reached disk: %+v", in)
	}
}

// Compaction keeps every live intent and drops the superseded records.
func TestLedgerCompaction(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_784_000_000, 0)
	l, err := OpenLedger(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(1); i <= 5; i++ {
		if _, err := l.Enqueue(eid(i), tid(9), now); err != nil {
			t.Fatal(err)
		}
	}
	for range 50 {
		for i := byte(1); i <= 5; i++ {
			if _, _, err := l.Update(eid(i), now, func(in *DeliveryIntent) bool {
				in.AttemptNo++
				return true
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := l.Settle(eid(5)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dir, "delivery.ledger"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Compact(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(dir, "delivery.ledger"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("compaction reclaimed nothing: %d then %d", before.Size(), after.Size())
	}
	l.Close()

	l2, err := OpenLedger(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Len() != 4 {
		t.Fatalf("compaction changed the live set: %d intents", l2.Len())
	}
	in, ok := l2.Get(eid(1))
	if !ok || in.AttemptNo != 50 {
		t.Fatalf("compaction lost the latest state: %+v", in)
	}
	if _, ok := l2.Get(eid(5)); ok {
		t.Fatal("compaction resurrected a settled intent")
	}
}

// The quota refuses rather than evicts. Dropping an intent to make room
// would silently abandon a message; refusing keeps the caller informed.
func TestLedgerQuotaRefusesRatherThanEvicts(t *testing.T) {
	now := time.Unix(1_784_000_000, 0)
	l, err := OpenLedger(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := byte(1); i <= 2; i++ {
		if _, err := l.Enqueue(eid(i), tid(9), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Enqueue(eid(3), tid(9), now); err != ErrLedgerFull {
		t.Fatalf("a full ledger accepted a third intent: %v", err)
	}
	if _, ok := l.Get(eid(1)); !ok {
		t.Fatal("an existing intent was evicted to make room")
	}
	// An already-tracked event is still idempotent when full.
	if _, err := l.Enqueue(eid(1), tid(9), now); err != nil {
		t.Fatalf("a full ledger refused an event it already tracks: %v", err)
	}
}
