// SC-4 AI composition: a prompt becomes a *validated appearance proposal*, never
// a direct change. The node builds a constrained system prompt (the allowlisted
// appearance grammar), asks the user-configured LLM for JSON only, parses it
// into an AppearanceOverride, and validates it through the same projection +
// ValidateAppearance gate as a manual edit. The user accepts a proposal by
// PATCHing it (reusing the SC-3 signed-revision path); AI never writes the log.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/protocol/composition"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// appearanceSystemPrompt enumerates the strict, bounded grammar the model may
// use. It must return JSON only — there is no free-form styling channel.
func appearanceSystemPrompt() string {
	return strings.Join([]string{
		"You design a calm, accessible atmosphere for a private chat space.",
		"Reply with a SINGLE JSON object and nothing else — no prose, no code fences.",
		"Allowed keys (all optional):",
		`  "accent":  "#rrggbb"  — primary accent color`,
		`  "canvas":  "#rrggbb"  — background base color`,
		`  "tint":    "#rrggbb"  — background tint`,
		`  "dim":     integer 0..1000  — how much the background is dimmed`,
		`  "blur":    integer 0..64     — background blur in px`,
		`  "motion":  one of "still" | "quiet" | "lively"`,
		`  "density": one of "calm" | "dense"`,
		"Keep text readable: prefer sufficient contrast between accent and canvas.",
		"Do not include any other keys or any explanation.",
	}, "\n")
}

// parseOverride extracts the JSON object from the model's reply.
func parseOverride(raw string) (AppearanceOverride, error) {
	var ov AppearanceOverride
	i := strings.IndexByte(raw, '{')
	j := strings.LastIndexByte(raw, '}')
	if i < 0 || j < i {
		return ov, errors.New("node: the model did not return a JSON object")
	}
	if err := json.Unmarshal([]byte(raw[i:j+1]), &ov); err != nil {
		return ov, errors.New("node: could not parse the model's JSON")
	}
	return ov, nil
}

// applyLocks overwrites locked fields in the proposal with the space's current
// stored override, so AI cannot touch what the user pinned (ADR-013).
func applyLocks(ov *AppearanceOverride, locked map[string]bool, cur AppearanceOverride) {
	if locked["accent"] {
		ov.Accent = cur.Accent
	}
	if locked["canvas"] {
		ov.Canvas = cur.Canvas
	}
	if locked["tint"] {
		ov.Tint = cur.Tint
	}
	if locked["dim"] {
		ov.Dim = cur.Dim
	}
	if locked["blur"] {
		ov.Blur = cur.Blur
	}
	if locked["motion"] {
		ov.Motion = cur.Motion
	}
	if locked["density"] {
		ov.Density = cur.Density
	}
}

// ProposeAppearance generates a validated appearance proposal for a prompt. It
// never commits; it returns the override + the projected appearance so the UI
// can preview it. A proposal that violates the grammar is an error, not a
// silent partial apply.
func (r *Runtime) ProposeAppearance(ctx context.Context, tid id.TerminalID, prompt string, locks []string) (AppearanceOverride, *composition.Appearance, error) {
	cfg := r.LLMConfig()
	if cfg.Provider == "" || cfg.Model == "" {
		return AppearanceOverride{}, nil, errors.New("node: configure an AI provider in Settings first")
	}
	raw, err := r.llm().Generate(ctx, cfg, appearanceSystemPrompt(), prompt)
	if err != nil {
		return AppearanceOverride{}, nil, err
	}
	ov, err := parseOverride(raw)
	if err != nil {
		return AppearanceOverride{}, nil, err
	}
	locked := map[string]bool{}
	for _, l := range locks {
		locked[strings.ToLower(strings.TrimSpace(l))] = true
	}
	applyLocks(&ov, locked, r.currentOverrideLocked(tid))

	r.mu.Lock()
	st, ok := r.spaces[tid]
	r.mu.Unlock()
	if !ok {
		return AppearanceOverride{}, nil, errors.New("node: unknown space")
	}
	_, ch := st.space.Character()
	ap := projectAppearance(ch, &ov)
	if err := composition.ValidateAppearance(ap); err != nil {
		return AppearanceOverride{}, nil, err
	}
	return ov, ap, nil
}

// currentOverrideLocked is currentOverride with the runtime lock held safely.
func (r *Runtime) currentOverrideLocked(tid id.TerminalID) AppearanceOverride {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentOverride(tid)
}

// ---- API ----

func (a *APIServer) handleProposeAppearance(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Prompt string   `json:"prompt"`
		Locks  []string `json:"locks"`
	}](r)
	if err != nil || strings.TrimSpace(body.Prompt) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("prompt required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	ov, ap, err := a.rt.ProposeAppearance(ctx, tid, body.Prompt, body.Locks)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok":         true,
		"override":   ov,
		"appearance": appearanceJSON(ap),
	})
}
