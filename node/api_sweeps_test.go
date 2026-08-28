package node

// API-level gates: the JSON boundary speaks degrees, refusals carry the
// right status codes, and the exports split at gaps.

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/field"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

func TestTrackExportsSplitAtGaps(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "robert")
	defer rt.Close()
	tid, sectorID, _ := sweepFixture(t, rt)
	info, err := rt.StartSweep(tid, sectorID, nil, "Sweep GPX")
	if err != nil {
		t.Fatal(err)
	}
	base := uint64(time.Now().UnixMilli())
	gap := SpoolSample{Tag: field.SampleQGap, UnixMS: base + 30000, DurationMS: 52000, Reason: field.GapNoFix}
	if _, err := rt.AppendSweepSamples(info.SweepID, 1, []SpoolSample{
		fixAt(0, base), fixAt(2, base+15000), gap, fixAt(6, base+90000), fixAt(8, base+105000),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.StopSweep(info.SweepID, schemas.SweepNothingFound, ""); err != nil {
		t.Fatal(err)
	}
	var assetHex string
	_ = rt.withSpace(tid, func(st *spaceState) error {
		f := st.space.State.SweepsForObject(info.SweepID)[0]
		assetHex = hex.EncodeToString(f.TrackAsset[:])
		return nil
	})
	data, _, err := rt.RetrieveAsset(tid, assetHex)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := field.Decode(data)
	if err != nil {
		t.Fatal(err)
	}

	gpx := string(exportGPX(tr, "Sweep GPX"))
	if got := strings.Count(gpx, "<trkseg>"); got != 2 {
		t.Fatalf("GPX must split at the gap: %d segments, want 2", got)
	}
	gj := string(exportGeoJSON(tr))
	if !strings.Contains(gj, `"MultiLineString"`) || !strings.Contains(gj, `"duration_ms":52000`) {
		t.Fatalf("GeoJSON lost the gap: %s", gj)
	}
	if got := strings.Count(gj, "],["); got < 1 {
		t.Fatal("GeoJSON has a single line — it joined across the gap")
	}
	csv := string(exportCSV(tr))
	if !strings.Contains(csv, "gap,") {
		t.Fatal("CSV dropped the gap row")
	}
	if got := strings.Count(csv, "point,"); got != 4 {
		t.Fatalf("CSV point rows: %d, want 4", got)
	}
}
