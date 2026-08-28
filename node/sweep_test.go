package node

// SP-3.2 engine gates: the full lifecycle, both sagas re-driven across
// restarts, both orphan outcomes, the interrupted/task law, and the
// honesty tests — positions only from fresh fixes, zero after Stop.

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/field"
	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func sweepFixture(t *testing.T, rt *Runtime) (id.TerminalID, [16]byte, id.EventID) {
	t.Helper()
	tid, err := rt.CreateSpace("field ops")
	if err != nil {
		t.Fatal(err)
	}
	sector := &objects.Record{Kind: "sector", Name: "Sector B3"}
	sectorID, _, err := rt.CreateObject(tid, sector)
	if err != nil {
		t.Fatal(err)
	}
	card, err := rt.MakeCard(tid, "sweep western edge", CardOptions{ObjectID: &sectorID})
	if err != nil {
		t.Fatal(err)
	}
	return tid, sectorID, card
}

func fixAt(latMilli int, unixMS uint64) SpoolSample {
	p, _ := geo.FromDegrees(46.600+float64(latMilli)/1000, 8.030)
	return SpoolSample{Tag: field.SampleQPoint, UnixMS: unixMS,
		LatE7U: p.LatE7U, LonE7U: p.LonE7U, AccuracyM: 8}
}

func TestSweepHappyPathEndToEnd(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "robert")
	defer rt.Close()
	tid, sectorID, card := sweepFixture(t, rt)

	info, err := rt.StartSweep(tid, sectorID, card[:], "Sweep 01")
	if err != nil {
		t.Fatal(err)
	}
	base := uint64(time.Now().UnixMilli())
	if _, err := rt.AppendSweepSamples(info.SweepID, 1, []SpoolSample{
		fixAt(0, base), fixAt(2, base+15000), fixAt(4, base+30000),
	}); err != nil {
		t.Fatal(err)
	}
	// A host-reported gap rides inside a batch.
	gap := SpoolSample{Tag: field.SampleQGap, UnixMS: base + 45000, DurationMS: 52000, Reason: field.GapNoFix}
	if _, err := rt.AppendSweepSamples(info.SweepID, 2, []SpoolSample{gap, fixAt(8, base+97000)}); err != nil {
		t.Fatal(err)
	}
	if err := rt.StopSweep(info.SweepID, schemas.SweepNothingFound, "чисто, следов нет"); err != nil {
		t.Fatal(err)
	}

	// The completion fact folded, the task closed, the note landed, the
	// object cache says completed, and the asset decodes back into the
	// same shaped track — gap preserved.
	err = rt.withSpace(tid, func(st *spaceState) error {
		facts := st.space.State.SweepsForObject(info.SweepID)
		if len(facts) != 1 {
			t.Fatalf("want 1 completion fact, got %d", len(facts))
		}
		f := facts[0]
		if f.Result != schemas.SweepNothingFound || f.DistanceM == 0 {
			t.Fatalf("fact wrong: %+v", f)
		}
		o, ok := st.space.State.ObjectByID(info.SweepID)
		if !ok || o.Record.Status != "completed" {
			t.Fatalf("object cache: %+v", o.Record)
		}
		var cardDone bool
		for _, c := range st.space.State.Cards() {
			if c.ID == card && c.Status == "done" {
				cardDone = true
			}
		}
		if !cardDone {
			t.Fatal("the linked task did not close")
		}
		var noted bool
		for _, o := range st.space.State.Objects() {
			if o.ObjectID == sectorID && len(o.Observations) > 0 {
				noted = true
			}
		}
		if !noted {
			t.Fatal("the operator's note did not land on the sector")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The keystore record retired; the session is gone from the actives.
	if got := rt.ActiveSweeps(); len(got) != 0 {
		t.Fatalf("finished sweep still active: %+v", got)
	}
	// Capture is closed forever.
	if _, err := rt.AppendSweepSamples(info.SweepID, 3, []SpoolSample{fixAt(9, base+120000)}); err == nil {
		t.Fatal("samples accepted after finalize")
	}
}

func TestSweepInterruptedLeavesTheTaskOpen(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "robert")
	tid, sectorID, card := sweepFixture(t, rt)
	info, err := rt.StartSweep(tid, sectorID, card[:], "Sweep 02")
	if err != nil {
		t.Fatal(err)
	}
	base := uint64(time.Now().UnixMilli())
	if _, err := rt.AppendSweepSamples(info.SweepID, 1, []SpoolSample{fixAt(0, base), fixAt(2, base+15000)}); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	// The node reopens with NO service to claim the session. The grace
	// is hours shorter in prod; here we drive the orphan path directly
	// by reopening and waiting out a shortened grace via the restore
	// path — the sweep must finalize as interrupted.
	rt2 := openRuntime(t, dir, "robert")
	defer rt2.Close()
	// restoreSweeps armed the grace; claim nothing and force the orphan
	// decision now rather than sleeping two minutes.
	rec, ok := rt2.sweepRecord(info.SweepID)
	if !ok || rec.State != storage.SweepSuspended {
		t.Fatalf("restored state = %d, want suspended", rec.State)
	}
	rt2.updateSweepRecord(info.SweepID, func(sw *storage.SweepRecord) {
		sw.Result = schemas.SweepInterrupted
		sw.StoppedAt = uint64(time.Now().Unix())
		sw.State = storage.SweepStopped
	})
	rt2.stopSweepTicker(info.SweepID)
	if err := rt2.finalizeSweep(info.SweepID); err != nil {
		t.Fatal(err)
	}

	err = rt2.withSpace(tid, func(st *spaceState) error {
		facts := st.space.State.SweepsForObject(info.SweepID)
		if len(facts) != 1 || facts[0].Result != schemas.SweepInterrupted {
			t.Fatalf("facts: %+v", facts)
		}
		// THE LAW: an interrupted sweep did not do the work — the card
		// stays open, and the object cache says interrupted, not
		// completed.
		for _, c := range st.space.State.Cards() {
			if c.ID == card && c.Status != "open" {
				t.Fatalf("interrupted sweep closed the task: %s", c.Status)
			}
		}
		o, _ := st.space.State.ObjectByID(info.SweepID)
		if o.Record.Status != "interrupted" {
			t.Fatalf("object cache lies: %q", o.Record.Status)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSweepResumeWearsASuspendedGap(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "robert")
	tid, sectorID, _ := sweepFixture(t, rt)
	info, err := rt.StartSweep(tid, sectorID, nil, "Sweep 03")
	if err != nil {
		t.Fatal(err)
	}
	base := uint64(time.Now().UnixMilli()) - 120_000
	if _, err := rt.AppendSweepSamples(info.SweepID, 1, []SpoolSample{fixAt(0, base)}); err != nil {
		t.Fatal(err)
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "robert")
	defer rt2.Close()
	// The service's sticky restart claims the session within the grace.
	if err := rt2.ResumeSweep(info.SweepID); err != nil {
		t.Fatal(err)
	}
	rec, _ := rt2.sweepRecord(info.SweepID)
	if rec.State != storage.SweepRecording {
		t.Fatalf("resume did not restore recording: %d", rec.State)
	}
	// The node authored EXACTLY ONE gap, suspended, covering its own
	// dead span — its lifecycle, not an invention.
	rt2ByID := rt2.sweepByID(info.SweepID)
	samples := rt2ByID.spool.Samples()
	gaps := 0
	for _, s := range samples {
		if s.Tag == field.SampleQGap {
			gaps++
			if s.Reason != field.GapSuspended {
				t.Fatalf("resume gap reason %d, want suspended", s.Reason)
			}
			if s.DurationMS < 60_000 {
				t.Fatalf("suspended gap too short to be honest: %d ms", s.DurationMS)
			}
		}
	}
	if gaps != 1 {
		t.Fatalf("want exactly 1 suspended gap, got %d", gaps)
	}
	// And recording continues on the same session.
	if _, err := rt2.AppendSweepSamples(info.SweepID, 2, []SpoolSample{fixAt(3, uint64(time.Now().UnixMilli()))}); err != nil {
		t.Fatal(err)
	}
	if err := rt2.StopSweep(info.SweepID, schemas.SweepFound, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSweepStartSagaRedrivesAcrossACrash(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "robert")
	tid, sectorID, _ := sweepFixture(t, rt)

	// Simulate the crash between "record persisted" and "object
	// created": persist the starting record by hand and reopen.
	var sid [16]byte
	sid[0] = 0x5A
	rt.mu.Lock()
	rt.ks.Sweeps = append(rt.ks.Sweeps, storage.SweepRecord{
		Space: tid, SweepID: sid, ParentID: sectorID, Label: "Sweep 04",
		StartedAt: uint64(time.Now().Unix()), State: storage.SweepStarting,
	})
	if err := rt.saveKeystore(); err != nil {
		rt.mu.Unlock()
		t.Fatal(err)
	}
	rt.mu.Unlock()
	rt.Close()

	rt2 := openRuntime(t, dir, "robert")
	defer rt2.Close()
	// The re-driven start saga produced the Object AND a live session —
	// no Object-without-session, no session-without-Object.
	err := rt2.withSpace(tid, func(st *spaceState) error {
		if _, ok := st.space.State.ObjectByID(sid); !ok {
			t.Fatal("start saga re-drive left no object")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := rt2.sweepRecord(sid)
	if !ok || (rec.State != storage.SweepRecording && rec.State != storage.SweepSuspended) {
		t.Fatalf("re-driven state: %+v ok=%v", rec, ok)
	}
	if rt2.sweepByID(sid) == nil {
		t.Fatal("no live session after the re-drive")
	}
}

// The asset round-trips: the sealed track downloads and decodes with
// the gap intact — the renderer's segment iteration depends on it.
func TestSweepTrackAssetRoundTrips(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "robert")
	defer rt.Close()
	tid, sectorID, _ := sweepFixture(t, rt)
	info, err := rt.StartSweep(tid, sectorID, nil, "Sweep 05")
	if err != nil {
		t.Fatal(err)
	}
	base := uint64(time.Now().UnixMilli())
	gap := SpoolSample{Tag: field.SampleQGap, UnixMS: base + 15000, DurationMS: 30000, Reason: field.GapUnknown}
	if _, err := rt.AppendSweepSamples(info.SweepID, 1, []SpoolSample{fixAt(0, base), gap, fixAt(5, base+45000)}); err != nil {
		t.Fatal(err)
	}
	if err := rt.StopSweep(info.SweepID, schemas.SweepFound, ""); err != nil {
		t.Fatal(err)
	}
	var assetHex string
	err = rt.withSpace(tid, func(st *spaceState) error {
		facts := st.space.State.SweepsForObject(info.SweepID)
		if len(facts) != 1 {
			t.Fatal("no fact")
		}
		assetHex = hex.EncodeToString(facts[0].TrackAsset[:])
		// The edge exists with role "track".
		for _, e := range st.space.State.EdgesForObject(info.SweepID) {
			if e.Role == "track" && e.Asset == assetHex {
				return nil
			}
		}
		t.Fatal("no track edge on the sweep object")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := rt.RetrieveAsset(tid, assetHex)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := field.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Samples) != 3 || tr.Samples[1].Tag != field.SampleQGap ||
		tr.Samples[1].Reason != field.GapUnknown {
		t.Fatalf("the gap did not survive the seal: %+v", tr.Samples)
	}
}

// THE HONESTY TEST: positions are emitted only from a FRESH fix — an
// old fix re-stamped with a new TTL would draw ● live on a stale point
// — and after Stop the emission is ZERO, because the law is about
// capture and the consent scope ended.
func TestSweepPositionsOnlyFreshAndSilentAfterStop(t *testing.T) {
	old := sweepPositionEvery
	sweepPositionEvery = 50 * time.Millisecond
	defer func() { sweepPositionEvery = old }()

	rt := openRuntime(t, t.TempDir(), "robert")
	defer rt.Close()
	tid, sectorID, _ := sweepFixture(t, rt)
	info, err := rt.StartSweep(tid, sectorID, nil, "Sweep 06")
	if err != nil {
		t.Fatal(err)
	}
	// A FRESH fix → the ticker emits a claim.
	if _, err := rt.AppendSweepSamples(info.SweepID, 1,
		[]SpoolSample{fixAt(0, uint64(time.Now().UnixMilli()))}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	sawLive := false
	for time.Now().Before(deadline) {
		var known bool
		_ = rt.withSpace(tid, func(st *spaceState) error {
			p := st.space.Trust.Position(rt.Self.TerminalID, uint64(time.Now().Unix()))
			known = p.Known
			return nil
		})
		if known {
			sawLive = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawLive {
		t.Fatal("a fresh fix never became a position claim")
	}

	// A STALE fix (older than sweepFixFresh) must NOT be re-emitted:
	// feed one two minutes old and watch for silence.
	rt2 := openRuntime(t, t.TempDir(), "katya")
	defer rt2.Close()
	tid2, sector2, _ := sweepFixture(t, rt2)
	info2, err := rt2.StartSweep(tid2, sector2, nil, "Sweep 07")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt2.AppendSweepSamples(info2.SweepID, 1,
		[]SpoolSample{fixAt(0, uint64(time.Now().Add(-2*time.Minute).UnixMilli()))}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = rt2.withSpace(tid2, func(st *spaceState) error {
		p := st.space.Trust.Position(rt2.Self.TerminalID, uint64(time.Now().Unix()))
		if p.Known {
			t.Fatal("a stale fix was re-stamped as a live position")
		}
		return nil
	})

	// After Stop: zero further emission, even with a fresh fix cached.
	if err := rt.StopSweep(info.SweepID, schemas.SweepFound, ""); err != nil {
		t.Fatal(err)
	}
	var expiryBefore uint64
	_ = rt.withSpace(tid, func(st *spaceState) error {
		p := st.space.Trust.Position(rt.Self.TerminalID, uint64(time.Now().Unix()))
		expiryBefore = p.RemainingSeconds
		return nil
	})
	time.Sleep(300 * time.Millisecond)
	_ = rt.withSpace(tid, func(st *spaceState) error {
		p := st.space.Trust.Position(rt.Self.TerminalID, uint64(time.Now().Unix()))
		if p.RemainingSeconds > expiryBefore {
			t.Fatal("a position claim was emitted after Stop")
		}
		return nil
	})
}
