// Embedded Relay Registry (RR-0). The registry tells this build which
// relays the application KNOWS — never whom to trust with anything else:
// it cannot change membership, sign events, alter a space's relay set,
// or force traffic anywhere (R-I7). Official entries carry SPKI pin SETS
// so a key rotation ships as [current, next] → [next] without ever
// turning pinning off.
//
// The canonical way to NAME a relay is a RelayRef, fixed here before the
// first signed manifest ever carries one:
//
//	official:<id>            resolved through this registry — endpoint and
//	                         pins come from the build, so an official
//	                         relay may move hosts without any space
//	                         revising its policy
//	custom:tls://host:port   self-contained; identity via the local TOFU
//	                         record (confirmed pin), never the registry
//
// An unknown official:<id> is UNAVAILABLE — it is never treated as a
// custom endpoint and never falls back to a personal relay: a name this
// build cannot resolve must not quietly become somebody else's mailbox.
package node

import (
	"errors"
	"strings"
)

// Relay roles a descriptor may advertise. Strings, not iota — they are
// serialized into relays.json and must stay stable.
const (
	RelayRoleBootstrap       = "bootstrap"
	RelayRolePersonalInbox   = "personal-inbox"
	RelayRoleSpaceRendezvous = "space-rendezvous"
	RelayRolePublicHost      = "public-host"
)

// RelayDescriptor describes one relay this build knows about.
type RelayDescriptor struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"` // host:port, dialed over TLS

	Label  string `json:"label,omitempty"`
	Region string `json:"region,omitempty"`

	// Priority is an administrative tie-breaker only — measured score
	// always outranks it.
	Priority int `json:"priority"`

	ProtocolMin int `json:"protocol_min"`
	ProtocolMax int `json:"protocol_max"`

	Roles []string `json:"roles"`

	Official bool `json:"official"`

	// SPKIPins: base64 SHA-256 of the relay identity public key (SPKI).
	// A SET, so rotation is [current, next] → change key → [next].
	// Empty = local-lan profile (loopback/LAN — no pinning; identity on
	// those paths lives in event signatures, as everywhere else).
	SPKIPins []string `json:"spki_pins,omitempty"`
}

// LocalLAN reports whether this descriptor runs under the local-lan trust
// profile (no pinning): loopback or an unpinned non-official entry.
func (d RelayDescriptor) LocalLAN() bool {
	if len(d.SPKIPins) > 0 {
		return false
	}
	host := d.Endpoint
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// RelayRegistry is the embedded snapshot.
type RelayRegistry struct {
	Version int               `json:"version"`
	Relays  []RelayDescriptor `json:"relays"`
}

// ByID resolves an official id; ok=false for anything this build does
// not know.
func (rr RelayRegistry) ByID(id string) (RelayDescriptor, bool) {
	for _, d := range rr.Relays {
		if d.ID == id {
			return d, true
		}
	}
	return RelayDescriptor{}, false
}

// Compatible filters descriptors whose protocol range overlaps ours.
func (rr RelayRegistry) Compatible(protoMin, protoMax int) []RelayDescriptor {
	var out []RelayDescriptor
	for _, d := range rr.Relays {
		if d.ProtocolMax >= protoMin && d.ProtocolMin <= protoMax {
			out = append(out, d)
		}
	}
	return out
}

// BuiltinRelayRegistry is this build's snapshot. The public beta adds the
// four official regional entries here (pins from `terminal-relay`'s
// startup banner) — a constant edit, no architecture change.
var BuiltinRelayRegistry = RelayRegistry{
	Version: 1,
	Relays: []RelayDescriptor{
		{
			ID:          "local-dev",
			Endpoint:    "127.0.0.1:7411",
			Label:       "Local development relay",
			Region:      "local",
			Priority:    100,
			ProtocolMin: 1,
			ProtocolMax: 1,
			Roles: []string{
				RelayRoleBootstrap, RelayRolePersonalInbox,
				RelayRoleSpaceRendezvous, RelayRolePublicHost,
			},
			Official: true,
			// no pins: local-lan profile
		},
		{
			// The shared test relay. Named for what it IS: standing this box
			// up (ADR/plan, 2026-08-05) came with the warning that it must
			// never drift into being production infrastructure, and a label
			// saying "official EU relay" is exactly how that drift starts.
			//
			// It sits BELOW local-dev on priority, but priority is only an
			// administrative tie-break: selection is measured, so a relay on
			// this machine wins on RTT whenever it is up, and this one is
			// what a node falls to when it is not.
			ID:          "staging-1",
			Endpoint:    "91.201.114.71:7411",
			Label:       "Shared test relay",
			Region:      "eu",
			Priority:    50,
			ProtocolMin: 1,
			ProtocolMax: 1,
			Roles: []string{
				RelayRoleBootstrap, RelayRolePersonalInbox,
				RelayRoleSpaceRendezvous, RelayRolePublicHost,
			},
			Official: true,
			// Verified from the running box with `terminal relay
			// show-identity`, and identical to the pin recorded when it was
			// stood up — RR-1's persistent identity key held across every
			// restart since. A SET so rotation is [current, next].
			SPKIPins: []string{"A63rjukjUJkPVU98l0XPdKjRiDNXTVs1xCm9Xs7jyI4="},
		},
	},
}

// ---- RelayRef ----

// RelayRef names a relay in signed and stored state. Zero value = absent.
type RelayRef struct {
	// Official is the registry id when the ref is official:<id>.
	Official string
	// Endpoint is host:port when the ref is custom:tls://host:port.
	Endpoint string
}

var errBadRelayRef = errors.New("node: unreadable relay reference")

// ParseRelayRef parses the canonical grammar. It never dials and never
// consults the registry — an official ref may be parsed by a build that
// cannot resolve it (that resolution failure is a SEPARATE, honest state).
func ParseRelayRef(s string) (RelayRef, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "official:"):
		id := s[len("official:"):]
		if id == "" || strings.ContainsAny(id, " \t\n/:") {
			return RelayRef{}, errBadRelayRef
		}
		return RelayRef{Official: id}, nil
	case strings.HasPrefix(s, "custom:tls://"):
		ep := s[len("custom:tls://"):]
		if !plausibleHostPort(ep) {
			return RelayRef{}, errBadRelayRef
		}
		return RelayRef{Endpoint: ep}, nil
	}
	return RelayRef{}, errBadRelayRef
}

func (r RelayRef) String() string {
	switch {
	case r.Official != "":
		return "official:" + r.Official
	case r.Endpoint != "":
		return "custom:tls://" + r.Endpoint
	}
	return ""
}

func (r RelayRef) IsZero() bool { return r.Official == "" && r.Endpoint == "" }

// Resolve turns the ref into a dialable endpoint. An unknown official id
// resolves to nothing — the caller renders "unavailable", never a
// substitute relay.
func (r RelayRef) Resolve(reg RelayRegistry) (endpoint string, ok bool) {
	switch {
	case r.Official != "":
		d, found := reg.ByID(r.Official)
		if !found {
			return "", false
		}
		return d.Endpoint, true
	case r.Endpoint != "":
		return r.Endpoint, true
	}
	return "", false
}

// plausibleHostPort is a cheap structural check: host + ":" + numeric
// port, no spaces, no scheme. Real reachability is the prober's job.
func plausibleHostPort(s string) bool {
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return false
	}
	host, port := s[:i], s[i+1:]
	if strings.ContainsAny(host, " \t\n") || strings.ContainsAny(port, " \t\n") {
		return false
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(port) <= 5
}
