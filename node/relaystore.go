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
	"time"
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

// relayStateCache remembers the last parsed relays.json per data dir,
// keyed on the file's size and modification time. The state is asked for
// on every route decision — several times per recipient per space per
// pass — and each ask used to read and parse the file: on the owner's
// phone (26 spaces, automatic relay) that was hundreds of reads a pass and
// the "plan" phase of an EMPTY pass ran one to eight seconds. A stat is
// what a read costs now; an in-process update refreshes the entry itself,
// and a writer from outside the process is seen by the next stat.
var relayStateCache = map[string]relayStateEntry{}

type relayStateEntry struct {
	size  int64
	mtime time.Time
	st    RelayLocalState
}

func relayStatePath(dataDir string) string {
	return filepath.Join(dataDir, "relays.json")
}

// cloneRelayState copies the state so a caller's edits never reach the
// cache (UpdateRelayStateAt edits a copy and then replaces the entry).
func cloneRelayState(st RelayLocalState) RelayLocalState {
	out := st
	out.Stats = make(map[string]*RelayProbeStats, len(st.Stats))
	for k, v := range st.Stats {
		if v != nil {
			c := *v
			out.Stats[k] = &c
		}
	}
	out.Trust = append([]RelayTrust(nil), st.Trust...)
	return out
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
	path := relayStatePath(dataDir)
	fi, statErr := os.Stat(path)
	if statErr == nil {
		if e, ok := relayStateCache[dataDir]; ok && e.size == fi.Size() && e.mtime.Equal(fi.ModTime()) {
			return cloneRelayState(e.st)
		}
	}
	var st RelayLocalState
	b, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Version == 0 {
		st.Version = 1
	}
	if st.Stats == nil {
		st.Stats = map[string]*RelayProbeStats{}
	}
	if statErr == nil {
		relayStateCache[dataDir] = relayStateEntry{size: fi.Size(), mtime: fi.ModTime(), st: cloneRelayState(st)}
	} else {
		delete(relayStateCache, dataDir)
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
	if err := os.Rename(tmp, relayStatePath(dataDir)); err != nil {
		return err
	}
	// The entry follows the write: the next load is a stat and a copy,
	// and never a stale read of what this very call replaced.
	if fi, err := os.Stat(relayStatePath(dataDir)); err == nil {
		relayStateCache[dataDir] = relayStateEntry{size: fi.Size(), mtime: fi.ModTime(), st: cloneRelayState(st)}
	} else {
		delete(relayStateCache, dataDir)
	}
	return nil
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
