package node

// The sweep engine (SP-3.2, ADR-034). The recording lives HERE, in the
// node: the Android host is a sensor pump that posts fixes over the
// loopback API and can die without taking the truth with it. Both ends
// of a session are sagas persisted step-by-step in the SweepRecord —
// the START saga so a crash between the record and the Object leaves
// neither an Object without a session nor a session without an Object,
// and the FINALIZE saga so a crash between the asset and the event is
// re-driven without repeating or skipping a step.
//
// The two laws (the brief, verbatim):
//   Background location exists only inside an explicitly started,
//   visibly active, bounded Field Session.
//   A Sweep Object owns the meaning of the operation; the detailed
//   trajectory is an attached asset, never an oversized Object
//   revision.
//
// After Stop, no further location capture or position emission occurs;
// finalization and synchronization of the completed sweep may continue.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/field"
	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

const (
	// sweepResumeGrace: how long a restored session waits for the
	// Android service's resume claim before the hybrid orphan policy
	// finalizes it as interrupted (owner decision, 2026-08-28).
	sweepResumeGrace = 2 * time.Minute
	sweepPositionTTL = 600
	// sweepFixFresh: a cached fix older than this is not re-emitted —
	// re-stamping an old fix with a fresh TTL would draw ● live on a
	// stale point, which is the exact lie the ladder exists to prevent.
	sweepFixFresh = 90 * time.Second
)

// sweepPositionEvery: the cadence of ordinary position claims emitted
// WHILE recording. The sweep is its own consent scope; the map-open
// toggle is untouched. A var so a test can drive the clock.
var sweepPositionEvery = 60 * time.Second

var errSweepGone = errors.New("node: no such sweep session")

// ErrSweepClosed answers a sample POST that arrives after capture
// closed — the host's cue to stop pumping and stand down.
var ErrSweepClosed = errors.New("node: sweep capture is closed")

type sweepRuntime struct {
	rec    storage.SweepRecord
	spool  *sweepSpool
	stop   chan struct{} // stops the position ticker; closed once
	sealed *sealedTrack  // geometry cache across finalize re-drives
	// lastFix is the freshest point the pump delivered, for the ticker.
	// ITS OWN LOCK, not r.mu: the writer is a sample POST and the reader
	// is the ticker goroutine — neither holds the runtime lock at that
	// moment, and the -race detector proved the gap the day this shipped.
	fixMu   sync.Mutex
	lastFix struct {
		pt     geo.Point
		acc    uint64
		unixMS uint64
	}
}

// SweepInfo is the API-facing view of a live session.
type SweepInfo struct {
	SweepID   [16]byte
	Space     id.TerminalID
	ParentID  [16]byte
	Label     string
	State     uint64
	StartedAt uint64
	Samples   int
	DistanceM uint64
}

// StartSweep runs the start saga. The SweepID is minted FIRST and the
// record persisted before anything else exists, so every later step is
// re-drivable against the same name.
func (r *Runtime) StartSweep(space id.TerminalID, parent [16]byte, taskID []byte, label string) (SweepInfo, error) {
	if label == "" {
		label = "Sweep"
	}
	var sid [16]byte
	if _, err := rand.Read(sid[:]); err != nil {
		return SweepInfo{}, err
	}
	rec := storage.SweepRecord{
		Space: space, SweepID: sid, ParentID: parent, TaskID: taskID,
		Label: label, StartedAt: uint64(time.Now().Unix()),
		State: storage.SweepStarting,
	}
	r.mu.Lock()
	if _, ok := r.spaces[space]; !ok {
		r.mu.Unlock()
		return SweepInfo{}, errors.New("node: unknown space")
	}
	for _, sw := range r.ks.Sweeps {
		if sw.Space == space && sw.State < storage.SweepStopped {
			r.mu.Unlock()
			return SweepInfo{}, errors.New("node: a sweep is already running in this space — stop it first")
		}
	}
	r.ks.Sweeps = append(r.ks.Sweeps, rec)
	if err := r.saveKeystore(); err != nil {
		r.mu.Unlock()
		return SweepInfo{}, err
	}
	r.mu.Unlock()
	if err := r.driveSweepStart(sid); err != nil {
		return SweepInfo{}, err
	}
	rt := r.sweepByID(sid)
	if rt == nil {
		return SweepInfo{}, errSweepGone
	}
	return r.sweepInfo(rt), nil
}

// driveSweepStart finishes (or re-finishes) the start saga for a
// record in SweepStarting: spool, Object, state=recording, ticker.
func (r *Runtime) driveSweepStart(sid [16]byte) error {
	rec, ok := r.sweepRecord(sid)
	if !ok {
		return errSweepGone
	}
	spool, err := openSweepSpool(sweepSpoolDir(r.dataDir, sid))
	if err != nil {
		// A spool that cannot exist refuses the whole start: recording
		// with nowhere durable to record would be a promise with no
		// floor under it.
		r.dropSweepRecord(sid)
		return fmt.Errorf("node: sweep spool: %w", err)
	}
	if !rec.ObjectCreated {
		parent := rec.ParentID
		obj := &objects.Record{
			ObjectID: sid, Kind: "sweep", Name: rec.Label, Status: "recording",
		}
		if parent != ([16]byte{}) {
			obj.Parent = &parent
		}
		if _, _, err := r.CreateObject(rec.Space, obj); err != nil {
			// "already exists" is the saga re-driving past a crash that
			// happened after the emit — the object is ours.
			if !containsStr(err.Error(), "already exists") {
				spool.Close()
				return err
			}
		}
		r.updateSweepRecord(sid, func(sw *storage.SweepRecord) {
			sw.ObjectCreated = true
		})
	}
	r.updateSweepRecord(sid, func(sw *storage.SweepRecord) {
		if sw.State == storage.SweepStarting {
			sw.State = storage.SweepRecording
		}
	})
	rec, _ = r.sweepRecord(sid)
	rt := &sweepRuntime{rec: rec, spool: spool, stop: make(chan struct{})}
	r.mu.Lock()
	if r.sweeps == nil {
		r.sweeps = map[[16]byte]*sweepRuntime{}
	}
	r.sweeps[sid] = rt
	r.mu.Unlock()
	r.startSweepTicker(rt)
	return nil
}

// AppendSweepSamples spools one host batch. Idempotent by seq.
func (r *Runtime) AppendSweepSamples(sid [16]byte, seq uint64, samples []SpoolSample) (SweepInfo, error) {
	rt := r.sweepByID(sid)
	if rt == nil {
		return SweepInfo{}, errSweepGone
	}
	rec, _ := r.sweepRecord(sid)
	if rec.State >= storage.SweepStopping {
		return SweepInfo{}, ErrSweepClosed
	}
	if _, err := rt.spool.AppendBatch(seq, samples); err != nil {
		return SweepInfo{}, err
	}
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].Tag == field.SampleQPoint {
			rt.fixMu.Lock()
			rt.lastFix.pt = geo.Point{LatE7U: samples[i].LatE7U, LonE7U: samples[i].LonE7U}
			rt.lastFix.acc = samples[i].AccuracyM
			rt.lastFix.unixMS = samples[i].UnixMS
			rt.fixMu.Unlock()
			break
		}
	}
	return r.sweepInfo(rt), nil
}

// ResumeSweep is the sticky-restart claim: the service came back and
// the same session continues, wearing an honest suspended gap — the
// node's OWN lifecycle, the one cause it may state without inventing.
func (r *Runtime) ResumeSweep(sid [16]byte) error {
	rt := r.sweepByID(sid)
	if rt == nil {
		return errSweepGone
	}
	rec, _ := r.sweepRecord(sid)
	if rec.State != storage.SweepSuspended && rec.State != storage.SweepRecording {
		return ErrSweepClosed
	}
	if rec.State == storage.SweepSuspended {
		last := rt.spool.LastSampleUnixMS()
		if last == 0 {
			last = rec.StartedAt * 1000
		}
		now := uint64(time.Now().UnixMilli())
		if now > last {
			if err := rt.spool.AppendNodeGap(last, now-last, field.GapSuspended); err != nil {
				return err
			}
		}
		r.updateSweepRecord(sid, func(sw *storage.SweepRecord) {
			sw.State = storage.SweepRecording
		})
	}
	return nil
}

// StopSweep closes capture NOW — the law is about capture: the ticker
// stops, samples refuse — and then finalizes. An empty result becomes
// `undeclared`: the fact of completion travels at once, the judgement
// arrives later as an observation if at all.
func (r *Runtime) StopSweep(sid [16]byte, result, note string) error {
	rt := r.sweepByID(sid)
	if rt == nil {
		return errSweepGone
	}
	rec, _ := r.sweepRecord(sid)
	if rec.State >= storage.SweepStopped {
		return ErrSweepClosed
	}
	if result == "" {
		result = schemas.SweepUndeclared
	}
	switch result {
	case schemas.SweepNothingFound, schemas.SweepFound, schemas.SweepUndeclared, schemas.SweepInterrupted:
	default:
		return errors.New("node: sweep result not in the vocabulary")
	}
	r.updateSweepRecord(sid, func(sw *storage.SweepRecord) {
		if sw.State < storage.SweepStopping {
			sw.State = storage.SweepStopping
			sw.StoppedAt = uint64(time.Now().Unix())
		}
		sw.Result = result
		sw.Note = note
		sw.State = storage.SweepStopped
	})
	r.stopSweepTicker(sid)
	return r.finalizeSweep(sid)
}

// ActiveSweeps lists live sessions for the UI banner and the Android
// service's restart discovery — the node's keystore is the truth, the
// host holds only intent.
func (r *Runtime) ActiveSweeps() []SweepInfo {
	r.mu.Lock()
	recs := append([]storage.SweepRecord(nil), r.ks.Sweeps...)
	r.mu.Unlock()
	var out []SweepInfo
	for _, rec := range recs {
		if rec.State >= storage.SweepStopped {
			continue
		}
		if rt := r.sweepByID(rec.SweepID); rt != nil {
			out = append(out, r.sweepInfo(rt))
		}
	}
	return out
}

// ---- restore (the hybrid orphan policy) ----

func (r *Runtime) restoreSweeps() {
	r.mu.Lock()
	recs := append([]storage.SweepRecord(nil), r.ks.Sweeps...)
	r.mu.Unlock()
	for _, rec := range recs {
		switch rec.State {
		case storage.SweepStarting:
			// The start saga died mid-flight: re-drive it. The record is
			// the truth; the Object may or may not exist yet.
			_ = r.driveSweepStart(rec.SweepID)
		case storage.SweepRecording, storage.SweepSuspended:
			// A live session survived a restart. Suspend it and give the
			// service's resume claim a grace window; no claim → the
			// owner's hybrid policy finalizes as interrupted with the
			// spooled track preserved.
			spool, err := openSweepSpool(sweepSpoolDir(r.dataDir, rec.SweepID))
			if err != nil {
				continue
			}
			r.updateSweepRecord(rec.SweepID, func(sw *storage.SweepRecord) {
				sw.State = storage.SweepSuspended
			})
			rec2, _ := r.sweepRecord(rec.SweepID)
			rt := &sweepRuntime{rec: rec2, spool: spool, stop: make(chan struct{})}
			r.mu.Lock()
			if r.sweeps == nil {
				r.sweeps = map[[16]byte]*sweepRuntime{}
			}
			r.sweeps[rec.SweepID] = rt
			r.mu.Unlock()
			r.armSweepOrphanGrace(rec.SweepID)
		case storage.SweepStopping, storage.SweepStopped:
			// Capture had closed; finish the job. Stopping without a
			// declared result finalizes as interrupted — recording
			// ended, meaning never given.
			spool, err := openSweepSpool(sweepSpoolDir(r.dataDir, rec.SweepID))
			if err != nil {
				continue
			}
			rt := &sweepRuntime{rec: rec, spool: spool, stop: make(chan struct{})}
			r.mu.Lock()
			if r.sweeps == nil {
				r.sweeps = map[[16]byte]*sweepRuntime{}
			}
			r.sweeps[rec.SweepID] = rt
			r.mu.Unlock()
			r.updateSweepRecord(rec.SweepID, func(sw *storage.SweepRecord) {
				if sw.Result == "" {
					sw.Result = schemas.SweepInterrupted
				}
				if sw.StoppedAt == 0 {
					sw.StoppedAt = uint64(time.Now().Unix())
				}
				sw.State = storage.SweepStopped
			})
			_ = r.finalizeSweep(rec.SweepID)
		}
	}
}

func (r *Runtime) armSweepOrphanGrace(sid [16]byte) {
	rt := r.sweepByID(sid)
	if rt == nil {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		select {
		case <-r.stop:
			return
		case <-rt.stop:
			return
		case <-time.After(sweepResumeGrace):
		}
		rec, ok := r.sweepRecord(sid)
		if !ok || rec.State != storage.SweepSuspended {
			return // resumed, or already finalized
		}
		r.updateSweepRecord(sid, func(sw *storage.SweepRecord) {
			sw.Result = schemas.SweepInterrupted
			sw.StoppedAt = uint64(time.Now().Unix())
			sw.State = storage.SweepStopped
		})
		r.stopSweepTicker(sid)
		_ = r.finalizeSweep(sid)
	}()
}

// ---- the position ticker ----

func (r *Runtime) startSweepTicker(rt *sweepRuntime) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(sweepPositionEvery)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-rt.stop:
				return
			case <-t.C:
				rt.fixMu.Lock()
				fix := rt.lastFix
				rt.fixMu.Unlock()
				fixAge := time.Duration(uint64(time.Now().UnixMilli())-fix.unixMS) * time.Millisecond
				if fix.unixMS == 0 || fixAge > sweepFixFresh {
					continue // an old fix re-stamped fresh would lie on the map
				}
				_ = r.SetPosition(rt.rec.Space, fix.pt, fix.acc, sweepPositionTTL)
			}
		}
	}()
}

func (r *Runtime) stopSweepTicker(sid [16]byte) {
	r.mu.Lock()
	rt := r.sweeps[sid]
	r.mu.Unlock()
	if rt == nil {
		return
	}
	// Unlock before close — the instrument.go ordering; double-close is
	// prevented by the select probe.
	select {
	case <-rt.stop:
	default:
		close(rt.stop)
	}
}

// ---- the finalize saga ----

func (r *Runtime) finalizeSweep(sid [16]byte) error {
	rt := r.sweepByID(sid)
	if rt == nil {
		return errSweepGone
	}
	rec, _ := r.sweepRecord(sid)
	if rec.State == storage.SweepDone {
		return nil
	}
	space := rec.Space

	// 1+2: seal the spool into field.track.v1 and ingest it.
	if rec.AssetHex == "" {
		track, distance, bmin, bmax := sealTrack(rec.StartedAt, rt.spool.Samples())
		enc, err := track.Encode()
		if err != nil {
			return err
		}
		ref, err := r.IngestAsset(bytes.NewReader(enc), int64(len(enc)),
			assets.Metadata{MediaType: field.MediaType, Role: "original"})
		if err != nil {
			return err
		}
		r.RideAhead(space, ref)
		payload, err := (&schemas.AttachedBlock{
			Filename: rec.Label + ".track", MediaType: field.MediaType, Original: ref,
		}).Encode()
		if err != nil {
			r.DisarmRideAhead(space, ref)
			return err
		}
		// 4: the carrier — MANDATORY: the asset index is gated on the
		// block. prefix; the completion event alone would leave every
		// replica blind to the asset.
		beid, err := r.EmitBlock(space, schemas.BlockAttached, payload)
		if err != nil {
			r.DisarmRideAhead(space, ref)
			return err
		}
		r.updateSweepRecord(sid, func(sw *storage.SweepRecord) {
			sw.AssetHex = ref.PublicIDHex()
			sw.BlockEventID = beid[:]
		})
		rec, _ = r.sweepRecord(sid)
		rt.sealed = &sealedTrack{distance: distance, bmin: bmin, bmax: bmax}
	}
	if rt.sealed == nil {
		// Re-driven after a crash: recompute the geometry from the spool
		// (deterministic — same samples, same numbers).
		_, distance, bmin, bmax := sealTrack(rec.StartedAt, rt.spool.Samples())
		rt.sealed = &sealedTrack{distance: distance, bmin: bmin, bmax: bmax}
	}

	// 5: the object→asset edge, role "track".
	if len(rec.EdgeEventID) == 0 {
		eeid, err := r.EmitAssetEdge(space, &objects.AttachPayload{
			Fallback: "track · " + rec.Label,
			ObjectID: rec.SweepID, Asset: rec.AssetHex, Role: "track", Label: rec.Label + ".track",
		})
		if err != nil {
			return err
		}
		r.updateSweepRecord(sid, func(sw *storage.SweepRecord) { sw.EdgeEventID = eeid[:] })
		rec, _ = r.sweepRecord(sid)
	}

	// 6: the canonical completion fact.
	if len(rec.CompletedEventID) == 0 {
		var asset [32]byte
		ah, err := hex.DecodeString(rec.AssetHex)
		if err != nil || len(ah) != 32 {
			return errors.New("node: sweep asset id is not 32 bytes")
		}
		copy(asset[:], ah)
		stopped := rec.StoppedAt
		if stopped == 0 {
			stopped = uint64(time.Now().Unix())
		}
		sw := &schemas.CompletedSweep{
			Fallback:  sweepFallback(rec.Result, rt.sealed.distance, rec.StartedAt, stopped),
			ObjectID:  rec.SweepID,
			StartedAt: rec.StartedAt, EndedAt: stopped,
			DistanceM: rt.sealed.distance, Result: rec.Result,
			BBoxMin: rt.sealed.bmin, BBoxMax: rt.sealed.bmax,
			TrackAsset: asset,
		}
		payload, err := sw.Encode()
		if err != nil {
			return err
		}
		ceid, err := r.emitCompleted(space, payload)
		if err != nil {
			return err
		}
		r.updateSweepRecord(sid, func(s *storage.SweepRecord) { s.CompletedEventID = ceid[:] })
		rec, _ = r.sweepRecord(sid)
	}

	// 7: the linked task — but NEVER for interrupted: an interrupted
	// sweep did not do the work, and a ✓ on the card would be a lie.
	if !rec.CardDone && len(rec.TaskID) == len(id.EventID{}) && rec.Result != schemas.SweepInterrupted {
		var card id.EventID
		copy(card[:], rec.TaskID)
		if err := r.SetCardStatus(space, card, "", "done"); err == nil {
			r.updateSweepRecord(sid, func(s *storage.SweepRecord) { s.CardDone = true })
			rec, _ = r.sweepRecord(sid)
		}
	}

	// 8: the operator's prose, on the PARENT sector — human words travel
	// as an observation, never inside the completion fact.
	if len(rec.NoteEventID) == 0 && rec.Note != "" {
		parent := rec.ParentID
		neid, err := r.NoteObservation(space, rec.Note, &parent, rec.StoppedAt)
		if err == nil {
			r.updateSweepRecord(sid, func(s *storage.SweepRecord) { s.NoteEventID = neid[:] })
			rec, _ = r.sweepRecord(sid)
		}
	}

	// 9: the status cache on the Object — it must say what the canon
	// says (completed | interrupted), or the generic renderer would
	// contradict the event it projects.
	if !rec.ObjectRevised {
		if err := r.reviseSweepObjectStatus(space, rec); err == nil {
			r.updateSweepRecord(sid, func(s *storage.SweepRecord) { s.ObjectRevised = true })
		}
	}

	// 10: done. The spool's truth now lives in the asset.
	r.updateSweepRecord(sid, func(s *storage.SweepRecord) { s.State = storage.SweepDone })
	r.stopSweepTicker(sid)
	rt.spool.Close()
	_ = os.RemoveAll(sweepSpoolDir(r.dataDir, sid))
	r.mu.Lock()
	delete(r.sweeps, sid)
	// The finished record leaves the keystore: the Object and the event
	// ARE the history now.
	kept := r.ks.Sweeps[:0]
	for _, s := range r.ks.Sweeps {
		if s.SweepID != sid {
			kept = append(kept, s)
		}
	}
	r.ks.Sweeps = kept
	err := r.saveKeystore()
	r.mu.Unlock()
	r.kickRelaySync()
	return err
}

type sealedTrack struct {
	distance   uint64
	bmin, bmax geo.Point
}

// sealTrack converts the absolute-clock spool into field.track.v1 and
// computes the geometry. Distance is haversine WITHIN segments — never
// across a gap: the system does not measure a line it refused to draw.
func sealTrack(startedAt uint64, spool []SpoolSample) (*field.Track, uint64, geo.Point, geo.Point) {
	tr := &field.Track{StartedAt: startedAt}
	prevClock := startedAt * 1000
	var distance float64
	var havePrevPt bool
	var prevPt geo.Point
	var haveBBox bool
	var bmin, bmax geo.Point
	for _, s := range spool {
		dt := uint64(0)
		if s.UnixMS > prevClock {
			dt = s.UnixMS - prevClock
		}
		prevClock = s.UnixMS
		switch s.Tag {
		case field.SampleQPoint:
			pt := geo.Point{LatE7U: s.LatE7U, LonE7U: s.LonE7U}
			tr.Samples = append(tr.Samples, field.Sample{
				Tag: field.SampleQPoint, DtMS: dt, Point: pt, AccuracyM: s.AccuracyM,
			})
			if havePrevPt {
				distance += haversineM(prevPt, pt)
			}
			prevPt, havePrevPt = pt, true
			if !haveBBox {
				bmin, bmax, haveBBox = pt, pt, true
			} else {
				if pt.LatE7U < bmin.LatE7U {
					bmin.LatE7U = pt.LatE7U
				}
				if pt.LonE7U < bmin.LonE7U {
					bmin.LonE7U = pt.LonE7U
				}
				if pt.LatE7U > bmax.LatE7U {
					bmax.LatE7U = pt.LatE7U
				}
				if pt.LonE7U > bmax.LonE7U {
					bmax.LonE7U = pt.LonE7U
				}
			}
		case field.SampleQGap:
			tr.Samples = append(tr.Samples, field.Sample{
				Tag: field.SampleQGap, DtMS: dt, DurationMS: s.DurationMS, Reason: s.Reason,
			})
			prevClock = s.UnixMS + s.DurationMS
			havePrevPt = false // the next point starts a NEW segment
		}
	}
	return tr, uint64(distance), bmin, bmax
}

func haversineM(a, b geo.Point) float64 {
	const R = 6_371_000.0
	rad := math.Pi / 180
	la1, lo1 := a.LatDeg()*rad, a.LonDeg()*rad
	la2, lo2 := b.LatDeg()*rad, b.LonDeg()*rad
	dla, dlo := la2-la1, lo2-lo1
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * R * math.Asin(math.Sqrt(h))
}

// sweepFallback composes the sentence an old client renders. The wire
// never parses it; consumers key on the result slug.
func sweepFallback(result string, distanceM, startedAt, endedAt uint64) string {
	mins := (endedAt - startedAt) / 60
	km := float64(distanceM) / 1000
	switch result {
	case schemas.SweepInterrupted:
		return fmt.Sprintf("⚠ sweep interrupted · %.1f km · %d min", km, mins)
	default:
		return fmt.Sprintf("✓ sweep · %.1f km · %d min", km, mins)
	}
}

func (r *Runtime) emitCompleted(space id.TerminalID, payload []byte) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[space]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, schemas.SweepCompleted, payload)
}

func (r *Runtime) reviseSweepObjectStatus(space id.TerminalID, rec storage.SweepRecord) error {
	status := "completed"
	if rec.Result == schemas.SweepInterrupted {
		status = "interrupted"
	}
	var cur *objects.Record
	var tip id.EventID
	err := r.withSpace(space, func(st *spaceState) error {
		o, ok := st.space.State.ObjectByID(rec.SweepID)
		if !ok {
			return errors.New("node: sweep object missing")
		}
		// Copy: a revision must not write through the projection's pointer.
		c := *o.Record
		cur = &c
		tip = o.RevisionEventID
		return nil
	})
	if err != nil {
		return err
	}
	cur.Status = status
	_, err = r.ReviseObject(space, cur, &tip)
	return err
}

// ---- small helpers ----

func (r *Runtime) sweepByID(sid [16]byte) *sweepRuntime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sweeps[sid]
}

func (r *Runtime) sweepRecord(sid [16]byte) (storage.SweepRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.ks.Sweeps {
		if s.SweepID == sid {
			return s, true
		}
	}
	return storage.SweepRecord{}, false
}

func (r *Runtime) updateSweepRecord(sid [16]byte, mut func(*storage.SweepRecord)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.ks.Sweeps {
		if r.ks.Sweeps[i].SweepID == sid {
			mut(&r.ks.Sweeps[i])
			_ = r.saveKeystore()
			return
		}
	}
}

func (r *Runtime) dropSweepRecord(sid [16]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.ks.Sweeps[:0]
	for _, s := range r.ks.Sweeps {
		if s.SweepID != sid {
			kept = append(kept, s)
		}
	}
	r.ks.Sweeps = kept
	_ = r.saveKeystore()
}

func (r *Runtime) sweepInfo(rt *sweepRuntime) SweepInfo {
	rec, _ := r.sweepRecord(rt.rec.SweepID)
	samples := rt.spool.Samples()
	_, distance, _, _ := sealTrack(rec.StartedAt, samples)
	return SweepInfo{
		SweepID: rec.SweepID, Space: rec.Space, ParentID: rec.ParentID,
		Label: rec.Label, State: rec.State, StartedAt: rec.StartedAt,
		Samples: len(samples), DistanceM: distance,
	}
}

func containsStr(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && bytes.Contains([]byte(haystack), []byte(needle))
}
