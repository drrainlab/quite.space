// Relay diagnostics bundle (RR-7): one local JSON without secrets that a
// beta tester can copy into a report — selection, trust, health, cadence
// — instead of standing up a metrics stack for twelve users.
package node

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/storage"
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
	IntervalMs     int    `json:"interval_ms,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	// Public spaces and where their traffic goes ("" = the personal relay).
	Spaces []PublicSpaceStatus `json:"spaces,omitempty"`
	// The route book (RT-0), so "where does this go" and "why is this held"
	// are answerable on screen. Ingress is every endpoint this device
	// listens on; Peers summarises delivery routes per known peer device —
	// endpoints and provenance only, never mailbox hints or caps. NoRoute
	// counts peer devices this node currently cannot deliver to at all:
	// non-zero here is the honest face of a hold that used to be silence.
	Ingress []string         `json:"ingress,omitempty"`
	Peers   []PeerRouteBrief `json:"peers,omitempty"`
	NoRoute int              `json:"no_route_peers,omitempty"`
	// LocalPeers are devices authenticated live on a local link right now
	// (T6-LAN observed routes). Ephemeral by doctrine — this list is the
	// ONLY place they surface; they are never in Peers, because Peers is
	// the durable book and these are the room.
	LocalPeers []string `json:"local_peers,omitempty"`
	// THE VERDICTS, because a beta report is exactly the moment they are
	// needed: Held is what this node could not hand over and why
	// (sender side), WantHolds is media it was ASKED for and could not
	// answer yet (holder side), Fetches is every asset THIS node is
	// trying to pull right now or has given up on, with the true reason.
	// Together the two halves of one stuck photo — the asker's fetch and
	// the holder's want_hold — name the seam that failed.
	Held      []HeldSpace      `json:"held,omitempty"`
	WantHolds []WantHoldStatus `json:"want_holds,omitempty"`
	Fetches   []FetchBrief     `json:"fetches,omitempty"`
}

// FetchBrief is one in-flight or failed asset fetch, for diagnostics.
type FetchBrief struct {
	Space string `json:"space"` // short prefix
	Asset string `json:"asset"` // short prefix
	State string `json:"state"` // fetching | silent | failed
	// Reason is the fetch verdict for failed entries (no_peers, …).
	Reason string `json:"reason,omitempty"`
	// Missing/Total chunks — two diagnostics snapshots a few seconds
	// apart show whether a fetch is flowing or frozen, which is the
	// exact question a stalled progress bar raises.
	Missing int `json:"missing,omitempty"`
	Total   int `json:"total,omitempty"`
}

// PeerRouteBrief is one peer device's delivery picture, for diagnostics.
type PeerRouteBrief struct {
	Device string   `json:"device"` // short prefix — enough to correlate
	Routes []string `json:"routes"` // "endpoint (provenance)" per entry
}

// RelayDiagnosticsSnapshot assembles the bundle. Nothing here is secret:
// refs, health words, bucketed timings — no caps, no hints, no pins of
// OTHER people's relays beyond their public identity role.
func (r *Runtime) RelayDiagnosticsSnapshot() RelayDiagnostics {
	s := r.GetSettings()
	st := r.loadRelayState()
	d := RelayDiagnostics{RegistryVersion: BuiltinRelayRegistry().Version}
	// relayIsAutomatic, NOT a literal comparison — see its comment: it is the
	// one reading of the mode, and the difference is the whole fresh-install
	// case. A new node stores "" and means automatic; comparing to the string
	// made this screen report "custom, primary —" on a device that was at
	// that moment syncing happily through a measured official relay. The one
	// screen somebody opens to find out why the relay is not working was
	// telling them the selection had never run.
	if relayIsAutomatic(s) {
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
			if ep, ok := ref.Resolve(BuiltinRelayRegistry()); ok {
				d.PrimaryHealth = r.pool().health(ep)
				// Waiting on purpose is not the same as being broken, and
				// from outside they are identical: sync active, relay
				// healthy, nothing arriving.
				if left, yes := r.relayThrottled(ep); yes {
					d.ThrottledForMs = int(left.Milliseconds())
				}
				switch {
				case ref.Official != "":
					if desc, found := BuiltinRelayRegistry().ByID(ref.Official); found {
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
	d.Held = sync.Held
	d.WantHolds = sync.WantHolds

	// The route book, briefly (RT-0).
	d.Ingress = r.SelfIngressRoutes()
	r.mu.Lock()
	for dev, l := range r.lanPeers {
		if l == nil {
			continue
		}
		if closed, _ := l.Closed(); closed {
			continue
		}
		d.LocalPeers = append(d.LocalPeers, dev.Hex()[:8])
	}
	for dev, routes := range r.ks.PeerRoutes {
		brief := PeerRouteBrief{Device: dev.Hex()[:8]}
		for _, rt := range routes {
			brief.Routes = append(brief.Routes,
				rt.Endpoint+" ("+provenanceWord(rt.Provenance)+")")
		}
		if len(brief.Routes) == 0 {
			d.NoRoute++
			continue
		}
		d.Peers = append(d.Peers, brief)
	}
	// Fetch verdicts: what this node is pulling, waiting on, or done
	// asking for. Failed entries keep their reason; a silent entry is one
	// the person is probably staring at right now.
	for key := range r.assetIdx.fetching {
		st := "fetching"
		if _, quiet := r.assetIdx.silent[key]; quiet {
			st = "silent"
		}
		fb := FetchBrief{
			Space: key.Space.Hex()[:8], Asset: shortAsset(key.Asset), State: st}
		if ref := r.assetIdx.refs[key]; ref != nil {
			if res, err := assets.Missing(r.root, ref); err == nil {
				fb.Missing = len(res.MissingChunks)
				if res.ManifestMissing {
					fb.Missing = -1 // not even the manifest yet
				}
				fb.Total = res.TotalChunks
			}
		}
		d.Fetches = append(d.Fetches, fb)
	}
	for key, why := range r.assetIdx.failed {
		d.Fetches = append(d.Fetches, FetchBrief{
			Space: key.Space.Hex()[:8], Asset: shortAsset(key.Asset),
			State: "failed", Reason: string(why)})
	}
	r.mu.Unlock()
	sort.Slice(d.Peers, func(i, j int) bool { return d.Peers[i].Device < d.Peers[j].Device })
	sort.Strings(d.LocalPeers)
	sort.Slice(d.Fetches, func(i, j int) bool {
		a, b := d.Fetches[i], d.Fetches[j]
		return a.Space+a.Asset < b.Space+b.Asset
	})
	return d
}

// shortAsset trims an asset id to a correlatable prefix.
func shortAsset(a string) string {
	if len(a) > 8 {
		return a[:8]
	}
	return a
}

// provenanceWord names a provenance for a person reading diagnostics.
func provenanceWord(p uint8) string {
	switch p {
	case storage.RouteManual:
		return "manual"
	case storage.RouteInvitation:
		return "invitation"
	case storage.RouteAdvertised:
		return "advertised"
	case storage.RouteObserved:
		return "observed"
	case storage.RouteLegacy:
		return "legacy"
	}
	return "unknown"
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
		if ep, ok := ref.Resolve(BuiltinRelayRegistry()); ok {
			s := a.rt.GetSettings()
			a.rt.applyRelaySync(ep, relayInterval(s))
		}
	}
	writeJSON(w, map[string]any{
		"status": "measured", "primary": primary, "backup": backup,
	})
}
