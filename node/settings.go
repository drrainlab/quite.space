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
	"strconv"
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
	// RelayMode selects how relays are chosen (RR-0). "" or "custom" =
	// exactly the relay above, nothing else, no hidden official fallback;
	// "automatic" = measured selection over the embedded registry (the
	// probe-selected primary/backup live in relays.json — they are derived
	// runtime state, never user configuration, and never stored here).
	RelayMode string `json:"relay_mode,omitempty"`
	// RelaySyncSeconds is the background push/pull cadence; 0 = default 2s.
	RelaySyncSeconds int `json:"relay_sync_seconds"`
	// Connectivity is the transport policy: which ways out of this device
	// are permitted. Enforced BEFORE a connection is opened.
	Connectivity Connectivity `json:"connectivity"`
	// Attention is the QuietRank policy. It lives in this device-local blob
	// and is never emitted, bundled, or relayed.
	Attention *attention.Policy `json:"attention,omitempty"`
	// Adapters configures external connectors (TR-0d), keyed by connector
	// id. Tokens live here BECAUSE this blob is encrypted at rest (the
	// LLM.APIKey precedent) — and therefore ride `terminal backup`, which
	// the security notes say out loud. They are never projected by the
	// settings API.
	Adapters map[string]AdapterConfig `json:"adapters,omitempty"`
}

// AdapterConfig is one connector's configuration. The zero Profile is the
// STRICT one: text-only, attachments never fetched — opting into more is a
// deliberate act (plan rev 4).
type AdapterConfig struct {
	Type        string `json:"type"` // "email"
	JMAPURL     string `json:"jmap_url,omitempty"`
	Account     string `json:"account,omitempty"`
	Token       string `json:"token,omitempty"`
	PollSeconds int    `json:"poll_seconds,omitempty"`
	Profile     string `json:"profile,omitempty"` // "" == "text-only"
}

// relayInterval clamps the configured cadence to a sane range (default 2s).
func relayInterval(s Settings) time.Duration { return relayIntervalAt(cadence, s) }

// relayIntervalAt is relayInterval against an explicit beat, so the shipped
// arithmetic can be asserted without touching the live variable.
func relayIntervalAt(beat time.Duration, s Settings) time.Duration {
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
	// n is seconds to the person; under test a "second" is half a beat.
	return time.Duration(n) * (beat / 2)
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

// relayIsAutomatic is the ONE reading of the relay mode, used by the code
// that starts a selection and by the code that consumes one. They used to
// disagree: Open measured for a fresh node while the resolver, seeing "",
// treated it as custom-with-no-address and returned nothing — so the node
// probed, chose a primary, wrote it down, and then refused to use it.
//
//	"automatic"           → measured
//	"" AND no address     → measured. Nobody has expressed a preference:
//	                        a fresh install, or a pre-RR-0 node that had no
//	                        relay and was therefore doing nothing anyway.
//	"" AND an address     → that address. A node somebody set up before
//	                        modes existed keeps exactly what it had.
//	"custom"              → exactly what was configured, blank included.
//	                        A choice is not a gap to be helpfully filled.
func relayIsAutomatic(s Settings) bool {
	return s.RelayMode == "automatic" || (s.RelayMode == "" && s.Relay == "")
}

// ErrBadRelayMode is a relay mode this build does not understand.
type ErrBadRelayMode struct{ Mode string }

func (e ErrBadRelayMode) Error() string {
	return "node: unknown relay mode " + strconv.Quote(e.Mode) + " — use \"automatic\" or \"custom\""
}

// SetSettings persists settings. An empty LLM.APIKey means "keep the stored
// key" (the UI sends the key only when the user changes it).
func (r *Runtime) SetSettings(s Settings) error {
	// Refuse an unreadable connectivity policy at the door. A mode this
	// build does not understand must never reach storage: once there it is
	// indistinguishable from a corrupted file, and the only safe reading of
	// it is "send nothing", which looks to the person like an outage.
	if err := s.Connectivity.Validate(); err != nil {
		return err
	}
	// Same rule for the relay mode: validate at the door, hold on unreadable.
	switch s.RelayMode {
	case "", "automatic", "custom":
	default:
		return ErrBadRelayMode{Mode: s.RelayMode}
	}
	r.mu.Lock()
	var cur Settings
	if len(r.ks.Settings) > 0 {
		_ = json.Unmarshal(r.ks.Settings, &cur)
	}
	if s.LLM.APIKey == "" {
		s.LLM.APIKey = cur.LLM.APIKey
	}
	// An omitted connectivity policy means "leave it alone", exactly like an
	// omitted API key. A settings write from a screen that does not show the
	// transport choice must not silently widen it back to the default —
	// which is what saving a zero value here would do.
	if s.Connectivity.Mode == "" && s.Connectivity.PerSpace == nil {
		s.Connectivity = cur.Connectivity
	}
	// An omitted attention policy means "leave it alone" too. Before this
	// guard, ANY settings write from a screen without the QuietRank controls
	// silently erased the trained policy (RR-0 hygiene fix).
	if s.Attention == nil {
		s.Attention = cur.Attention
	}
	// Adapters follow the same rule: a settings write from a screen without
	// connector controls must not erase a JMAP token.
	if s.Adapters == nil {
		s.Adapters = cur.Adapters
	}
	b, err := json.Marshal(s)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.ks.Settings = b
	err = r.saveKeystore()
	// Restart the loop when the address, the cadence OR THE MODE changed.
	//
	// The mode used to be missing from this list, and the consequence was
	// invisible in the worst way: switching to automatic saved the
	// preference and then did nothing at all, because the only thing that
	// ever ran a selection was Open. The node sat with no personal relay
	// — measured 60 s and counting — until somebody restarted it, and
	// every screen that needs one refused meanwhile.
	relayChanged := s.Relay != cur.Relay || s.RelaySyncSeconds != cur.RelaySyncSeconds
	modeChanged := s.RelayMode != cur.RelayMode
	r.mu.Unlock() // release before either call (both take r.mu themselves)
	if err != nil {
		return err
	}
	switch {
	case modeChanged && relayIsAutomatic(s):
		// The same background path Open takes: last-known-good applies at
		// once, a re-measure happens only when the network moved or the
		// reading is stale. Nothing here waits on a probe.
		r.startAutomaticRelay(relayInterval(s))
	case relayChanged || modeChanged:
		// Custom, or a changed address: exactly what was configured.
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
	// Report the mode IN EFFECT, not the stored string. "" means custom on
	// a node that has an address and automatic on one that does not, and a
	// screen showing "Custom" while the node measures would be a plain lie
	// about what it is doing.
	mode := "custom"
	if relayIsAutomatic(s) {
		mode = "automatic"
	}
	return map[string]any{
		"theme": s.Theme, "preset": s.Preset, "render_mode": s.RenderMode,
		"relay": s.Relay, "relay_mode": mode,
		"relay_sync_seconds": int(relayInterval(s) / time.Second),
		"connectivity":       map[string]any{"mode": string(s.Connectivity.Mode)},
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
	// Relay fields are POINTERS so absence is distinguishable from an
	// explicit clear: before this, a settings POST from any screen that
	// did not show the relay silently erased it (RR-0 hygiene fix). nil =
	// keep stored; "" = the user really cleared it.
	body, err := readBody[struct {
		Theme      string  `json:"theme"`
		Preset     string  `json:"preset"`
		RenderMode string  `json:"render_mode"`
		Relay      *string `json:"relay"`
		RelayMode  *string `json:"relay_mode"`
		RelaySync  *int    `json:"relay_sync_seconds"`
		Conn       *struct {
			Mode string `json:"mode"`
		} `json:"connectivity"`
		LLM struct {
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
	cur := a.rt.GetSettings()
	s := Settings{Theme: body.Theme, Preset: body.Preset, RenderMode: body.RenderMode,
		Relay: cur.Relay, RelayMode: cur.RelayMode, RelaySyncSeconds: cur.RelaySyncSeconds}
	if body.Relay != nil {
		s.Relay = strings.TrimSpace(*body.Relay)
	}
	if body.RelayMode != nil {
		s.RelayMode = strings.TrimSpace(*body.RelayMode)
	}
	if body.RelaySync != nil {
		s.RelaySyncSeconds = *body.RelaySync
	}
	s.LLM.Provider = body.LLM.Provider
	s.LLM.Model = body.LLM.Model
	s.LLM.BaseURL = body.LLM.BaseURL
	s.LLM.APIKey = body.LLM.APIKey
	// A POINTER, for the same reason the relay fields are: absent has to be
	// distinguishable from cleared. Left nil, the whole policy stays zero and
	// SetSettings' "omitted means leave it alone" guard restores what was
	// stored — including a mode this build cannot read, which must survive a
	// settings save from an unrelated screen rather than block it.
	if body.Conn != nil {
		s.Connectivity.Mode = ConnectivityMode(body.Conn.Mode)
		// PER-SPACE OVERRIDES ARE NOT THIS CALLER'S TO DROP. The endpoint
		// speaks only about the device-wide mode, and the guard above cannot
		// help once a mode is present: it fires only when the WHOLE policy is
		// zero. Without this line, naming a mode would carry a nil PerSpace
		// over the stored one and silently widen every room that had been
		// narrowed.
		s.Connectivity.PerSpace = cur.Connectivity.PerSpace
	}
	if err := a.rt.SetSettings(s); err != nil {
		// An unreadable mode is the caller's mistake, not a server fault.
		if _, badConn := err.(ErrBadConnectivityMode); badConn {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		if _, badRelay := err.(ErrBadRelayMode); badRelay {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
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
