// App runtime enforcement (ADR-014, APP-0). The node is the ONLY path an app
// has to the log: inputs are served per instance with the definition's scope
// filter applied server-side, and actions are the only emit path — checked
// against effective = requested ∩ granted ∩ node policy, with instance_id
// injected by the node, never trusted from the client.
package node

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/appdef"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// nodePolicy is the schema allowlist this node will serve/emit for apps.
var appPolicyRead = appdef.NewCapSet([]appdef.Capability{{
	Kind: "read", Schemas: []string{appdef.SchemaPollVote, appdef.SchemaFormResponse},
}})
var appPolicyAppend = appdef.NewCapSet([]appdef.Capability{{
	Kind: "append", Schemas: []string{appdef.SchemaPollVote, appdef.SchemaFormResponse},
}})

// Built-in trusted templates (fixed grants; pinned by the binary version).
var builtinApps = map[string]*appdef.Definition{
	"poll": {
		AppID: "poll", Name: "Poll",
		Inputs: map[string]appdef.Input{
			"votes": {Schema: appdef.SchemaPollVote,
				Where: &appdef.Where{Field: "instance_id", Value: "$instance.id"}, Limit: 1000},
		},
		State: map[string]appdef.Reducer{
			"votes": {Kind: "list", Input: "votes"},
		},
		Actions: map[string]appdef.Action{
			"vote": {Emit: appdef.SchemaPollVote, Fields: []string{"option"}},
		},
		Requested: []appdef.Capability{
			{Kind: "read", Schemas: []string{appdef.SchemaPollVote}},
			{Kind: "append", Schemas: []string{appdef.SchemaPollVote}},
		},
		Fallback: "A poll (open it on a richer device to vote).",
	},
	"form": {
		AppID: "form", Name: "Form",
		Inputs: map[string]appdef.Input{
			"responses": {Schema: appdef.SchemaFormResponse,
				Where: &appdef.Where{Field: "instance_id", Value: "$instance.id"}, Limit: 1000},
		},
		State: map[string]appdef.Reducer{
			"responses": {Kind: "list", Input: "responses"},
		},
		Actions: map[string]appdef.Action{
			"submit": {Emit: appdef.SchemaFormResponse, Fields: []string{"fields"}},
		},
		Requested: []appdef.Capability{
			{Kind: "read", Schemas: []string{appdef.SchemaFormResponse}},
			{Kind: "append", Schemas: []string{appdef.SchemaFormResponse}},
		},
		// Honesty: responses are encrypted to the space, readable by members.
		Fallback: "A form. Responses are visible to members of this space.",
	},
}

// builtinGrants are the fixed grants a trusted template instance receives.
func builtinGrants(appID string) []appdef.Capability {
	def := builtinApps[appID]
	if def == nil {
		return nil
	}
	return def.Requested // trusted templates: granted == requested
}

// resolveDefinition returns the definition an instance is pinned to.
func (r *Runtime) resolveDefinition(st *spaceState, inst *appdef.Instance) (*appdef.Definition, error) {
	if inst.RevisionEventID == "" {
		def := builtinApps[inst.AppID]
		if def == nil {
			return nil, errors.New("node: unknown built-in app")
		}
		return def, nil
	}
	b, err := hex.DecodeString(inst.RevisionEventID)
	if err != nil || len(b) != 32 {
		return nil, errors.New("node: bad pinned revision")
	}
	var eid id.EventID
	copy(eid[:], b)
	rec, ok := st.space.State.AppDefinitionByEvent(eid)
	if !ok {
		return nil, errors.New("node: pinned definition revision not found")
	}
	if rec.Definition.AppID != inst.AppID {
		return nil, errors.New("node: pinned revision belongs to another app")
	}
	return rec.Definition, nil
}

// PublishAppDefinition emits a custom definition (owner-gated) and returns
// the revision event id instances pin to.
func (r *Runtime) PublishAppDefinition(tid id.TerminalID, def *appdef.Definition) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	if !r.ks.Spaces[tid].Owned {
		return id.EventID{}, errors.New("node: only the space owner can define apps")
	}
	payload, err := appdef.EncodeDefinition(def)
	if err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, appdef.SchemaDefinition, payload)
}

// CreateAppInstance places an app (owner-gated). Grants: built-ins get their
// fixed grants; custom definitions get requested ∩ node policy — recorded in
// the instance event so enforcement is reproducible everywhere.
func (r *Runtime) CreateAppInstance(tid id.TerminalID, appID, revisionEventID, documentID string, props map[string]string) (*appdef.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return nil, errors.New("node: unknown space")
	}
	if !r.ks.Spaces[tid].Owned {
		return nil, errors.New("node: only the space owner can place apps")
	}
	var iid [16]byte
	if _, err := rand.Read(iid[:]); err != nil {
		return nil, err
	}
	inst := &appdef.Instance{
		InstanceID: hex.EncodeToString(iid[:]), AppID: appID,
		RevisionEventID: revisionEventID, DocumentID: documentID, Props: props,
	}
	def, err := r.resolveDefinition(st, inst)
	if err != nil {
		return nil, err
	}
	if inst.RevisionEventID == "" {
		inst.Granted = builtinGrants(appID)
	} else {
		// Custom app: grant requested ∩ node policy, nothing more.
		var granted []appdef.Capability
		for _, c := range def.Requested {
			var ok []string
			for _, s := range c.Schemas {
				if (c.Kind == "read" && appPolicyRead.Has("read", s)) ||
					(c.Kind == "append" && appPolicyAppend.Has("append", s)) {
					ok = append(ok, s)
				}
			}
			if len(ok) > 0 {
				granted = append(granted, appdef.Capability{Kind: c.Kind, Schemas: ok})
			}
		}
		inst.Granted = granted
	}
	if err := appdef.ValidateInstance(inst, def); err != nil {
		return nil, err
	}
	payload, err := appdef.EncodeInstance(inst)
	if err != nil {
		return nil, err
	}
	if _, err := r.emitLocked(st, appdef.SchemaInstance, payload); err != nil {
		return nil, err
	}
	return inst, nil
}

// AppInput serves ONE input of ONE instance: the node loads the pinned
// definition and grants, applies the where filter itself, and refuses any
// schema outside effective capabilities. There is no general events endpoint.
func (r *Runtime) AppInput(tid id.TerminalID, instanceID, inputName string) ([]map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return nil, errors.New("node: unknown space")
	}
	iid, err := hex16(instanceID)
	if err != nil {
		return nil, err
	}
	rec, ok := st.space.State.AppInstanceByID(iid)
	if !ok {
		return nil, errors.New("node: unknown app instance")
	}
	def, err := r.resolveDefinition(st, rec.Instance)
	if err != nil {
		return nil, err
	}
	in, ok := def.Inputs[inputName]
	if !ok {
		return nil, errors.New("node: unknown input")
	}
	requested := appdef.NewCapSet(def.Requested)
	granted := appdef.NewCapSet(rec.Instance.Granted)
	if !appdef.Allowed("read", in.Schema, requested, granted, appPolicyRead) {
		return nil, fmt.Errorf("node: reading %s is not within this app's effective capabilities", in.Schema)
	}
	// The ONLY supported partition in M1 is the instance's own state
	// (where instance_id == $instance.id) — enforced, not client-chosen.
	if in.Where == nil || in.Where.Field != "instance_id" || in.Where.Value != "$instance.id" {
		return nil, errors.New("node: input scope not supported in M1")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 200
	}
	events := st.space.State.AppEvents(iid, in.Schema, limit)
	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		var data any
		if len(ev.Data) > 0 {
			_ = json.Unmarshal(ev.Data, &data)
		}
		out = append(out, map[string]any{
			"author": ev.Author.String(),
			"clock":  ev.Clock,
			"data":   data,
		})
	}
	return out, nil
}

// AppAction is the ONLY emit path for apps: the schema must be within
// effective capabilities; the node injects instance_id and bounds the data.
func (r *Runtime) AppAction(tid id.TerminalID, instanceID, actionName string, fields map[string]any) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	iid, err := hex16(instanceID)
	if err != nil {
		return id.EventID{}, err
	}
	rec, ok := st.space.State.AppInstanceByID(iid)
	if !ok {
		return id.EventID{}, errors.New("node: unknown app instance")
	}
	def, err := r.resolveDefinition(st, rec.Instance)
	if err != nil {
		return id.EventID{}, err
	}
	action, ok := def.Actions[actionName]
	if !ok {
		return id.EventID{}, errors.New("node: unknown action")
	}
	requested := appdef.NewCapSet(def.Requested)
	granted := appdef.NewCapSet(rec.Instance.Granted)
	if !appdef.Allowed("append", action.Emit, requested, granted, appPolicyAppend) {
		return id.EventID{}, fmt.Errorf("node: emitting %s is not within this app's effective capabilities", action.Emit)
	}
	// Copy ONLY the action's declared fields — nothing else passes through.
	data := map[string]any{}
	for _, f := range action.Fields {
		if v, ok := fields[f]; ok {
			data[f] = v
		}
	}
	j, err := json.Marshal(data)
	if err != nil || len(j) > appdef.MaxDataBytes {
		return id.EventID{}, errors.New("node: action data too large")
	}
	ev := &appdef.StateEvent{Fallback: def.Name + ": " + actionName, InstanceID: iid, Data: j}
	return r.emitLocked(st, action.Emit, ev.Encode())
}

func hex16(s string) ([16]byte, error) {
	var out [16]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return out, errors.New("node: id must be 16 bytes hex")
	}
	copy(out[:], b)
	return out, nil
}

// ---- API ----

func (a *APIServer) handleListApps(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	sp, ok := a.rt.Space(tid)
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("unknown space"))
		return
	}
	out := []map[string]any{}
	for _, rec := range sp.State.AppInstances() {
		out = append(out, map[string]any{
			"instance_id": rec.Instance.InstanceID,
			"app_id":      rec.Instance.AppID,
			"document_id": rec.Instance.DocumentID,
			"props":       rec.Instance.Props,
		})
	}
	writeJSON(w, map[string]any{"apps": out})
}

func (a *APIServer) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		AppID           string            `json:"app_id"`
		RevisionEventID string            `json:"revision_event_id"`
		DocumentID      string            `json:"document_id"`
		Props           map[string]string `json:"props"`
	}](r)
	if err != nil || body.AppID == "" {
		httpErr(w, http.StatusBadRequest, errors.New("app_id required"))
		return
	}
	inst, err := a.rt.CreateAppInstance(tid, body.AppID, body.RevisionEventID, body.DocumentID, body.Props)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]any{"instance_id": inst.InstanceID, "granted": inst.Granted})
}

// handleAppMeta returns the instance + its pinned definition's view/props so
// the client runtime can render it.
func (a *APIServer) handleAppMeta(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	sp, ok := a.rt.Space(tid)
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("unknown space"))
		return
	}
	iid, err := hex16(r.PathValue("instance"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	rec, ok := sp.State.AppInstanceByID(iid)
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("unknown app instance"))
		return
	}
	a.rt.mu.Lock()
	st := a.rt.spaces[tid]
	def, err := a.rt.resolveDefinition(st, rec.Instance)
	a.rt.mu.Unlock()
	if err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{
		"instance":   rec.Instance,
		"definition": def,
	})
}

func (a *APIServer) handleAppInput(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	events, err := a.rt.AppInput(tid, r.PathValue("instance"), r.PathValue("input"))
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

func (a *APIServer) handleAppAction(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var fields map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&fields); err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad action body"))
		return
	}
	eid, err := a.rt.AppAction(tid, r.PathValue("instance"), strings.TrimSpace(r.PathValue("action")), fields)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}
