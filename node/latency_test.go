package node

import (
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// The ledger keeps the last few own frames, marks each step once, and
// never lets a sibling's receipt count as delivery (the caller filters
// that; here: a receipt at position p covers every own frame at or below).
func TestLatencyLedgerMarksEachStepOnce(t *testing.T) {
	var l latencyLedger
	tid := id.TerminalID{1}
	e1, e2, e3 := id.EventID{1}, id.EventID{2}, id.EventID{3}
	l.minted(e1, tid, 1)
	l.minted(e2, tid, 2)
	l.minted(e3, tid, 3)
	time.Sleep(2 * time.Millisecond)
	l.relayed([]id.EventID{e1, e2}, "relay-a:7411")
	l.relayed([]id.EventID{e1}, "relay-b:7411") // a second relay does not overwrite the first
	l.delivered(tid, 2, id.DeviceID{9})         // covers e1 and e2, not e3
	snap := l.snapshot(10)
	if len(snap) != 3 {
		t.Fatalf("snapshot has %d samples", len(snap))
	}
	by := map[string]LatencySample{}
	for _, s := range snap {
		by[s.Event] = s
	}
	if by[e1.Hex()].Relay != "relay-a:7411" || by[e1.Hex()].RelayedMs < 0 || by[e1.Hex()].DeliveredMs < 0 {
		t.Fatalf("e1 timeline wrong: %+v", by[e1.Hex()])
	}
	if by[e3.Hex()].RelayedMs != -1 || by[e3.Hex()].DeliveredMs != -1 {
		t.Fatalf("e3 should be untouched: %+v", by[e3.Hex()])
	}
	for i := 0; i < latencyKeep+5; i++ {
		l.minted(id.EventID{byte(i), 7}, tid, uint64(10+i))
	}
	if n := len(l.snapshot(1000)); n != latencyKeep {
		t.Fatalf("the ledger kept %d, want %d", n, latencyKeep)
	}
}
