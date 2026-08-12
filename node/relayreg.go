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
	"sync/atomic"
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

	// ManualOnly keeps an entry out of AUTOMATIC selection while leaving it
	// fully resolvable. It exists for one kind of row: a relay that is right
	// for whoever deliberately stood it up and wrong for everybody else.
	//
	// Measured selection has no way to tell those apart on its own. A relay
	// on the same machine wins on RTT against every relay in the world, and
	// winning is exactly the wrong outcome — a relay's value is that OTHER
	// PEOPLE ARE THERE, and a mailbox nobody else uses is not a meeting
	// place. The beta saw the consequence as a quicklink refused with
	// "nothing waiting under those words", which was true: the words were
	// waiting on the shared relay, and that phone was not.
	//
	// A tag on the row rather than a rule about loopback addresses, because
	// the distinguishing fact is what the entry is FOR. A relay somebody runs
	// on their LAN for a group in one building is not loopback and should be
	// selectable; the development entry below is on loopback and should not.
	ManualOnly bool `json:"manual_only,omitempty"`
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

// BuiltinRelayRegistry returns this build's registry snapshot. A FUNCTION,
// not a var, loaded atomically: the automatic-selection goroutine reads the
// registry in the background while the test suite swaps it per test
// (withRelayRegistry) — a plain global was the suite's one data race.
// Production never writes it; the setter exists for tests.
func BuiltinRelayRegistry() RelayRegistry {
	return relayRegistryV.Load().(RelayRegistry)
}

func setBuiltinRelayRegistry(reg RelayRegistry) { relayRegistryV.Store(reg) }

// Initialized by var-dependency order: the shipped literal below is built
// first, then this atomic wraps it — before any init() or test runs.
var relayRegistryV = func() *atomic.Value {
	v := new(atomic.Value)
	v.Store(shippedBuiltinRegistry)
	return v
}()

// shippedBuiltinRegistry is the snapshot compiled into the binary. The
// public beta adds the official regional entries here (pins from
// `terminal-relay`'s startup banner) — a constant edit, no architecture
// change.
var shippedBuiltinRegistry = RelayRegistry{
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
			//
			// AND NEVER CHOSEN FOR ANYBODY. This row is here so a developer
			// can say `official:local-dev`, and its Priority of 100 is the
			// highest in the file — which, before ManualOnly, meant any
			// machine with something answering on 7411 selected it on the
			// spot and stopped meeting anyone.
			ManualOnly: true,
		},
		{
			// Amsterdam. It began as AR-0c's staging box, with the warning
			// that it must never drift into being production infrastructure —
			// and then it did, because it is the relay every beta build has
			// been meeting on. The id is kept as it is: changing a registry
			// id orphans every space whose signed policy already names it.
			//
			// It sits BELOW local-dev on priority, but priority is only an
			// administrative tie-break: selection is measured, and local-dev
			// is not a candidate at all (ManualOnly).
			ID:          "staging-1",
			Endpoint:    "91.201.114.71:7411",
			Label:       "Amsterdam",
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
		{
			// THE SECOND REGION, and the first time the backup rule has
			// anything to choose from: runAutoSelection prefers a backup in a
			// DIFFERENT region, because two relays in one failure domain are
			// one relay with extra steps. Until now there was only eu.
			//
			// Same priority as Amsterdam on purpose. Neither is "the main
			// one" — every device measures the real path and picks for
			// itself, which is the whole point of automatic mode, and a tie
			// here means nothing overrides that measurement.
			ID:          "ru-1",
			Endpoint:    "178.20.45.239:7411",
			Label:       "Russia",
			Region:      "ru",
			Priority:    50,
			ProtocolMin: 1,
			ProtocolMax: 1,
			Roles: []string{
				RelayRoleBootstrap, RelayRolePersonalInbox,
				RelayRoleSpaceRendezvous, RelayRolePublicHost,
			},
			Official: true,
			// Read off the running box AND verified over the wire from a
			// second machine with `terminal relay show-identity` — the log
			// line says what the process believes, the handshake says what a
			// client will actually be offered. Confirmed to survive a restart
			// (RR-1's persistent key, /var/lib/quiet-relay-ru). A SET so
			// rotation ships as [current, next] → [next].
			SPKIPins: []string{"gk0X84yjjmahVYaUO7snq/a/BecbtX4deDFp7hGkC/c="},
		},
		{
			// THE DIRECTORY'S HOME. This is the relay the official catalog
			// lives on, and that makes it different from the two above in a
			// way worth writing down: a share link is relay-bound and
			// irrevocable, so the address in this entry is baked into every
			// catalog link ever handed out. It can gain a backup pin; it
			// cannot move.
			//
			// It is an ORDINARY relay in every other respect — same roles,
			// same priority, blind like the rest, and no device is obliged to
			// use it for anything of its own. Hosting the catalog is not a
			// privilege the protocol knows about.
			ID:          "catalog-1",
			Endpoint:    "195.63.160.237:7411",
			Label:       "Catalog",
			Region:      "eu",
			Priority:    50,
			ProtocolMin: 1,
			ProtocolMax: 1,
			Roles: []string{
				RelayRoleBootstrap, RelayRolePersonalInbox,
				RelayRoleSpaceRendezvous, RelayRolePublicHost,
			},
			Official: true,
			// Printed by the process at startup AND fetched back over the
			// internet from a second machine with `terminal relay
			// show-identity` before it was written here — the log says what
			// the process believes, the handshake says what a client is
			// actually offered, and only the second one is evidence.
			SPKIPins: []string{"nPKCFXxKyvcwaUcNVB1Ibn7xalO1aNRiHfMNXueQgD4="},
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
