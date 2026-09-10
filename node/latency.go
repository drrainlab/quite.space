package node

// LT-1 — THE TIMELINE OF A MESSAGE. Three facts a sender can measure
// about their own words without asking anybody anything: when the frame
// was minted here, when a relay first accepted it (and which), and when
// a device outside this principal first signed for holding it. Kept for
// the last few dozen own frames, in memory, for the diagnostics screen —
// so "delivery feels slow" becomes a number with a place, not a feeling.

import (
	"sort"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

const latencyKeep = 32

type latencySample struct {
	id        id.EventID
	space     id.TerminalID
	seq       uint64
	minted    time.Time
	relayed   time.Time
	relay     string
	delivered time.Time
	receiptor id.DeviceID
}

type latencyLedger struct {
	mu   sync.Mutex
	list []*latencySample // oldest first
}

func (l *latencyLedger) minted(eid id.EventID, tid id.TerminalID, seq uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, &latencySample{id: eid, space: tid, seq: seq, minted: time.Now()})
	if len(l.list) > latencyKeep {
		l.list = l.list[len(l.list)-latencyKeep:]
	}
}

// relayed marks the first relay acceptance for each of the given frames.
func (l *latencyLedger) relayed(ids []id.EventID, relay string) {
	if len(ids) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	set := map[id.EventID]bool{}
	for _, e := range ids {
		set[e] = true
	}
	now := time.Now()
	for _, s := range l.list {
		if s.relayed.IsZero() && set[s.id] {
			s.relayed, s.relay = now, relay
		}
	}
}

// delivered marks every own frame of the space at or below pos that a
// foreign device just signed for — the first such receipt wins.
func (l *latencyLedger) delivered(tid id.TerminalID, pos uint64, receiptor id.DeviceID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, s := range l.list {
		if s.space == tid && s.seq <= pos && s.delivered.IsZero() {
			s.delivered, s.receiptor = now, receiptor
		}
	}
}

// LatencySample is the diagnostics projection: durations in ms from the
// mint, -1 where the step has not happened.
type LatencySample struct {
	Event       string `json:"event"`
	Space       string `json:"space"`
	MintedAt    int64  `json:"minted_at"`
	RelayedMs   int64  `json:"relayed_ms"`
	Relay       string `json:"relay,omitempty"`
	DeliveredMs int64  `json:"delivered_ms"`
	Receiptor   string `json:"receiptor,omitempty"`
}

func (l *latencyLedger) snapshot(limit int) []LatencySample {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LatencySample, 0, len(l.list))
	for i := len(l.list) - 1; i >= 0 && len(out) < limit; i-- {
		s := l.list[i]
		ls := LatencySample{Event: s.id.Hex(), Space: s.space.Hex(), MintedAt: s.minted.Unix(),
			RelayedMs: -1, DeliveredMs: -1, Relay: s.relay}
		if !s.relayed.IsZero() {
			ls.RelayedMs = s.relayed.Sub(s.minted).Milliseconds()
		}
		if !s.delivered.IsZero() {
			ls.DeliveredMs = s.delivered.Sub(s.minted).Milliseconds()
			ls.Receiptor = s.receiptor.Hex()
		}
		out = append(out, ls)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MintedAt > out[j].MintedAt })
	return out
}
