// Resonance (RP-2), node side: emit gates and the projection API. The API
// enforces the palette policy BEFORE emitting (semantic keys must be in the
// active palette, unicode only when allowed); the reducer folds whatever is
// in the log — policy gates emission, never history.
package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/resonance"
	"github.com/drrainlab/quiet_places/terminals"
)

// ResonanceSet places (or replaces) the caller's single active reaction.
func (r *Runtime) ResonanceSet(tid id.TerminalID, target id.EventID, re resonance.Reaction) error {
	if err := re.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if !st.space.State.ResonanceTargetStatus(target) {
		return errors.New("node: reaction target not found in this space")
	}
	pal, _ := st.space.State.ActivePalette()
	switch re.Kind {
	case resonance.KindSemantic:
		found := false
		for _, s := range pal.Slots {
			if s.Key == re.Key {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("node: %q is not in this space's palette", re.Key)
		}
	case resonance.KindUnicode:
		if !pal.Policy.AllowUnicode {
			return errors.New("node: this space speaks semantic reactions only")
		}
	}
	payload, err := (&resonance.SetPayload{Target: target, Reaction: re}).Encode()
	if err != nil {
		return err
	}
	_, err = r.Self.Emit(st.space, resonance.SchemaSet, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	return err
}

// ResonanceClear releases the caller's reaction on a target.
func (r *Runtime) ResonanceClear(tid id.TerminalID, target id.EventID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if !st.space.State.ResonanceTargetStatus(target) {
		return errors.New("node: reaction target not found in this space")
	}
	payload := (&resonance.ClearPayload{Target: target}).Encode()
	_, err := r.Self.Emit(st.space, resonance.SchemaClear, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	return err
}

// SetResonancePalette publishes the space palette (owner only).
func (r *Runtime) SetResonancePalette(tid id.TerminalID, pal *resonance.Palette) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	if !r.ks.Spaces[tid].Owned {
		return errors.New("node: only the space owner tunes the palette")
	}
	payload, err := pal.Encode()
	if err != nil {
		return err
	}
	_, err = r.Self.Emit(st.space, resonance.SchemaPalette, payload,
		r.Self.DefaultAuthorship(), uint64(time.Now().Unix()))
	return err
}

// ---- API projection ----

const actorsPreviewCap = 5

type resReactionResp struct {
	Kind     string `json:"kind"` // semantic | unicode
	Key      string `json:"key,omitempty"`
	Value    string `json:"value,omitempty"`
	Fallback string `json:"fallback,omitempty"` // authoritative glyph (ResolveFallback)
	Label    string `json:"label,omitempty"`    // palette/registry label when known
}

type resActorResp struct {
	Name string `json:"name"`
	Mine bool   `json:"mine"`
}

type resGroupResp struct {
	resReactionResp
	Count           int            `json:"count"`
	ActorsPreview   []resActorResp `json:"actors_preview,omitempty"`
	ActorsTruncated bool           `json:"actors_truncated,omitempty"`
}

type resonanceResp struct {
	Groups   []resGroupResp   `json:"groups,omitempty"`
	Total    int              `json:"total"`
	Own      *resReactionResp `json:"own,omitempty"`
	Revision string           `json:"revision,omitempty"`
}

func projectReactionResp(re resonance.Reaction, pal *resonance.Palette) resReactionResp {
	out := resReactionResp{}
	if re.Kind == resonance.KindSemantic {
		out.Kind = "semantic"
		out.Key = re.Key
		out.Fallback = resonance.ResolveFallback(re.Key, pal, re.Fallback)
		for _, s := range pal.Slots {
			if s.Key == re.Key {
				out.Label = s.Label
			}
		}
		if out.Label == "" {
			if m, ok := resonance.CoreMeaningByKey(re.Key); ok {
				out.Label = m.Label
			}
		}
	} else {
		out.Kind = "unicode"
		out.Value = re.Value
		out.Fallback = re.Value
	}
	return out
}

// projectResonance builds one target's aggregate for the API (own/mine are
// viewer-relative — this layer only).
func (a *APIServer) projectResonance(sp *terminals.Space, target id.EventID,
	me id.PrincipalID, names map[id.PrincipalID]string) *resonanceResp {

	agg := sp.State.ResonanceFor(target)
	pal, _ := sp.State.ActivePalette()
	out := &resonanceResp{Total: agg.Total}
	if agg.RevClock != 0 || agg.RevEID != (id.EventID{}) {
		out.Revision = fmt.Sprintf("%d-%s", agg.RevClock, hex.EncodeToString(agg.RevEID[:8]))
	}
	for _, g := range agg.Groups {
		gr := resGroupResp{resReactionResp: projectReactionResp(g.Reaction, &pal), Count: g.Count}
		for i, actor := range g.Actors {
			if i >= actorsPreviewCap {
				gr.ActorsTruncated = true
				break
			}
			gr.ActorsPreview = append(gr.ActorsPreview, resActorResp{
				Name: names[actor], Mine: actor == me,
			})
		}
		out.Groups = append(out.Groups, gr)
	}
	if own, ok := sp.State.OwnResonance(target, me); ok {
		o := projectReactionResp(own, &pal)
		out.Own = &o
	}
	if out.Total == 0 && out.Own == nil && out.Revision == "" {
		return nil // keep feed JSON lean
	}
	return out
}

// ---- handlers ----

func (a *APIServer) handleResonance(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Target   string `json:"target"`
		Op       string `json:"op"` // set | clear
		Reaction *struct {
			Kind  string `json:"kind"`
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"reaction"`
	}](r)
	if err != nil || body.Target == "" {
		httpErr(w, http.StatusBadRequest, errors.New("target and op required"))
		return
	}
	target, err := parseEventID(body.Target)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	switch body.Op {
	case "clear":
		err = a.rt.ResonanceClear(tid, target)
	case "set":
		if body.Reaction == nil {
			httpErr(w, http.StatusBadRequest, errors.New("set requires a reaction"))
			return
		}
		re := resonance.Reaction{}
		switch body.Reaction.Kind {
		case "semantic":
			re.Kind = resonance.KindSemantic
			re.Key = body.Reaction.Key
			// The wire fallback for a semantic emit comes from the palette /
			// registry — the client never invents it.
			sp, ok := a.rt.Space(tid)
			if !ok {
				httpErr(w, http.StatusNotFound, errors.New("unknown space"))
				return
			}
			pal, _ := sp.State.ActivePalette()
			re.Fallback = resonance.ResolveFallback(re.Key, &pal, "")
		case "unicode":
			re.Kind = resonance.KindUnicode
			re.Value = body.Reaction.Value
		default:
			httpErr(w, http.StatusBadRequest, errors.New("reaction kind must be semantic or unicode"))
			return
		}
		err = a.rt.ResonanceSet(tid, target, re)
	default:
		httpErr(w, http.StatusBadRequest, errors.New("op must be set or clear"))
		return
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func paletteJSON(pal resonance.Palette, source string) map[string]any {
	slots := make([]map[string]any, 0, len(pal.Slots))
	for _, s := range pal.Slots {
		slot := map[string]any{"key": s.Key, "label": s.Label, "fallback": s.Fallback}
		if s.RendererID != "" {
			slot["renderer_id"] = s.RendererID
		}
		if s.EffectID != "" {
			slot["effect_id"] = s.EffectID
		}
		slots = append(slots, slot)
	}
	registry := make([]map[string]string, 0, len(resonance.CoreRegistry))
	for _, m := range resonance.CoreRegistry {
		registry = append(registry, map[string]string{
			"key": m.Key, "label": m.Label, "fallback": m.Fallback,
		})
	}
	return map[string]any{
		"palette_id":  pal.PaletteID,
		"default_key": pal.DefaultKey,
		"slots":       slots,
		"policy": map[string]any{
			"allow_unicode":   pal.Policy.AllowUnicode,
			"show_counts":     pal.Policy.ShowCounts,
			"show_actors":     pal.Policy.ShowActors,
			"surface_effects": pal.Policy.SurfaceEffects,
		},
		"source":   source,
		"registry": registry, // the Go core registry — never mirrored by hand
	}
}

func (a *APIServer) handleGetResonancePalette(w http.ResponseWriter, r *http.Request) {
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
	pal, own := sp.State.ActivePalette()
	source := "default"
	if own {
		source = "space"
	}
	writeJSON(w, paletteJSON(pal, source))
}

func (a *APIServer) handleSetResonancePalette(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		PaletteID  string `json:"palette_id"`
		DefaultKey string `json:"default_key"`
		Slots      []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Fallback string `json:"fallback"`
			Renderer string `json:"renderer_id"`
			Effect   string `json:"effect_id"`
		} `json:"slots"`
		Policy struct {
			AllowUnicode   bool `json:"allow_unicode"`
			ShowCounts     bool `json:"show_counts"`
			SurfaceEffects bool `json:"surface_effects"`
		} `json:"policy"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad palette body"))
		return
	}
	pal := &resonance.Palette{
		PaletteID:  body.PaletteID,
		DefaultKey: body.DefaultKey,
		Policy: resonance.PalettePolicy{
			Cardinality: 1, AllowUnicode: body.Policy.AllowUnicode,
			ShowCounts: body.Policy.ShowCounts, ShowActors: resonance.ActorsMembers,
			SurfaceEffects: body.Policy.SurfaceEffects,
		},
	}
	for _, s := range body.Slots {
		pal.Slots = append(pal.Slots, resonance.PaletteSlot{
			Key: s.Key, Label: s.Label, Fallback: s.Fallback,
			RendererID: s.Renderer, EffectID: s.Effect,
		})
	}
	if err := a.rt.SetResonancePalette(tid, pal); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleResonanceActors serves the FULL actor list of one target on expand
// (feed responses carry only a bounded preview).
func (a *APIServer) handleResonanceActors(w http.ResponseWriter, r *http.Request) {
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
	target, err := parseEventID(r.PathValue("target"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	me := a.rt.Principal.ID
	names := map[id.PrincipalID]string{me: a.rt.DisplayName()}
	for _, c := range sp.MemberCards(0) {
		if c.Name != "" {
			names[c.Principal] = c.Name
		}
	}
	agg := sp.State.ResonanceFor(target)
	pal, _ := sp.State.ActivePalette()
	out := make([]map[string]any, 0, len(agg.Groups))
	for _, g := range agg.Groups {
		actors := make([]resActorResp, 0, len(g.Actors))
		for _, actor := range g.Actors {
			actors = append(actors, resActorResp{Name: names[actor], Mine: actor == me})
		}
		re := projectReactionResp(g.Reaction, &pal)
		out = append(out, map[string]any{
			"kind": re.Kind, "key": re.Key, "value": re.Value,
			"fallback": re.Fallback, "label": re.Label,
			"count": g.Count, "actors": actors,
		})
	}
	writeJSON(w, map[string]any{"groups": out, "total": agg.Total})
}

var _ = reducers.ResonanceAggregate{}
