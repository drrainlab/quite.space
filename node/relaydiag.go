// Relay diagnostics bundle (RR-7): one local JSON without secrets that a
// beta tester can copy into a report — selection, trust, health, cadence
// — instead of standing up a metrics stack for twelve users.
package node

import (
	"errors"
	"net/http"
	"strings"
)

// RelayDiagnostics is the copyable snapshot.
type RelayDiagnostics struct {
	RegistryVersion int    `json:"registry_version"`
	Mode            string `json:"mode"` // automatic | custom
	Primary         string `json:"primary,omitempty"`
	Backup          string `json:"backup,omitempty"`
	PrimaryHealth   string `json:"primary_health,omitempty"`
	// Trust: official-pinned | tofu-pinned | local-lan | none
	Trust string `json:"trust,omitempty"`
	// RTTBucketMs rounds the primary's EWMA to a 10ms bucket — enough to
	// reason about selection, not enough to fingerprint a path.
	RTTBucketMs int    `json:"rtt_bucket_ms,omitempty"`
	Jitter      int    `json:"jitter_ms,omitempty"`
	LoadClass   string `json:"load_class,omitempty"`
	// ThrottledForMs is how long this node has decided NOT to ask, because
	// the relay said to come back later (RR). Reported because a node that
	// is deliberately waiting looks exactly like one that is broken: sync is
	// active, the relay is healthy, and nothing is arriving. Zero when it is
	// free to ask.
	ThrottledForMs int    `json:"throttled_for_ms,omitempty"`
	SyncActive     bool   `json:"sync_active"`
	IntervalMs  int    `json:"interval_ms,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	// Public spaces and where their traffic goes ("" = the personal relay).
	Spaces []PublicSpaceStatus `json:"spaces,omitempty"`
}

// RelayDiagnosticsSnapshot assembles the bundle. Nothing here is secret:
// refs, health words, bucketed timings — no caps, no hints, no pins of
// OTHER people's relays beyond their public identity role.
func (r *Runtime) RelayDiagnosticsSnapshot() RelayDiagnostics {
	s := r.GetSettings()
	st := r.loadRelayState()
	d := RelayDiagnostics{RegistryVersion: BuiltinRelayRegistry.Version}
	if s.RelayMode == "automatic" {
		d.Mode = "automatic"
		d.Primary = st.SelectedPrimary
		d.Backup = st.SelectedBackup
	} else {
		d.Mode = "custom"
		if s.Relay != "" {
			d.Primary = RelayRef{Endpoint: s.Relay}.String()
		}
	}
	if d.Primary != "" {
		if ref, err := ParseRelayRef(d.Primary); err == nil {
			if ep, ok := ref.Resolve(BuiltinRelayRegistry); ok {
				d.PrimaryHealth = r.pool().health(ep)
				// Waiting on purpose is not the same as being broken, and
				// from outside they are identical: sync active, relay
				// healthy, nothing arriving.
				if left, yes := r.relayThrottled(ep); yes {
					d.ThrottledForMs = int(left.Milliseconds())
				}
				switch {
				case ref.Official != "":
					if desc, found := BuiltinRelayRegistry.ByID(ref.Official); found {
						if len(desc.SPKIPins) > 0 {
							d.Trust = "official-pinned"
						} else {
							d.Trust = "local-lan"
						}
					}
				default:
					if _, ok := st.TrustedPin(ep); ok {
						d.Trust = "tofu-pinned"
					} else if loopbackAddr(ep) {
						d.Trust = "local-lan"
					} else {
						d.Trust = "none"
					}
				}
			}
		}
		if ps := st.Stats[d.Primary]; ps != nil {
			d.RTTBucketMs = int(ps.RTTEWMAMs/10) * 10
			d.Jitter = int(ps.JitterEWMAMs)
			d.LoadClass = ps.LoadClass
		}
	}
	sync := r.RelaySync()
	d.SyncActive = sync.Active
	d.IntervalMs = sync.IntervalMs
	d.LastError = sync.LastErr
	d.Spaces = sync.Public
	return d
}

func (a *APIServer) handleRelayDiagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.rt.RelayDiagnosticsSnapshot())
}

// handleRelayIdentity OBSERVES a relay's identity without trusting it —
// the UI shows the fingerprint so a person can compare it with the
// operator before anything is pinned.
func (a *APIServer) handleRelayIdentity(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Endpoint string `json:"endpoint"`
	}](r)
	if err != nil || strings.TrimSpace(body.Endpoint) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("endpoint required"))
		return
	}
	ep := strings.TrimSpace(body.Endpoint)
	pin, err := RelayIdentity(ep)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	_, trusted := a.rt.loadRelayState().TrustedPin(ep)
	writeJSON(w, map[string]any{
		"endpoint": ep, "pin": pin, "trusted": trusted,
		"local_lan": loopbackAddr(ep),
	})
}

// handleRelayTrust stores a CONFIRMED pin. The fingerprint must match what
// the relay currently presents — confirming blind would defeat the point.
func (a *APIServer) handleRelayTrust(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Endpoint    string `json:"endpoint"`
		Fingerprint string `json:"fingerprint"`
		Forget      bool   `json:"forget"`
	}](r)
	if err != nil || strings.TrimSpace(body.Endpoint) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("endpoint required"))
		return
	}
	ep := strings.TrimSpace(body.Endpoint)
	if body.Forget {
		if err := a.rt.ForgetRelay(ep); err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]string{"status": "forgotten"})
		return
	}
	if err := a.rt.TrustRelay(ep, strings.TrimSpace(body.Fingerprint)); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	// A re-confirmed relay leaves the untrusted latch (RR-6).
	a.rt.pool().resetTrust(ep)
	writeJSON(w, map[string]string{"status": "pinned"})
}

// handleRelayRemeasure runs the measured selection now (automatic mode).
func (a *APIServer) handleRelayRemeasure(w http.ResponseWriter, r *http.Request) {
	primary, backup := a.rt.runAutoSelection()
	if primary == "" {
		writeJSON(w, map[string]any{"status": "nothing reachable"})
		return
	}
	// Move the sync loop onto the (possibly new) primary right away.
	if ref, err := ParseRelayRef(primary); err == nil {
		if ep, ok := ref.Resolve(BuiltinRelayRegistry); ok {
			s := a.rt.GetSettings()
			a.rt.applyRelaySync(ep, relayInterval(s))
		}
	}
	writeJSON(w, map[string]any{
		"status": "measured", "primary": primary, "backup": backup,
	})
}
