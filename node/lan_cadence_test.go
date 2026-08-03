package node

import (
	"testing"
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// sixSpaces builds a runtime holding six spaces and returns them shaped the
// way the pump sees them. Six, because six is what the user's node actually
// held on the night this wave was written.
func sixSpaces(t *testing.T) (*Runtime, []activeSpace) {
	t.Helper()
	rt := openRuntime(t, t.TempDir(), "alice")
	titles := []string{"one", "two", "three", "four", "five", "six"}
	ids := make([]id.TerminalID, 0, len(titles))
	for _, title := range titles {
		tid, err := rt.CreateSpace(title)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tid)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	active := make([]activeSpace, 0, len(ids))
	for _, tid := range ids {
		st := rt.spaces[tid]
		if st == nil {
			t.Fatalf("space %s vanished", tid.String()[:8])
		}
		active = append(active, activeSpace{tid: tid, st: st})
	}
	return rt, active
}

// The failure this wave exists to end.
//
// adoptLinkFiltered held ONE lastSummary for the whole link and, when it
// elapsed, called SendSummary once per active space in the SAME tick. Six
// spaces meant six whole messages handed to a carrier that moves about one
// every thirty seconds — oversubscribed threefold before anybody pressed
// anything, which is why an invitation could sit behind minutes of sync.
func TestSixSpacesDoNotFireOnOneTick(t *testing.T) {
	rt, active := sixSpaces(t)
	defer rt.Close()

	cad := newLinkCadence(true, time.Now())
	const every = 60 * time.Second

	start := time.Now()
	worst := 0
	for tick := 0; tick < 600; tick++ { // 20 minutes at the 2s radio pump
		now := start.Add(time.Duration(tick) * 2 * time.Second)
		got := cad.due(active, every, now)
		if len(got) > worst {
			worst = len(got)
		}
		for _, a := range got {
			cad.done(a, nil, now) // the pump's report: it went out
		}
	}
	if worst > 1 {
		t.Fatalf("a metered link offered %d summaries in one tick; on this "+
			"carrier that is %d whole messages handed over at once", worst, worst)
	}
	if worst == 0 {
		t.Fatal("a metered link never offered a summary at all: tiering that " +
			"silences a link is not tiering, it is a broken link")
	}
}

// Pacing must not become starving. Every space still gets its turn.
func TestEverySpaceEventuallyGetsItsTurn(t *testing.T) {
	rt, active := sixSpaces(t)
	defer rt.Close()

	cad := newLinkCadence(true, time.Now())
	const every = 60 * time.Second

	seen := map[id.TerminalID]int{}
	start := time.Now()
	for tick := 0; tick < 900; tick++ { // 30 minutes
		now := start.Add(time.Duration(tick) * 2 * time.Second)
		for _, a := range cad.due(active, every, now) {
			seen[a.tid]++
			cad.done(a, nil, now)
		}
	}
	for _, a := range active {
		if seen[a.tid] == 0 {
			t.Fatalf("space %s never offered a summary in thirty minutes: "+
				"round-robin that skips somebody is a space that never syncs",
				a.tid.String()[:8])
		}
	}
}

// A LAN link is not the bottleneck and must not be slowed. Byte-identical
// behaviour to the shared timer it replaces.
func TestAnUnmeteredLinkStillOffersEverySpaceTogether(t *testing.T) {
	rt, active := sixSpaces(t)
	defer rt.Close()

	cad := newLinkCadence(false, time.Now())
	const every = 2 * time.Second

	start := time.Now()
	// The FIRST call fires, because the shared timer starts at the zero time —
	// exactly what `lastSummary := time.Time{}` did before this type existed.
	// Preserving that is the point: LAN behaviour must be unchanged.
	if got := cad.due(active, every, start); len(got) != len(active) {
		t.Fatalf("an unmetered link offered %d of %d spaces on its first tick; "+
			"LAN was never the bottleneck and pacing it is a regression",
			len(got), len(active))
	}
	if got := cad.due(active, every, start.Add(time.Second)); len(got) != 0 {
		t.Fatalf("an unmetered link offered %d summaries again inside its "+
			"interval", len(got))
	}
	if got := cad.due(active, every, start.Add(3*time.Second)); len(got) != len(active) {
		t.Fatalf("an unmetered link offered %d of %d spaces after its interval "+
			"elapsed", len(got), len(active))
	}
}

// A carrier with no room did not carry it, so the turn is not spent.
//
// The old shared timer advanced whether or not anything went out, which is how
// six spaces stayed permanently in lockstep. Repeating that per-space would be
// the same defect at finer grain: a link that is merely busy would quietly cost
// each space its slot and then wait a whole interval before trying again.
func TestARefusedSummaryKeepsItsTurn(t *testing.T) {
	rt, active := sixSpaces(t)
	defer rt.Close()

	now := time.Now()
	cad := newLinkCadence(true, now)
	const every = 60 * time.Second

	first := cad.due(active, every, now)
	if len(first) != 1 {
		t.Fatalf("the cadence released %d summaries, want 1", len(first))
	}
	// The carrier had no room. Nothing was carried.
	cad.done(first[0], kernelsync.ErrCarrierFull, now)

	again := cad.due(active, every, now)
	if len(again) != 1 {
		t.Fatalf("after a refusal the cadence released %d summaries, want 1",
			len(again))
	}
	if again[0].tid != first[0].tid {
		t.Fatal("after a refusal a DIFFERENT space took the turn: the refused " +
			"one lost its slot to a send that never left the node")
	}
}

// A budget the link cannot pay must not silently cost a space its turn.
func TestThePumpSkipsASummaryItCannotAfford(t *testing.T) {
	rt, active := sixSpaces(t)
	defer rt.Close()

	now := time.Now()
	cad := newLinkCadence(true, now)
	const every = 60 * time.Second

	// Spend the whole burst, so the next summary is unaffordable at this
	// instant. Taking it in one go is what a large transfer would do.
	//
	// The check has to happen AT `now`: the budget is 2000 bytes a minute, so
	// the 1 KiB burst is back inside about thirty seconds and any later
	// instant would be testing the refill rather than the refusal.
	if !cad.air.Take(1024, now) {
		t.Fatal("a fresh airtime budget could not pay its own burst")
	}
	if got := cad.due(active, every, now); len(got) != 0 {
		t.Fatalf("a link with no airtime left still offered %d summaries", len(got))
	}
	// And the turn is still OWED: nothing was recorded as sent, so as soon as
	// the budget refills the same space goes first. An advance-regardless timer
	// is how the old pump kept six spaces permanently in lockstep.
	if len(cad.last) == 0 {
		t.Fatal("no space was even considered due; the test proved nothing")
	}
	if got := cad.due(active, every, now.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("after the budget refilled the link offered %d summaries, "+
			"want exactly 1 — the skipped turn was lost rather than deferred",
			len(got))
	}
}
