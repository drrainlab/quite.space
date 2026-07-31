// Relay diagnostics bundle (RR-7): one local JSON without secrets that a
// beta tester can copy into a report — selection, trust, health, cadence
// — instead of standing up a metrics stack for twelve users.
package node

import "net/http"

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
	SyncActive  bool   `json:"sync_active"`
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
