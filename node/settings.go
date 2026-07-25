// Local settings (PUI-2): UI preferences + the LLM provider config, persisted
// in the encrypted keystore. The API redacts the key on read; the node makes
// the outbound call only on an explicit user action.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/drrainlab/quiet_places/attention"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/node/llm"
)

// Settings is the local, per-device config blob.
type Settings struct {
	Theme      string     `json:"theme"`       // auto | light | dark
	Preset     string     `json:"preset"`      // quiet-glass | daylight | minimal-mono | comfort
	RenderMode string     `json:"render_mode"` // auto | full | reduced | minimal
	LLM        llm.Config `json:"llm"`
	// Relay: address of a blind relay to auto-sync through (host:port). Empty
	// disables background relay sync. Set it and the node pushes changed
	// spaces + pulls on a timer — no manual push/pull needed.
	Relay string `json:"relay"`
	// RelaySyncSeconds is the background push/pull cadence; 0 = default 2s.
	RelaySyncSeconds int `json:"relay_sync_seconds"`
	// Attention is the QuietRank policy. It lives in this device-local blob
	// and is never emitted, bundled, or relayed.
	Attention *attention.Policy `json:"attention,omitempty"`
}

// relayInterval clamps the configured cadence to a sane range (default 2s).
func relayInterval(s Settings) time.Duration {
	n := s.RelaySyncSeconds
	if n <= 0 {
		n = 2
	}
	if n < 1 {
		n = 1
	}
	if n > 3600 {
		n = 3600
	}
	return time.Duration(n) * time.Second
}

// GetSettings returns the decoded settings (zero-value if unset).
func (r *Runtime) GetSettings() Settings {
	r.mu.Lock()
	defer r.mu.Unlock()
	var s Settings
	if len(r.ks.Settings) > 0 {
		_ = json.Unmarshal(r.ks.Settings, &s)
	}
	return s
}

// SetSettings persists settings. An empty LLM.APIKey means "keep the stored
// key" (the UI sends the key only when the user changes it).
func (r *Runtime) SetSettings(s Settings) error {
	r.mu.Lock()
	var cur Settings
	if len(r.ks.Settings) > 0 {
		_ = json.Unmarshal(r.ks.Settings, &cur)
	}
	if s.LLM.APIKey == "" {
		s.LLM.APIKey = cur.LLM.APIKey
	}
	b, err := json.Marshal(s)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.ks.Settings = b
	err = r.saveKeystore()
	// Restart the loop when the address OR the cadence changed.
	relayChanged := s.Relay != cur.Relay || s.RelaySyncSeconds != cur.RelaySyncSeconds
	r.mu.Unlock() // release before applyRelaySync (it takes r.mu itself)
	if err != nil {
		return err
	}
	if relayChanged {
		r.applyRelaySync(s.Relay, relayInterval(s))
	}
	return nil
}

// LLMConfig returns the current provider config (with the real key) for
// node-side generation. Never exposed through the API.
func (r *Runtime) LLMConfig() llm.Config { return r.GetSettings().LLM }

// llm returns the generation client (default, or a test-injected one).
func (r *Runtime) llm() *llm.Client {
	if r.llmClient != nil {
		return r.llmClient
	}
	return llm.New()
}

// TestLLM runs a tiny round-trip to verify the provider + key are usable.
func (r *Runtime) TestLLM(ctx context.Context) error {
	cfg := r.LLMConfig()
	if cfg.Provider == "" || cfg.Model == "" {
		return errors.New("node: choose a provider and model first")
	}
	_, err := llm.New().Generate(ctx, cfg,
		"You are a connectivity probe. Reply with the single word: ok.",
		"ping")
	return err
}

// ---- API ----

// settingsJSON is the API view: it carries has_key instead of the key itself.
func settingsJSON(s Settings) map[string]any {
	return map[string]any{
		"theme": s.Theme, "preset": s.Preset, "render_mode": s.RenderMode,
		"relay": s.Relay, "relay_sync_seconds": int(relayInterval(s) / time.Second),
		"llm": map[string]any{
			"provider": s.LLM.Provider, "model": s.LLM.Model,
			"base_url": s.LLM.BaseURL, "has_key": s.LLM.APIKey != "",
		},
	}
}

func (a *APIServer) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, settingsJSON(a.rt.GetSettings()))
}

func (a *APIServer) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Theme      string `json:"theme"`
		Preset     string `json:"preset"`
		RenderMode string `json:"render_mode"`
		Relay      string `json:"relay"`
		RelaySync  int    `json:"relay_sync_seconds"`
		LLM        struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			BaseURL  string `json:"base_url"`
			APIKey   string `json:"api_key"` // omitted by the UI unless changed
		} `json:"llm"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	s := Settings{Theme: body.Theme, Preset: body.Preset, RenderMode: body.RenderMode,
		Relay: strings.TrimSpace(body.Relay), RelaySyncSeconds: body.RelaySync}
	s.LLM.Provider = body.LLM.Provider
	s.LLM.Model = body.LLM.Model
	s.LLM.BaseURL = body.LLM.BaseURL
	s.LLM.APIKey = body.LLM.APIKey
	if err := a.rt.SetSettings(s); err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, settingsJSON(a.rt.GetSettings()))
}

func (a *APIServer) handleTestLLM(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.rt.TestLLM(ctx); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
