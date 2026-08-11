// Per-purpose relay resolution (RR-4). ONE resolver with an explicit
// purpose instead of a universal fallback ladder — because a universal
// ladder falls back somewhere, and for a PUBLIC WRITE "somewhere" means
// quietly publishing a space's data to a personal relay no other member
// ever looks at. The rules:
//
//	personal        custom address, or the measured automatic primary
//	public READ     signed policy relays (RR-5) → observed SourceRelay →
//	                personal (a read probes; it cannot mislead others)
//	public WRITE    owner: policy relays → personal (pre-RR-5 the owner's
//	                choice IS the implicit relay set — it becomes qp.relay
//	                on creation once RR-5 lands);
//	                non-owner: policy relays → SourceRelay — NEVER personal
//	pass            ONLY passRecord.relay (per-record, see node/pass.go)
package node

import (
	"math"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// pickHealthy walks a resolution ladder and returns the first address
// the pool considers usable (RR-6). untrusted (pin mismatch — never
// auto-retried) and offline (cooling down) are skipped; nothing usable →
// "" — HOLD, because the durable pending set delivers later, and a bad
// substitute delivers never.
func (r *Runtime) pickHealthy(addrs []string) string {
	for _, a := range addrs {
		if a == "" {
			continue
		}
		switch r.pool().health(a) {
		case "untrusted", "offline":
			continue
		}
		return a
	}
	return ""
}

// personalRelayLadder is primary→backup for automatic mode, or the one
// configured address for custom.
func (r *Runtime) personalRelayLadder() []string {
	s := r.GetSettings()
	if !relayIsAutomatic(s) {
		if s.Relay == "" {
			return nil
		}
		return []string{s.Relay}
	}
	st := r.loadRelayState()
	var out []string
	for _, sref := range []string{st.SelectedPrimary, st.SelectedBackup} {
		if ref, err := ParseRelayRef(sref); err == nil {
			if ep, ok := ref.Resolve(BuiltinRelayRegistry()); ok {
				out = append(out, ep)
			}
		}
	}
	return out
}

// ResolvePersonalRelay is where THIS node's inbox lives: the configured
// custom address, or automatic mode's measured primary — falling over to
// the backup when the primary is unhealthy. "" = no relay.
func (r *Runtime) ResolvePersonalRelay() string {
	return r.pickHealthy(r.personalRelayLadder())
}

// policyRelaysOf returns the space's signed relay set (verified policy),
// resolved to dialable endpoints IN ORDER (first = primary). present
// reports whether the policy CARRIES a set at all — because "the set
// exists but this build cannot resolve any of it" (an unknown official
// id) is UNAVAILABLE, never an invitation to substitute someone's
// personal relay.
func (r *Runtime) policyRelaysOf(tid id.TerminalID) (addrs []string, present bool) {
	r.mu.Lock()
	st, ok := r.spaces[tid]
	if !ok {
		r.mu.Unlock()
		return nil, false
	}
	relays := st.space.Policy().Relays
	r.mu.Unlock()
	if len(relays) == 0 {
		return nil, false
	}
	for _, s := range relays {
		ref, err := ParseRelayRef(s)
		if err != nil {
			continue
		}
		if ep, ok := ref.Resolve(BuiltinRelayRegistry()); ok {
			addrs = append(addrs, ep)
		}
	}
	return addrs, true
}

func (r *Runtime) policyRelayOf(tid id.TerminalID) string {
	addrs, _ := r.policyRelaysOf(tid)
	if len(addrs) > 0 {
		return addrs[0]
	}
	return ""
}

func (r *Runtime) sourceRelayOf(tid id.TerminalID) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ks.Spaces[tid].SourceRelay
}

// ResolvePublicReadRelay picks where to FETCH a public space's projection.
// Reads may fall back to the personal relay — a wrong guess costs a
// probe and misleads nobody — UNLESS the space carries a relay set this
// build cannot resolve: an unknown official id is UNAVAILABLE, and the
// only remaining fallback is the space's own observed address.
func (r *Runtime) ResolvePublicReadRelay(tid id.TerminalID) string {
	addrs, present := r.policyRelaysOf(tid)
	if a := r.pickHealthy(addrs); a != "" {
		return a
	}
	if a := r.pickHealthy([]string{r.sourceRelayOf(tid)}); a != "" {
		return a
	}
	if present {
		return "" // an unresolvable set never routes to a personal relay
	}
	return r.ResolvePersonalRelay()
}

// ResolvePublicWriteRelay picks where a public space's data is PUBLISHED
// (owner Replace, contributor ingress uplink, mirror keepalive, seeding).
// A non-owner NEVER falls back to the personal relay: publishing where
// the members do not look is worse than not publishing — the pending set
// is durable and delivers when the real address is known.
func (r *Runtime) ResolvePublicWriteRelay(tid id.TerminalID) string {
	addrs, present := r.policyRelaysOf(tid)
	if a := r.pickHealthy(addrs); a != "" {
		return a
	}
	if present {
		return "" // a set exists but resolves to nothing: hold, never guess
	}
	r.mu.Lock()
	meta := r.ks.Spaces[tid]
	r.mu.Unlock()
	if meta.Owned {
		// A space with NO signed set: the owner's configured relay is the
		// implicit one (and becomes qp.relay on creation for new spaces).
		return r.ResolvePersonalRelay()
	}
	return r.pickHealthy([]string{meta.SourceRelay}) // "" = hold
}

// PersonalRelayRef names the personal relay AS a RelayRef — for signing
// into a NEW public space's policy (beta simple mode: the creator's
// relay becomes the space's qp.relay exactly once, at creation).
// Automatic mode signs the official ref, so the space follows registry
// endpoint moves without a policy revision; custom signs the literal
// endpoint.
func (r *Runtime) PersonalRelayRef() string {
	s := r.GetSettings()
	// relayIsAutomatic, not the literal string, and here it MATTERS MOST of
	// the three places that got this wrong. A fresh install stores "" and
	// means automatic; taking the custom branch found no configured address
	// and returned no ref at all — so a public space created on a
	// newly-installed device was signed with NOWHERE FOR ITS TRAFFIC TO GO,
	// and the person opening its link elsewhere had nothing to point at.
	if relayIsAutomatic(s) {
		st := r.loadRelayState()
		if ref, err := ParseRelayRef(st.SelectedPrimary); err == nil && !ref.IsZero() {
			return ref.String()
		}
		return ""
	}
	if s.Relay == "" {
		return ""
	}
	return RelayRef{Endpoint: s.Relay}.String()
}

// quicklinkCandidates orders the relays a quick link may be waiting on:
// the personal relay first, then registry entries by measured score —
// capped at 3, because "resolve anywhere" must never become "drain
// everywhere" (the destructive branch is sequential on top of this).
func (r *Runtime) quicklinkCandidates() []string {
	const maxCandidates = 3
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		if a != "" && !seen[a] && len(out) < maxCandidates {
			seen[a] = true
			out = append(out, a)
		}
	}
	add(r.ResolvePersonalRelay())
	st := r.loadRelayState()
	type cand struct {
		ep string
		s  float64
	}
	var cs []cand
	for _, d := range BuiltinRelayRegistry().Compatible(RelayProtocolVersionMin, RelayProtocolVersionMax) {
		s := math.Inf(1) // unmeasured sorts last but stays a candidate
		if ps := st.Stats[RelayRef{Official: d.ID}.String()]; ps != nil {
			s = relayScore(*ps)
		}
		cs = append(cs, cand{d.Endpoint, s})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].s < cs[j].s })
	for _, c := range cs {
		add(c.ep)
	}
	return out
}
