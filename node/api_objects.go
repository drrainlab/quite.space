// Objects API (SP-1). The revision contract, stated once and out loud:
// a revision carries the FULL record — props are the complete new set and
// an omitted prop is a DELETED prop, never a merge. base_revision_event_id
// is optimistic concurrency: stale base → 409, the publications rule.
package node

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
)

type propJSON struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type objectRecordJSON struct {
	ObjectID string     `json:"object_id,omitempty"`
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	Status   string     `json:"status,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	Props    []propJSON `json:"props,omitempty"`
	Cover    string     `json:"cover,omitempty"`
}

func recordFromJSON(j objectRecordJSON) (*objects.Record, error) {
	r := &objects.Record{
		Kind: strings.TrimSpace(j.Kind), Name: strings.TrimSpace(j.Name),
		Status: strings.TrimSpace(j.Status), Summary: j.Summary, Cover: j.Cover,
	}
	if j.ObjectID != "" {
		b, err := hex.DecodeString(j.ObjectID)
		if err != nil || len(b) != 16 {
			return nil, errors.New("bad object id")
		}
		copy(r.ObjectID[:], b)
	}
	for _, p := range j.Props {
		r.Props = append(r.Props, objects.Prop{Key: p.Key, Value: p.Value})
	}
	// The wire wants sorted-unique props; sorting FOR the client here would
	// silently accept duplicates, so only sort order is normalized upstream
	// by the UI — the codec's Validate stays the judge.
	return r, nil
}

func (a *APIServer) oidParam(r *http.Request) ([16]byte, error) {
	var out [16]byte
	b, err := hex.DecodeString(r.PathValue("oid"))
	if err != nil || len(b) != 16 {
		return out, errors.New("bad object id")
	}
	copy(out[:], b)
	return out, nil
}

func objectListJSON(o reducers.Object, tasks []reducers.Card) map[string]any {
	open, done := 0, 0
	for _, t := range tasks {
		switch t.Status {
		case "open":
			open++
		case "done":
			done++
		}
	}
	j := map[string]any{
		"object_id":         hex.EncodeToString(o.ObjectID[:]),
		"kind":              o.Record.Kind,
		"name":              o.Record.Name,
		"revision_event_id": o.RevisionEventID.Hex(),
		"created_at":        o.CreatedAt,
		"clock":             o.Clock,
		"task_open":         open,
		"task_done":         done,
		// The stable keep/react target — derived from the id, so it never
		// moves under a revision.
		"target": objects.Target(o.ObjectID).Hex(),
	}
	if o.Record.Status != "" {
		j["status"] = o.Record.Status
	}
	if o.Record.Summary != "" {
		j["summary"] = o.Record.Summary
	}
	if o.Record.Cover != "" {
		j["cover"] = o.Record.Cover
	}
	if o.Archived {
		j["archived"] = true
		// What a restore must reference — carried so the UI can offer one.
		j["archive_event_id"] = o.ArchiveEventID.Hex()
	}
	return j
}

func observationJSON(n reducers.ObservationNote) map[string]any {
	observed := n.ObservedAt
	if observed == 0 {
		observed = n.CreatedAt
	}
	j := map[string]any{
		"id":          n.EventID.Hex(),
		"author":      n.Author.Hex(),
		"text":        n.Text,
		"observed_at": observed,
		"created_at":  n.CreatedAt,
		"clock":       n.Clock,
	}
	if n.ObjectID != nil {
		j["object_id"] = hex.EncodeToString(n.ObjectID[:])
	}
	return j
}

func cardJSON(c reducers.Card) map[string]any {
	j := map[string]any{
		"id": c.ID.Hex(), "title": c.Title, "status": c.Status, "clock": c.Clock,
	}
	if c.Assignee != nil {
		j["assignee"] = c.Assignee.Hex()
	}
	if c.Origin != nil {
		j["origin"] = c.Origin.Hex()
	}
	if c.ObjectID != nil {
		j["object_id"] = hex.EncodeToString(c.ObjectID[:])
	}
	return j
}

func (a *APIServer) handleListObjects(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	archived := r.URL.Query().Get("archived") == "1"
	out := []map[string]any{}
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		list := st.space.State.Objects()
		if archived {
			list = st.space.State.ArchivedObjects()
		}
		for _, o := range list {
			out = append(out, objectListJSON(o, st.space.State.TasksForObject(o.ObjectID)))
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"objects": out})
}

func (a *APIServer) handleGetObject(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	oid, err := a.oidParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var j map[string]any
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		o, ok := st.space.State.ObjectByID(oid)
		if !ok {
			return errors.New("unknown object")
		}
		objTasks := st.space.State.TasksForObject(oid)
		j = objectListJSON(o, objTasks)
		// The stable target's social state: kept-by-me, keep count, and the
		// reaction aggregate — same viewer-relative projection the feed uses.
		me := a.rt.PrincipalID
		target := objects.Target(oid)
		if kept, _ := st.space.State.KeepState(target, me); kept {
			j["kept"] = true
		}
		if n := st.space.State.KeepCount(target); n > 0 {
			j["keep_count"] = n
		}
		names := map[id.PrincipalID]string{me: a.rt.displayNameLocked()}
		for _, c := range st.space.MemberCards(0) {
			if c.Name != "" {
				names[c.Principal] = c.Name
			}
		}
		if res := a.projectResonance(st.space, target, me, names); res != nil && res.Total > 0 {
			j["resonance"] = res
		}
		props := []propJSON{}
		for _, p := range o.Record.Props {
			props = append(props, propJSON{Key: p.Key, Value: p.Value})
		}
		j["props"] = props
		tasks := []map[string]any{}
		for _, c := range objTasks {
			tasks = append(tasks, cardJSON(c))
		}
		j["tasks"] = tasks
		obs := []map[string]any{}
		for _, n := range o.Observations {
			obs = append(obs, observationJSON(n))
		}
		j["observations"] = obs
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, j)
}

func (a *APIServer) handleCreateObject(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Object objectRecordJSON `json:"object"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	rec, err := recordFromJSON(body.Object)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	oid, eid, err := a.rt.CreateObject(tid, rec)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{
		"object_id":         hex.EncodeToString(oid[:]),
		"revision_event_id": eid.Hex(),
	})
}

func (a *APIServer) handleReviseObject(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	oid, err := a.oidParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Object objectRecordJSON `json:"object"`
		Base   string           `json:"base_revision_event_id"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	rec, err := recordFromJSON(body.Object)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	rec.ObjectID = oid // the path names the object; the body cannot retarget it
	var base *id.EventID
	if body.Base != "" {
		b, err := hex.DecodeString(body.Base)
		if err != nil || len(b) != 32 {
			httpErr(w, http.StatusBadRequest, errors.New("bad base revision id"))
			return
		}
		var e id.EventID
		copy(e[:], b)
		base = &e
	}
	eid, err := a.rt.ReviseObject(tid, rec, base)
	if errors.Is(err, ErrObjectConflict) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "conflict": true})
		return
	}
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"revision_event_id": eid.Hex()})
}

func (a *APIServer) handleArchiveObject(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	oid, err := a.oidParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	archEvent, err := a.rt.ArchiveObject(tid, oid)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"status": "archived", "archive_event_id": archEvent.Hex()})
}

func (a *APIServer) handleRestoreObject(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	oid, err := a.oidParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		ArchiveEventID string `json:"archive_event_id"`
	}](r)
	if err != nil || body.ArchiveEventID == "" {
		httpErr(w, http.StatusBadRequest, errors.New("archive_event_id required"))
		return
	}
	b, err := hex.DecodeString(body.ArchiveEventID)
	if err != nil || len(b) != 32 {
		httpErr(w, http.StatusBadRequest, errors.New("bad archive event id"))
		return
	}
	var arch id.EventID
	copy(arch[:], b)
	if err := a.rt.RestoreObject(tid, oid, arch); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"status": "restored"})
}

func (a *APIServer) handleNoteObservation(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	oid, err := a.oidParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Text       string `json:"text"`
		ObservedAt uint64 `json:"observed_at"`
	}](r)
	if err != nil || strings.TrimSpace(body.Text) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("text required"))
		return
	}
	eid, err := a.rt.NoteObservation(tid, strings.TrimSpace(body.Text), &oid, body.ObservedAt)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

// handleListCards is the SP-1 full card listing: ?object= and ?status=
// filters, the full form out (id/title/status/assignee/origin/object_id/
// clock) — /state's compact card list stays as it was.
func (a *APIServer) handleListCards(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	q := r.URL.Query()
	var objFilter *[16]byte
	if v := q.Get("object"); v != "" {
		b, err := hex.DecodeString(v)
		if err != nil || len(b) != 16 {
			httpErr(w, http.StatusBadRequest, errors.New("bad object id"))
			return
		}
		var o [16]byte
		copy(o[:], b)
		objFilter = &o
	}
	statusFilter := q.Get("status")
	out := []map[string]any{}
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		for _, c := range st.space.State.Cards() {
			if objFilter != nil && (c.ObjectID == nil || *c.ObjectID != *objFilter) {
				continue
			}
			if statusFilter != "" && c.Status != statusFilter {
				continue
			}
			out = append(out, cardJSON(c))
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"cards": out})
}
