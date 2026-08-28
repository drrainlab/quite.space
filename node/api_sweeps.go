package node

// The sweep API (SP-3.2). The host's pump and the web-ui both speak
// here; JSON speaks float degrees at the boundary (the api_field.go
// pattern) and the wire keeps its fixed point.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/field"
	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
)

func (a *APIServer) routeSweeps(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/spaces/{id}/sweeps", a.auth(a.handleStartSweep))
	mux.HandleFunc("GET /api/spaces/{id}/sweeps", a.auth(a.handleSpaceSweeps))
	mux.HandleFunc("GET /api/sweeps", a.auth(a.handleActiveSweeps))
	mux.HandleFunc("POST /api/sweeps/{sid}/samples", a.auth(a.handleSweepSamples))
	mux.HandleFunc("POST /api/sweeps/{sid}/resume", a.auth(a.handleSweepResume))
	mux.HandleFunc("POST /api/sweeps/{sid}/stop", a.auth(a.handleSweepStop))
	mux.HandleFunc("GET /api/spaces/{id}/objects/{oid}/track.gpx", a.auth(a.handleTrackExport("gpx")))
	mux.HandleFunc("GET /api/spaces/{id}/objects/{oid}/track.geojson", a.auth(a.handleTrackExport("geojson")))
	mux.HandleFunc("GET /api/spaces/{id}/objects/{oid}/track.csv", a.auth(a.handleTrackExport("csv")))
}

func sweepIDParam(r *http.Request) ([16]byte, error) {
	var sid [16]byte
	b, err := hex.DecodeString(r.PathValue("sid"))
	if err != nil || len(b) != 16 {
		return sid, errors.New("bad sweep id")
	}
	copy(sid[:], b)
	return sid, nil
}

func sweepInfoJSON(i SweepInfo) map[string]any {
	return map[string]any{
		"sweep_id":   hex.EncodeToString(i.SweepID[:]),
		"space":      i.Space.Hex(),
		"parent_id":  hex.EncodeToString(i.ParentID[:]),
		"label":      i.Label,
		"state":      i.State,
		"started_at": i.StartedAt,
		"samples":    i.Samples,
		"distance_m": i.DistanceM,
	}
}

func (a *APIServer) handleStartSweep(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// parent_object_id, NOT object_id: the sweep mints its OWN object;
	// this names the sector it belongs to.
	body, err := readBody[struct {
		ParentObjectID string `json:"parent_object_id"`
		TaskID         string `json:"task_id"`
		Name           string `json:"name"`
	}](r)
	if err != nil || body.ParentObjectID == "" {
		httpErr(w, http.StatusBadRequest, errors.New("parent_object_id required"))
		return
	}
	pb, err := hex.DecodeString(body.ParentObjectID)
	if err != nil || len(pb) != 16 {
		httpErr(w, http.StatusBadRequest, errors.New("bad parent_object_id"))
		return
	}
	var parent [16]byte
	copy(parent[:], pb)
	var taskID []byte
	if body.TaskID != "" {
		tb, err := hex.DecodeString(body.TaskID)
		if err != nil || len(tb) != len(id.EventID{}) {
			httpErr(w, http.StatusBadRequest, errors.New("bad task_id"))
			return
		}
		taskID = tb
	}
	info, err := a.rt.StartSweep(tid, parent, taskID, strings.TrimSpace(body.Name))
	if err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, sweepInfoJSON(info))
}

func (a *APIServer) handleActiveSweeps(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, i := range a.rt.ActiveSweeps() {
		out = append(out, sweepInfoJSON(i))
	}
	writeJSON(w, map[string]any{"sweeps": out})
}

func (a *APIServer) handleSweepSamples(w http.ResponseWriter, r *http.Request) {
	sid, err := sweepIDParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Seq     uint64 `json:"seq"`
		Samples []struct {
			Kind       string  `json:"kind"` // "point" | "gap"
			UnixMS     uint64  `json:"unix_ms"`
			Lat        float64 `json:"lat"`
			Lon        float64 `json:"lon"`
			AccuracyM  uint64  `json:"accuracy_m"`
			DurationMS uint64  `json:"duration_ms"`
			Reason     string  `json:"reason"` // "no_fix" | "suspended" | "unknown"
		} `json:"samples"`
	}](r)
	if err != nil || body.Seq == 0 {
		httpErr(w, http.StatusBadRequest, errors.New("seq and samples required"))
		return
	}
	samples := make([]SpoolSample, 0, len(body.Samples))
	for _, s := range body.Samples {
		switch s.Kind {
		case "point":
			pt, err := geo.FromDegrees(s.Lat, s.Lon)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err)
				return
			}
			samples = append(samples, SpoolSample{Tag: field.SampleQPoint, UnixMS: s.UnixMS,
				LatE7U: pt.LatE7U, LonE7U: pt.LonE7U, AccuracyM: s.AccuracyM})
		case "gap":
			reason := map[string]uint64{
				"no_fix": field.GapNoFix, "suspended": field.GapSuspended, "unknown": field.GapUnknown,
			}[s.Reason]
			if reason == 0 || s.DurationMS == 0 {
				httpErr(w, http.StatusBadRequest, errors.New("gap requires a vocabulary reason and a duration"))
				return
			}
			samples = append(samples, SpoolSample{Tag: field.SampleQGap, UnixMS: s.UnixMS,
				DurationMS: s.DurationMS, Reason: reason})
		default:
			httpErr(w, http.StatusBadRequest, fmt.Errorf("unknown sample kind %q", s.Kind))
			return
		}
	}
	info, err := a.rt.AppendSweepSamples(sid, body.Seq, samples)
	if err != nil {
		if errors.Is(err, ErrSweepClosed) || errors.Is(err, errSweepGone) {
			// 409: the host's cue to stop pumping — the session ended
			// (finalized remotely, or capture closed).
			httpErr(w, http.StatusConflict, err)
			return
		}
		if strings.Contains(err.Error(), "no space left") {
			httpErr(w, http.StatusInsufficientStorage, err)
			return
		}
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, sweepInfoJSON(info))
}

func (a *APIServer) handleSweepResume(w http.ResponseWriter, r *http.Request) {
	sid, err := sweepIDParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.ResumeSweep(sid); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *APIServer) handleSweepStop(w http.ResponseWriter, r *http.Request) {
	sid, err := sweepIDParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Result string `json:"result"`
		Note   string `json:"note"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.StopSweep(sid, body.Result, strings.TrimSpace(body.Note)); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleSpaceSweeps: the reducer's facts merged with the object cache —
// THE EVENT WINS disagreements (ADR-034).
func (a *APIServer) handleSpaceSweeps(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	out := []map[string]any{}
	err = a.rt.withSpace(tid, func(st *spaceState) error {
		for _, f := range st.space.State.Sweeps() {
			row := map[string]any{
				"sweep_id":    hex.EncodeToString(f.ObjectID[:]),
				"result":      f.Result,
				"started_at":  f.StartedAt,
				"ended_at":    f.EndedAt,
				"distance_m":  f.DistanceM,
				"track_asset": hex.EncodeToString(f.TrackAsset[:]),
				"fallback":    f.Fallback,
				"bbox": []float64{f.BBoxMin.LatDeg(), f.BBoxMin.LonDeg(),
					f.BBoxMax.LatDeg(), f.BBoxMax.LonDeg()},
			}
			if o, ok := st.space.State.ObjectByID(f.ObjectID); ok {
				row["label"] = o.Record.Name
				if o.Record.Parent != nil {
					row["parent_id"] = hex.EncodeToString(o.Record.Parent[:])
				}
			}
			out = append(out, row)
		}
		return nil
	})
	if err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"sweeps": out})
}

// ---- export projections (free per ADR-033) ----

func (a *APIServer) handleTrackExport(format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, err := a.spaceID(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		ob, err := hex.DecodeString(r.PathValue("oid"))
		if err != nil || len(ob) != 16 {
			httpErr(w, http.StatusBadRequest, errors.New("bad object id"))
			return
		}
		var oid [16]byte
		copy(oid[:], ob)
		var assetHex, label string
		err = a.rt.withSpace(tid, func(st *spaceState) error {
			facts := st.space.State.SweepsForObject(oid)
			if len(facts) == 0 {
				return errors.New("node: no completed sweep on this object")
			}
			f := facts[len(facts)-1]
			assetHex = hex.EncodeToString(f.TrackAsset[:])
			if o, ok := st.space.State.ObjectByID(oid); ok {
				label = o.Record.Name
			}
			return nil
		})
		if err != nil {
			httpErr(w, http.StatusNotFound, err)
			return
		}
		data, _, err := a.rt.RetrieveAsset(tid, assetHex)
		if err != nil {
			httpErr(w, http.StatusConflict, err)
			return
		}
		tr, err := field.Decode(data)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
		switch format {
		case "gpx":
			w.Header().Set("Content-Type", "application/gpx+xml")
			w.Write(exportGPX(tr, label))
		case "geojson":
			w.Header().Set("Content-Type", "application/geo+json")
			w.Write(exportGeoJSON(tr))
		case "csv":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Write(exportCSV(tr))
		}
	}
}

// trackSegments walks the samples into point runs split at every gap —
// the ONE iteration every exporter shares, so no projection can join
// across a gap by accident.
type trackPoint struct {
	unixMS uint64
	pt     geo.Point
	acc    uint64
}

func trackSegments(tr *field.Track) ([][]trackPoint, []field.Sample) {
	var segs [][]trackPoint
	var gaps []field.Sample
	var cur []trackPoint
	clock := tr.StartedAt * 1000
	for _, s := range tr.Samples {
		clock += s.DtMS
		switch s.Tag {
		case field.SampleQPoint:
			cur = append(cur, trackPoint{unixMS: clock, pt: s.Point, acc: s.AccuracyM})
		case field.SampleQGap:
			gaps = append(gaps, s)
			if len(cur) > 0 {
				segs = append(segs, cur)
				cur = nil
			}
			clock += s.DurationMS
		}
	}
	if len(cur) > 0 {
		segs = append(segs, cur)
	}
	return segs, gaps
}

// exportGPX splits a <trkseg> at every gap: the structural honesty of
// the format carried into the projection.
func exportGPX(tr *field.Track, label string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<gpx version="1.1" creator="Quiet Spaces" xmlns="http://www.topografix.com/GPX/1/1">` + "\n")
	fmt.Fprintf(&b, "  <trk><name>%s</name>\n", xmlEscape(label))
	segs, _ := trackSegments(tr)
	for _, seg := range segs {
		b.WriteString("    <trkseg>\n")
		for _, p := range seg {
			fmt.Fprintf(&b, `      <trkpt lat="%.7f" lon="%.7f"><time>%s</time></trkpt>`+"\n",
				p.pt.LatDeg(), p.pt.LonDeg(), gpxTime(p.unixMS))
		}
		b.WriteString("    </trkseg>\n")
	}
	b.WriteString("  </trk>\n</gpx>\n")
	return []byte(b.String())
}

func exportGeoJSON(tr *field.Track) []byte {
	segs, gaps := trackSegments(tr)
	var b strings.Builder
	b.WriteString(`{"type":"Feature","properties":{"gaps":[`)
	for i, g := range gaps {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"duration_ms":%d,"reason":%d}`, g.DurationMS, g.Reason)
	}
	b.WriteString(`]},"geometry":{"type":"MultiLineString","coordinates":[`)
	for i, seg := range segs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("[")
		for j, p := range seg {
			if j > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "[%.7f,%.7f]", p.pt.LonDeg(), p.pt.LatDeg())
		}
		b.WriteString("]")
	}
	b.WriteString("]}}")
	return []byte(b.String())
}

func exportCSV(tr *field.Track) []byte {
	var b strings.Builder
	b.WriteString("kind,unix_ms,lat,lon,accuracy_m,duration_ms,reason\n")
	clock := tr.StartedAt * 1000
	for _, s := range tr.Samples {
		clock += s.DtMS
		switch s.Tag {
		case field.SampleQPoint:
			fmt.Fprintf(&b, "point,%d,%.7f,%.7f,%d,,\n", clock, s.Point.LatDeg(), s.Point.LonDeg(), s.AccuracyM)
		case field.SampleQGap:
			fmt.Fprintf(&b, "gap,%d,,,,%d,%d\n", clock, s.DurationMS, s.Reason)
			clock += s.DurationMS
		}
	}
	return []byte(b.String())
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func gpxTime(unixMS uint64) string {
	return time.UnixMilli(int64(unixMS)).UTC().Format(time.RFC3339)
}
