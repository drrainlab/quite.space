// relays.json (RR-0): derived relay runtime state, beside the encrypted
// store. Everything in here is DISPOSABLE and non-secret — measurements,
// health counters, the automatically selected primary/backup, TOFU pins
// for custom relays, and the opaque network fingerprint the measurements
// were taken under. It deliberately lives OUTSIDE Settings: a
// probe-selected relay is derived runtime state, and storing it as
// configuration would make an old measurement look like a user decision.
//
// Plain JSON, tmp+rename, human-auditable — the quicklinks.json pattern.
package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// RelayProbeStats is one relay's measured history (EWMA-smoothed; the
// scoring itself lands with RR-3 and is pure over this struct).
type RelayProbeStats struct {
	RTTEWMAMs    float64 `json:"rtt_ewma_ms,omitempty"`
	JitterEWMAMs float64 `json:"jitter_ewma_ms,omitempty"`
	LastRTTMs    float64 `json:"last_rtt_ms,omitempty"`

	SuccessCount        int   `json:"success_count,omitempty"`
	FailureCount        int   `json:"failure_count,omitempty"`
	ConsecutiveFailures int   `json:"consecutive_failures,omitempty"`
	LastSuccessUnix     int64 `json:"last_success_unix,omitempty"`
	LastFailureUnix     int64 `json:"last_failure_unix,omitempty"`

	// LoadClass is the relay's last self-reported load ("" until probed).
	LoadClass string `json:"load_class,omitempty"`
}

// RelayTrust is the local trust record for a CUSTOM relay endpoint —
// a confirmed TOFU pin. Official pins live in the registry, never here.
type RelayTrust struct {
	Endpoint string `json:"endpoint"`
	// SPKIPin: base64 SHA-256 of the confirmed identity key.
	SPKIPin string `json:"spki_pin"`
	// ConfirmedUnix — when the person (UI or `terminal relay trust`)
	// confirmed the fingerprint. A pin is never stored silently.
	ConfirmedUnix int64 `json:"confirmed_unix"`
}

// RelayLocalState is the whole relays.json document.
type RelayLocalState struct {
	Version int `json:"version"`

	// NetworkFingerprint is an opaque local hash of the interface set the
	// measurements were taken under. A change invalidates the cache; the
	// raw network identifiers themselves are never stored.
	NetworkFingerprint string `json:"network_fingerprint,omitempty"`

	// Selected primary/backup for automatic mode — RelayRef strings.
	SelectedPrimary   string `json:"selected_primary,omitempty"`
	SelectedBackup    string `json:"selected_backup,omitempty"`
	LastSelectionUnix int64  `json:"last_selection_unix,omitempty"`

	// Stats keyed by RelayRef string.
	Stats map[string]*RelayProbeStats `json:"stats,omitempty"`

	// Trust: confirmed TOFU pins for custom endpoints.
	Trust []RelayTrust `json:"trust,omitempty"`
}

var relayStateMu sync.Mutex

func relayStatePath(dataDir string) string {
	return filepath.Join(dataDir, "relays.json")
}

// LoadRelayStateAt reads relays.json under dataDir; a missing or
// unreadable file is an empty state (everything here is re-derivable).
// A free function on purpose: `terminal relay trust` runs without a
// passphrase — nothing in this file is secret.
func LoadRelayStateAt(dataDir string) RelayLocalState {
	relayStateMu.Lock()
	defer relayStateMu.Unlock()
	return loadRelayStateLocked(dataDir)
}

func loadRelayStateLocked(dataDir string) RelayLocalState {
	var st RelayLocalState
	b, err := os.ReadFile(relayStatePath(dataDir))
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Version == 0 {
		st.Version = 1
	}
	if st.Stats == nil {
		st.Stats = map[string]*RelayProbeStats{}
	}
	return st
}

// UpdateRelayStateAt applies fn and writes back atomically (tmp+rename).
func UpdateRelayStateAt(dataDir string, fn func(*RelayLocalState)) error {
	relayStateMu.Lock()
	defer relayStateMu.Unlock()
	st := loadRelayStateLocked(dataDir)
	fn(&st)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := relayStatePath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, relayStatePath(dataDir))
}

func (r *Runtime) loadRelayState() RelayLocalState { return LoadRelayStateAt(r.dataDir) }

func (r *Runtime) updateRelayState(fn func(*RelayLocalState)) error {
	return UpdateRelayStateAt(r.dataDir, fn)
}

// TrustedPin returns the confirmed TOFU pin for a custom endpoint, if any.
func (st RelayLocalState) TrustedPin(endpoint string) (string, bool) {
	for _, t := range st.Trust {
		if t.Endpoint == endpoint {
			return t.SPKIPin, true
		}
	}
	return "", false
}
