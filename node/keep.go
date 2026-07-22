// Keep in Space (LR-1): node-side emit gates and the Shelf API. The API
// enforces the keepable allowlist and unkeep authorization BEFORE emitting;
// the reducer re-checks both against the envelope signature, so nothing the
// API lets through can widen what the space accepts.
package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/keep"
)

// Keep marks an existing event as part of the space's memory.
func (r *Runtime) Keep(tid id.TerminalID, target id.EventID, note string) error {
	if len(note) > keep.MaxNoteLen {
		return fmt.Errorf("node: note exceeds %d bytes", keep.MaxNoteLen)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	// Emit gate: the target must be a known, keepable object right now —
	// keeping blind references is how shelves fill with dead ends.
	_, resolved, keepable := st.space.State.KeepTargetStatus(target)
	if !resolved {
		return errors.New("node: keep target not found in this space")
	}
	if !keepable {
		return errors.New("node: this kind of event cannot be kept")
	}
	payload, err := (&keep.Kept{Target: target, Note: note}).Encode()
	if err != nil {
		return err
	}
	_, err = r.Self.Emit(st.space, keep.SchemaKept, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	return err
}

// Unkeep removes one person's keep state for a target. keepAuthor defaults
// to self; removing someone ELSE's keep is a moderation action allowed only
// to the space controller.
func (r *Runtime) Unkeep(tid id.TerminalID, target id.EventID, keepAuthor id.PrincipalID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if keepAuthor != r.Principal.ID {
		ctrl := st.space.State.Controller
		if ctrl == nil || *ctrl != r.Principal.ID {
			return errors.New("node: only the space controller may remove another member's keep")
		}
	}
	payload := (&keep.Unkept{Target: target, KeepAuthor: keepAuthor}).Encode()
	_, err := r.Self.Emit(st.space, keep.SchemaUnkept, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	return err
}

// ---- API ----

type shelfKeeperResp struct {
	Author     string `json:"author"`
	AuthorName string `json:"author_name"`
	Mine       bool   `json:"mine"`
	Note       string `json:"note,omitempty"`
	Clock      uint64 `json:"clock"`
}

type shelfItemResp struct {
	Target  string            `json:"target"`
	Kind    string            `json:"kind"`
	Removed bool              `json:"removed,omitempty"`
	Keepers []shelfKeeperResp `json:"keepers"`
	// Exactly one of these is set for a live target (none when Removed).
	Entry       *entryResp     `json:"entry,omitempty"`
	Publication map[string]any `json:"publication,omitempty"`
	App         map[string]any `json:"app,omitempty"`
}

func (a *APIServer) handleKeep(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Target string `json:"target"`
		Note   string `json:"note"`
	}](r)
	if err != nil || body.Target == "" {
		httpErr(w, http.StatusBadRequest, errors.New("target required"))
		return
	}
	target, err := parseEventID(body.Target)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.Keep(tid, target, body.Note); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *APIServer) handleUnkeep(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Target     string `json:"target"`
		KeepAuthor string `json:"keep_author"`
	}](r)
	if err != nil || body.Target == "" {
		httpErr(w, http.StatusBadRequest, errors.New("target required"))
		return
	}
	target, err := parseEventID(body.Target)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	author := a.rt.Principal.ID
	if body.KeepAuthor != "" {
		ab, err := hex.DecodeString(body.KeepAuthor)
		if err != nil || len(ab) != id.Size {
			httpErr(w, http.StatusBadRequest, errors.New("bad keep_author"))
			return
		}
		copy(author[:], ab)
	}
	if err := a.rt.Unkeep(tid, target, author); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *APIServer) handleShelf(w http.ResponseWriter, r *http.Request) {
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
	me := a.rt.Principal.ID
	names := map[id.PrincipalID]string{me: a.rt.DisplayName()}
	for _, c := range sp.MemberCards(0) {
		if c.Name != "" {
			names[c.Principal] = c.Name
		}
	}
	items := sp.State.Shelf()
	out := make([]shelfItemResp, 0, len(items))
	for _, it := range items {
		resp := shelfItemResp{
			Target: it.Target.Hex(), Kind: it.Kind, Removed: it.Removed,
			Keepers: make([]shelfKeeperResp, 0, len(it.Keepers)),
		}
		for _, k := range it.Keepers {
			resp.Keepers = append(resp.Keepers, shelfKeeperResp{
				Author: k.Author.String(), AuthorName: names[k.Author],
				Mine: k.Author == me, Note: k.Note, Clock: k.Clock,
			})
		}
		if !it.Removed {
			switch {
			case it.Kind == "publication":
				if docID, ok := sp.State.PublicationDocByTarget(it.Target); ok {
					for _, p := range sp.State.Publications() {
						if p.DocumentID == docID && !p.Archived {
							resp.Publication = map[string]any{
								"id":          hex.EncodeToString(docID[:]),
								"title":       p.Title,
								"author":      p.Author.String(),
								"author_name": names[p.Author],
							}
							break
						}
					}
				}
			case it.Kind == "app":
				if rec, ok := sp.State.AppInstanceByEvent(it.Target); ok {
					resp.App = map[string]any{
						"instance": rec.Instance.InstanceID,
						"app_id":   rec.Instance.AppID,
						"title":    rec.Instance.Props["title"],
					}
				}
			default:
				if e, ok := sp.State.EntryByID(it.Target); ok {
					er := a.projectEntry(tid, &e, me, names)
					resp.Entry = &er
				}
			}
		}
		out = append(out, resp)
	}
	writeJSON(w, out)
}

func parseEventID(s string) (id.EventID, error) {
	var e id.EventID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != id.Size {
		return e, errors.New("bad event id")
	}
	copy(e[:], b)
	return e, nil
}
