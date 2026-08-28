// Domain objects at the node (SP-1): create/revise with optimistic
// concurrency, archive/restore, and human observations. Every emit path
// here sits behind canWrite — the friendly refusal; the authoritative
// gates (ReadOnly emit gate, curated log admission) still hold below.
package node

import (
	"crypto/rand"
	"errors"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// ErrObjectConflict marks a stale base revision (optimistic concurrency) —
// the publications rule, word for word.
var ErrObjectConflict = errors.New("node: the object changed elsewhere — revise from the latest revision")

// CreateObject mints an id when the record carries none and emits the
// first revision. The record arrives WHOLE — props are the full set, an
// omitted prop is a deleted prop (that is the revision contract, stated
// at the API so no client invents a merge).
func (r *Runtime) CreateObject(tid id.TerminalID, rec *objects.Record) ([16]byte, id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return [16]byte{}, id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return [16]byte{}, id.EventID{}, err
	}
	if rec.ObjectID == ([16]byte{}) {
		if _, err := rand.Read(rec.ObjectID[:]); err != nil {
			return [16]byte{}, id.EventID{}, err
		}
	}
	if _, exists := st.space.State.ObjectByID(rec.ObjectID); exists {
		return [16]byte{}, id.EventID{}, errors.New("node: object already exists — revise it instead")
	}
	enc, err := rec.Encode()
	if err != nil {
		return [16]byte{}, id.EventID{}, err
	}
	payload := (&objects.RevisionPayload{Fallback: rec.Name, Record: enc}).Encode()
	eid, err := r.emitLocked(st, objects.SchemaCreated, payload)
	if err != nil {
		return [16]byte{}, id.EventID{}, err
	}
	return rec.ObjectID, eid, nil
}

// ReviseObject emits a full-record revision. baseRevision names the
// revision the author edited; a mismatch with the projection tip is a
// conflict (409 at the API), never a silent overwrite.
func (r *Runtime) ReviseObject(tid id.TerminalID, rec *objects.Record, baseRevision *id.EventID) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	cur, exists := st.space.State.ObjectByID(rec.ObjectID)
	if !exists {
		return id.EventID{}, errors.New("node: unknown object")
	}
	tip := cur.RevisionEventID
	if baseRevision == nil || *baseRevision != tip {
		return id.EventID{}, ErrObjectConflict
	}
	enc, err := rec.Encode()
	if err != nil {
		return id.EventID{}, err
	}
	payload := (&objects.RevisionPayload{
		Fallback: rec.Name, Record: enc, BaseRevision: baseRevision, PrevRevision: &tip,
	}).Encode()
	return r.emitLocked(st, objects.SchemaRevised, payload)
}

// ArchiveObject emits the archive event and returns its id — a restore
// must reference exactly this event.
func (r *Runtime) ArchiveObject(tid id.TerminalID, objectID [16]byte) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	obj, ok := st.space.State.ObjectByID(objectID)
	if !ok {
		return id.EventID{}, errors.New("node: unknown object")
	}
	payload := (&objects.LifecyclePayload{Fallback: obj.Name, ObjectID: objectID}).Encode()
	return r.emitLocked(st, objects.SchemaArchived, payload)
}

// RestoreObject lifts an archive by explicit reference.
func (r *Runtime) RestoreObject(tid id.TerminalID, objectID [16]byte, archiveEvent id.EventID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return err
	}
	payload := (&objects.LifecyclePayload{
		Fallback: "restored", ObjectID: objectID, ArchivedRevision: &archiveEvent,
	}).Encode()
	_, err := r.emitLocked(st, objects.SchemaRestored, payload)
	return err
}

// EmitAssetEdge emits the FULL state of one object→asset edge
// (object.attached.v1). Callers hand the complete intended edge — the
// wire is a whole-state LWW register, so "update one field" is the
// API layer's read-modify-write job, not the runtime's.
//
// Two refusals live here, before anything is signed (the resolveLineage
// discipline — checked bytes are signed bytes):
//   - the asset must be indexed in THIS space (spaceAssetOK): an edge to
//     an id no peer can serve would be a beautiful card over a dead file;
//   - a NEW asset is refused once the object holds
//     MaxAssetEdgesPerObject live registers. An authoring bound only —
//     the reducer never evicts (see protocol/objects/edges.go).
func (r *Runtime) EmitAssetEdge(tid id.TerminalID, edge *objects.AttachPayload) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	if _, exists := st.space.State.ObjectByID(edge.ObjectID); !exists {
		return id.EventID{}, errors.New("node: unknown object")
	}
	if !r.spaceAssetOK(tid)(edge.Asset) {
		return id.EventID{}, errors.New("node: asset is not in this space — upload it first")
	}
	if edge.Supersedes != "" && !r.spaceAssetOK(tid)(edge.Supersedes) {
		return id.EventID{}, errors.New("node: superseded asset is not in this space")
	}
	edges := st.space.State.EdgesForObject(edge.ObjectID)
	var known bool
	live := 0
	for _, e := range edges {
		if e.Asset == edge.Asset {
			known = true
		}
		if !e.Detached {
			live++
		}
	}
	if !known && live >= objects.MaxAssetEdgesPerObject {
		return id.EventID{}, errors.New("node: this object already holds the maximum number of attached assets")
	}
	payload, err := edge.Encode()
	if err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, objects.SchemaAttached, payload)
}

// AnnotateAsset records a media annotation. The asset must be indexed in
// this space, but the annotation never pins it — commentary is not a
// structural reference (ADR-030).
func (r *Runtime) AnnotateAsset(tid id.TerminalID, assetHex, text string, positionMs uint64, hasPosition bool, objectID *[16]byte) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	if !r.spaceAssetOK(tid)(assetHex) {
		return id.EventID{}, errors.New("node: asset is not in this space")
	}
	a := &schemas.AssetAnnotation{Text: text, Asset: assetHex, ObjectID: objectID}
	if hasPosition {
		a.SetPosition(positionMs)
	}
	if _, err := rand.Read(a.AnnotationID[:]); err != nil {
		return id.EventID{}, err
	}
	payload, err := a.Encode()
	if err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, schemas.AssetAnnotated, payload)
}

// NoteObservation records a human observation — on an object's timeline
// when objectID is set, in the space journal otherwise. The same event is
// also a feed entry (the reducer's two projections of one truth).
func (r *Runtime) NoteObservation(tid id.TerminalID, text string, objectID *[16]byte, observedAt uint64) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		return id.EventID{}, err
	}
	payload, err := (&schemas.NotedObservation{Text: text, ObjectID: objectID, ObservedAt: observedAt}).Encode()
	if err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, schemas.ObservationNoted, payload)
}
