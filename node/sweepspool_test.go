package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/field"
)

func spoolPoints(t *testing.T, base uint64, n int) []SpoolSample {
	t.Helper()
	out := make([]SpoolSample, n)
	for i := range out {
		out[i] = SpoolSample{Tag: field.SampleQPoint, UnixMS: base + uint64(i)*15000,
			LatE7U: 1_366_180_000 + uint64(i), LonE7U: 1_880_290_000, AccuracyM: 8}
	}
	return out
}

func TestSpoolSurvivesReopenAndTornTail(t *testing.T) {
	dir := t.TempDir()
	sp, err := openSweepSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.AppendBatch(1, spoolPoints(t, 1_790_000_000_000, 3)); err != nil {
		t.Fatal(err)
	}
	if err := sp.AppendNodeGap(1_790_000_050_000, 52_000, field.GapSuspended); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.AppendBatch(2, spoolPoints(t, 1_790_000_102_000, 2)); err != nil {
		t.Fatal(err)
	}
	sp.Close()

	// Tear the tail mid-record: everything after the damage is suspect
	// and must be truncated, everything before it must survive.
	path := filepath.Join(dir, "track.spool")
	st, _ := os.Stat(path)
	if err := os.Truncate(path, st.Size()-5); err != nil {
		t.Fatal(err)
	}
	sp2, err := openSweepSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sp2.Close()
	got := sp2.Samples()
	// batch 1 (3 points) + gap survive; batch 2 was torn.
	if len(got) != 4 {
		t.Fatalf("after a torn tail: %d samples, want 4", len(got))
	}
	if got[3].Tag != field.SampleQGap || got[3].Reason != field.GapSuspended {
		t.Fatalf("the gap did not survive: %+v", got[3])
	}
	if sp2.LastSeq() != 1 {
		t.Fatalf("lastSeq=%d after batch 2 was torn, want 1", sp2.LastSeq())
	}
	// The re-POST of the torn batch lands cleanly now.
	if ok, err := sp2.AppendBatch(2, spoolPoints(t, 1_790_000_102_000, 2)); err != nil || !ok {
		t.Fatalf("retry of the torn batch refused: ok=%v err=%v", ok, err)
	}
}

func TestSpoolBatchIdempotency(t *testing.T) {
	sp, err := openSweepSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	if ok, _ := sp.AppendBatch(1, spoolPoints(t, 1, 2)); !ok {
		t.Fatal("first batch refused")
	}
	// The same batch again — a host retry after a lost 200 — is a no-op.
	ok, err := sp.AppendBatch(1, spoolPoints(t, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a replayed batch was appended twice")
	}
	if got := len(sp.Samples()); got != 2 {
		t.Fatalf("%d samples after a replay, want 2", got)
	}
}

func TestSpoolSurfacesWriteFailure(t *testing.T) {
	sp, err := openSweepSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sp.f.Close() // simulate the disk going away under the handle
	if _, err := sp.AppendBatch(1, spoolPoints(t, 1, 1)); err == nil {
		t.Fatal("a failed write was reported as recorded")
	}
	// And the in-memory state must NOT have advanced past the truth.
	if sp.LastSeq() != 0 || len(sp.Samples()) != 0 {
		t.Fatal("memory claims what the disk refused")
	}
}
