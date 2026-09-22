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
	"net"
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

	// A STATEMENT DISPLACES EVERY GUESS. A legacy entry is not knowledge —
	// it is the single-relay assumption written down (open-time backfill,
	// or history from before this rule). Once anything real is known for
	// this device, keeping the guess around would let PeerRoutesFor's
	// health filter fall back to a healthy-but-wrong relay the moment the
	// stated one blinks — RT-0's banned re-aim, wearing a fallback's
	// clothes. Deleted, not merely outranked.
	if routeRank(provenance) < routeRank(storage.RouteLegacy) {
		kept := routes[:0]
		for _, rt := range routes {
			if rt.Provenance != storage.RouteLegacy {
				kept = append(kept, rt)
			}
		}
		if len(kept) != len(routes) {
			routes = append([]storage.Route(nil), kept...)
			// STRONGER KNOWLEDGE INVALIDATES DELIVERY MADE ON WEAKER. Any
			// space whose cursor last advanced on a legacy basis re-offers
			// from zero — the RR-6 idiom (re-push is idempotent by EventID
			// dedup), never a compensating decrement.
			//
			// Bumped only when a guess was actually displaced: since
			// content pushes started announcing their sender's ingress,
			// this records on every push — and a generation that ticks
			// without knowledge changing re-offers the world for nothing.
			r.routeKnowledgeGen++
		}
	}

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
	// A book is a record, not a dumping ground: the list is bounded, and
	// the guessy end of it is what gives way (mailbox-injected spam must
	// not be able to grow it without limit).
	const maxRoutesPerDevice = 6
	if len(routes) >= maxRoutesPerDevice {
		worst, at := -1, -1
		for i, rt := range routes {
			if rk := routeRank(rt.Provenance); rk > worst {
				worst, at = rk, i
			}
		}
		if at >= 0 && worst >= routeRank(provenance) {
			routes = append(routes[:at], routes[at+1:]...)
		} else {
			return // the newcomer is the weakest of them all: not recorded
		}
	}
	r.ks.PeerRoutes[dev] = append(routes, storage.Route{
		Endpoint: endpoint, Transport: transport,
		Provenance: provenance, LearnedAt: now, LastSeen: now,
	})
}

// RouteKnowledgeGen ticks every time a stated route displaces guesswork;
// the sync loop watches it to re-offer legacy-basis deliveries.
func (r *Runtime) RouteKnowledgeGen() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.routeKnowledgeGen
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
	ranked := r.rankedPeerRoutes(dev)
	out := make([]string, 0, len(ranked))
	for _, rt := range ranked {
		out = append(out, rt.Endpoint)
	}
	return out
}

// rankRoutes copies and orders a device's routes: best provenance first,
// most recently seen first within a rank. Pure; the lists are tiny.
func rankRoutes(in []storage.Route) []storage.Route {
	routes := append([]storage.Route(nil), in...)
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
	return routes
}

// rankedPeerRoutes is PeerRoutesFor with the provenance kept: stated
// routes for one device, best knowledge first, dead relays filtered.
// Delivery accounting needs the provenance — a copy sent on invitation
// knowledge and a copy sent on a legacy assumption must not be recorded
// as the same kind of success.
func (r *Runtime) rankedPeerRoutes(dev id.DeviceID) []storage.Route {
	r.mu.Lock()
	routes := rankRoutes(r.ks.PeerRoutes[dev])
	r.mu.Unlock()
	if len(routes) == 0 {
		return nil
	}
	out := make([]storage.Route, 0, len(routes))
	own := r.ownWorld()
	for _, rt := range routes {
		if !routableFrom(rt.Endpoint, own) {
			// A peer's loopback address reaches nothing from here. Kept in
			// the book (it is what they said), never dialled: on the owner's
			// phone a stand node's stale 127.0.0.1 route was the ONLY route
			// for that peer, every push failed on it and every word waited
			// for the cycle's fallback — ten seconds, measured, on a good
			// network.
			continue
		}
		if rt.Transport != "relay" {
			continue // LAN/radio candidates join the resolver in T6
		}
		switch r.pool().health(rt.Endpoint) {
		case "untrusted", "offline":
			continue
		}
		out = append(out, rt)
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

// routableFrom says whether a peer's stated endpoint can be dialled from
// this node. Loopback and unspecified addresses are somebody else's
// machine — unless this node's own relay is loopback too, which is the
// test bench and a developer's laptop, where everybody shares one machine.
// ownWorld is the relay address this node would NAME — configured or
// selected, health not consulted — which is the only sane input to "which
// world am I in". The health-filtered resolver answers "" the moment the
// dial breaker trips, and "" means "bench" to routableFrom: under load a
// peer's loopback statement slipped into the book exactly then (found by
// the race detector, 3 in 6). Whether the relay answers right now has
// nothing to do with whether 127.0.0.1 is somebody's address.
func (r *Runtime) ownWorld() string { return r.PersonalRelayAddress() }

func routableFrom(endpoint, ownRelay string) bool {
	if endpoint == "" {
		return false
	}
	if !loopbackAddr(endpoint) && !unspecifiedAddr(endpoint) {
		return true
	}
	// A node that does not yet know its own relay cannot tell which world
	// it is in; it dials what it was told rather than refuse on a guess.
	return ownRelay == "" || loopbackAddr(ownRelay)
}

func unspecifiedAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// courtesyRoute is where a plane that speaks to a device UNPROMPTED — a
// receipt, a grant, a knock — puts its item: the device's best dialable
// stated route, or this node's own relay as the courtesy when it states
// none (guessed = true). Never the raw book: a stale or unroutable entry in
// first position used to catch every receipt (LT-4 S2). Takes r.mu itself.
func (r *Runtime) courtesyRoute(dev id.DeviceID) (ep string, guessed bool) {
	if ranked := r.rankedPeerRoutes(dev); len(ranked) > 0 {
		return ranked[0].Endpoint, false
	}
	return r.ResolvePersonalRelay(), true
}

// dialableStatedLocked is courtesyRoute's stated half for a caller that
// already holds r.mu: the first stated relay route a peer could dial, or "".
func (r *Runtime) dialableStatedLocked(dev id.DeviceID, own string) string {
	for _, rt := range rankRoutes(r.ks.PeerRoutes[dev]) {
		if rt.Transport == "relay" && routableFrom(rt.Endpoint, own) {
			return rt.Endpoint
		}
	}
	return ""
}

// advertisable is the part of this node's ingress list a peer elsewhere
// could dial: everything, on a bench where the own relay is loopback too.
func advertisable(ingress []string, own string) []string {
	out := ingress[:0:0]
	for _, ep := range ingress {
		if routableFrom(ep, own) {
			out = append(out, ep)
		}
	}
	return out
}

// statableEndpoint says whether a peer's stated endpoint may enter the
// book at all: well-formed, a real port, and dialable from here.
func statableEndpoint(ep, own string) bool {
	host, port, err := net.SplitHostPort(ep)
	if err != nil || host == "" || port == "" || port == "0" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()) {
		return false
	}
	return routableFrom(ep, own)
}
