// The route book at runtime (RT-0): who can be reached where, and where this
// device is obliged to listen.
//
// Two DIRECTED tables — see kernel/storage/routes.go for the model and the
// inversion this design exists to end. This file owns every mutation, so the
// rules live in one place:
//
//   - A peer route is CREATED only by an explicit statement (the sealed
//     invitation exchange today, signed advertisements in T3) or by the
//     one-time legacy backfill. Never by observing where a frame arrived.
//   - Self ingress GROWS when this device advertises an endpoint to someone
//     — whoever was told "answer me at B" is entitled to keep answering at
//     B, so an advertised endpoint is listened on until a migration wave
//     (T4) can retire it honestly.
package node

import (
	"encoding/json"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// routeRank orders peer-route provenance by how much it should be believed
// for DELIVERY. Lower wins. Distinct from the constants' storage values,
// which are frozen on disk.
func routeRank(p uint8) int {
	switch p {
	case storage.RouteInvitation:
		return 0
	case storage.RouteAdvertised:
		return 1
	case storage.RouteObserved:
		return 2
	case storage.RouteManual:
		return 3
	case storage.RouteLegacy:
		return 4
	}
	return 5
}

// recordPeerRouteLocked upserts one route for one peer device. Caller holds
// r.mu; persistence rides whatever keystore commit the caller already owes.
//
// Same endpoint twice: the STRONGER provenance wins and LastSeen refreshes —
// a legacy guess must never shadow a route the peer stated themselves.
func (r *Runtime) recordPeerRouteLocked(dev id.DeviceID, endpoint, transport string, provenance uint8) {
	if endpoint == "" || dev == r.Device.ID {
		return
	}
	now := time.Now().Unix()
	if r.ks.PeerRoutes == nil {
		r.ks.PeerRoutes = map[id.DeviceID][]storage.Route{}
	}
	routes := r.ks.PeerRoutes[dev]
	for i := range routes {
		if routes[i].Endpoint == endpoint && routes[i].Transport == transport {
			if routeRank(provenance) < routeRank(routes[i].Provenance) {
				routes[i].Provenance = provenance
			}
			routes[i].LastSeen = now
			r.ks.PeerRoutes[dev] = routes
			return
		}
	}
	r.ks.PeerRoutes[dev] = append(routes, storage.Route{
		Endpoint: endpoint, Transport: transport,
		Provenance: provenance, LearnedAt: now, LastSeen: now,
	})
}

// recordSelfIngressLocked remembers that this device advertised (or was
// configured with) an ingress endpoint. Caller holds r.mu.
func (r *Runtime) recordSelfIngressLocked(endpoint string, provenance uint8) {
	if endpoint == "" {
		return
	}
	now := time.Now().Unix()
	for i := range r.ks.SelfIngress {
		if r.ks.SelfIngress[i].Endpoint == endpoint {
			r.ks.SelfIngress[i].LastSeen = now
			return
		}
	}
	r.ks.SelfIngress = append(r.ks.SelfIngress, storage.Route{
		Endpoint: endpoint, Transport: "relay",
		Provenance: provenance, LearnedAt: now, LastSeen: now,
	})
}

// PeerRoutesFor returns the endpoints this node may DELIVER to for one peer
// device, strongest provenance first, unhealthy endpoints dropped. Empty
// means NO ROUTE — the caller holds, visibly; it never substitutes its own
// relay and hopes.
func (r *Runtime) PeerRoutesFor(dev id.DeviceID) []string {
	r.mu.Lock()
	routes := append([]storage.Route(nil), r.ks.PeerRoutes[dev]...)
	r.mu.Unlock()
	if len(routes) == 0 {
		return nil
	}
	// Insertion sort by rank, then recency — the lists are tiny.
	for i := 1; i < len(routes); i++ {
		for j := i; j > 0; j-- {
			a, b := routes[j-1], routes[j]
			if routeRank(a.Provenance) < routeRank(b.Provenance) ||
				(routeRank(a.Provenance) == routeRank(b.Provenance) && a.LastSeen >= b.LastSeen) {
				break
			}
			routes[j-1], routes[j] = routes[j], routes[j-1]
		}
	}
	out := make([]string, 0, len(routes))
	for _, rt := range routes {
		if rt.Transport != "relay" {
			continue // LAN/radio candidates join the resolver in T6
		}
		switch r.pool().health(rt.Endpoint) {
		case "untrusted", "offline":
			continue
		}
		out = append(out, rt.Endpoint)
	}
	return out
}

// SelfIngressRoutes is every endpoint this device must LISTEN on: the
// current personal relay plus everything it has ever advertised to a peer.
// Deduped, personal first. Never derived from PeerRoutes — that inversion
// is the bug class the route book ends.
func (r *Runtime) SelfIngressRoutes() []string {
	personal := r.ResolvePersonalRelay()
	r.mu.Lock()
	stored := append([]storage.Route(nil), r.ks.SelfIngress...)
	r.mu.Unlock()

	seen := map[string]struct{}{}
	var out []string
	add := func(ep string) {
		if ep == "" {
			return
		}
		if _, dup := seen[ep]; dup {
			return
		}
		seen[ep] = struct{}{}
		out = append(out, ep)
	}
	add(personal)
	for _, rt := range stored {
		add(rt.Endpoint)
	}
	return out
}

// advertisedRoutes is what this device tells a peer about itself during the
// invitation exchange — and SAYING it creates the obligation to listen
// there, so the advertisement is recorded into SelfIngress in the same
// breath. Caller holds r.mu.
func (r *Runtime) advertisedRoutesLocked() []string {
	// The personal relay in force right now. Resolved without the lock
	// wrapper because callers already hold r.mu — read the ladder directly.
	var ep string
	if s := settingsFromLocked(r); !relayIsAutomatic(s) {
		ep = s.Relay
	} else if st := r.loadRelayState(); st.SelectedPrimary != "" {
		if ref, err := ParseRelayRef(st.SelectedPrimary); err == nil {
			if resolved, ok := ref.Resolve(BuiltinRelayRegistry()); ok {
				ep = resolved
			}
		}
	}
	if ep == "" {
		return nil
	}
	r.recordSelfIngressLocked(ep, storage.RouteManual)
	return []string{ep}
}

// settingsFromLocked reads the settings blob while r.mu is held.
func settingsFromLocked(r *Runtime) Settings {
	var s Settings
	if len(r.ks.Settings) > 0 {
		_ = json.Unmarshal(r.ks.Settings, &s)
	}
	return s
}

// backfillLegacyRoutesLocked runs once, at open, for a node whose route book
// is empty but whose keystore already holds shared spaces: every known
// member device gets ONE recorded route — the personal relay in force at
// migration — with provenance legacy.
//
// This is a RECORD of the single-relay era, not a live fallback: it is what
// makes yesterday's installations keep talking after the upgrade, because
// yesterday everybody genuinely was on one relay. A route learned any real
// way outranks it, and for spaces joined after the upgrade it never fires.
func (r *Runtime) backfillLegacyRoutesLocked() {
	if len(r.ks.PeerRoutes) > 0 || len(r.ks.SelfIngress) > 0 {
		return // the book exists; migration is history
	}
	var ep string
	if s := settingsFromLocked(r); !relayIsAutomatic(s) {
		ep = s.Relay
	} else if st := r.loadRelayState(); st.SelectedPrimary != "" {
		if ref, err := ParseRelayRef(st.SelectedPrimary); err == nil {
			if resolved, ok := ref.Resolve(BuiltinRelayRegistry()); ok {
				ep = resolved
			}
		}
	}
	if ep == "" {
		return // nothing was ever configured; nothing to migrate
	}
	stamped := false
	for tid, st := range r.spaces {
		if r.ks.Spaces[tid].LocalOnly {
			continue
		}
		for dev := range st.space.Members() {
			if dev == r.Device.ID {
				continue
			}
			r.recordPeerRouteLocked(dev, ep, "relay", storage.RouteLegacy)
			stamped = true
		}
		// Author devices seen in the log — the only member knowledge a
		// non-owner replica has.
		devs := map[id.DeviceID]struct{}{}
		_ = st.space.Log.Replay(func(a eventlog.Applied) error {
			devs[a.Env.Device] = struct{}{}
			return nil
		})
		for dev := range devs {
			if dev == r.Device.ID {
				continue
			}
			r.recordPeerRouteLocked(dev, ep, "relay", storage.RouteLegacy)
			stamped = true
		}
	}
	if stamped {
		r.recordSelfIngressLocked(ep, storage.RouteLegacy)
	}
}
