package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// The scoped queries must answer exactly what the whole-ledger walk
// answered — the delivery path used to ask for every intent on the device
// and throw away the ones that were not its space, which meant copying and
// SORTING the whole ledger once per space per cycle.
func TestScopedDueMatchesTheWholeLedgerWalk(t *testing.T) {
	now := time.Unix(1_784_000_000, 0)
	l, err := OpenLedger(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	mine, other := tid(1), tid(2)
	// Interleaved across spaces, and enqueued out of age order.
	for i, sp := range []id.TerminalID{mine, other, mine, other, mine} {
		if _, err := l.Enqueue(eid(byte(i+1)), sp, 128, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// One of mine is settled: it must not come back on either path.
	if err := l.Settle(eid(3)); err != nil {
		t.Fatal(err)
	}

	at := now.Add(time.Hour)
	var want []DeliveryIntent
	for _, in := range l.Due(at, 0) {
		if in.Space == mine {
			want = append(want, in)
		}
	}
	got := l.DueForSpace(at, mine, 0)
	if len(got) != len(want) {
		t.Fatalf("scoped due returned %d, the filtered walk %d", len(got), len(want))
	}
	for i := range want {
		if got[i].EventID != want[i].EventID {
			t.Fatalf("order differs at %d: %v vs %v", i, got[i].EventID, want[i].EventID)
		}
	}
	if len(got) == 0 {
		t.Fatal("the fixture produced nothing due — the comparison proves nothing")
	}

	oldest, ok := l.OldestDueForSpace(at, mine)
	if !ok || oldest.EventID != want[0].EventID {
		t.Fatalf("oldest-due is not the head of the ordered list: %v", oldest.EventID)
	}
	if _, ok := l.OldestDueForSpace(at, tid(9)); ok {
		t.Fatal("a space with nothing outstanding reported an intent")
	}

	// The limit still truncates the ORDERED result, not an arbitrary slice.
	if one := l.DueForSpace(at, mine, 1); len(one) != 1 || one[0].EventID != want[0].EventID {
		t.Fatalf("limit did not keep the oldest: %+v", one)
	}
}
