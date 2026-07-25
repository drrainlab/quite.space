// Connectivity policy (RB-1 steps 4-6): which ways out of this device a
// person has allowed, and the gate that enforces it.
//
// The gate runs BEFORE a connection is opened, not after. "We dialled the
// relay and then decided not to use it" still tells the relay operator that
// this device is awake, on this address, at this moment — which is exactly
// what someone choosing "Meshtastic only" is trying not to say. Refusing
// after the fact would be a setting that describes intent rather than
// enforces it.
package node

import (
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// ConnectivityMode is the person's choice of what may carry their messages.
type ConnectivityMode string

const (
	// ModeAuto uses whatever is available, preferring the cheap and fast.
	ModeAuto ConnectivityMode = "auto"
	// ModeInternetOnly: relay and LAN, never the radio.
	ModeInternetOnly ConnectivityMode = "internet"
	// ModeMeshtasticOnly: the radio and nothing else. No relay connection
	// is opened at all.
	ModeMeshtasticOnly ConnectivityMode = "meshtastic"
	// ModeOffline: nothing leaves the device. Messages are still written
	// and still tracked; they wait.
	ModeOffline ConnectivityMode = "offline"
)

// Valid reports whether a mode is one this build understands. An unknown
// mode falls back to Auto rather than silently disabling every transport —
// a typo in a config file must not look like a network outage.
func (m ConnectivityMode) Valid() bool {
	switch m {
	case ModeAuto, ModeInternetOnly, ModeMeshtasticOnly, ModeOffline:
		return true
	}
	return false
}

// Connectivity is the persisted policy. PerSpace is in the data model from
// the start even though the UI does not expose it yet: retrofitting a
// per-space override onto a global-only setting means migrating stored
// config, and the shape costs nothing now.
type Connectivity struct {
	Mode     ConnectivityMode            `json:"mode"`
	PerSpace map[string]ConnectivityMode `json:"per_space,omitempty"`
}

// modeFor resolves the effective mode for a space.
func (c Connectivity) modeFor(tid id.TerminalID) ConnectivityMode {
	if m, ok := c.PerSpace[tid.Hex()]; ok && m.Valid() {
		return m
	}
	if c.Mode.Valid() {
		return c.Mode
	}
	return ModeAuto
}

// allows reports whether a transport may be used for a space.
func (c Connectivity) allows(k TransportKind, tid id.TerminalID) bool {
	switch c.modeFor(tid) {
	case ModeOffline:
		return false
	case ModeMeshtasticOnly:
		return k == TransportRadio
	case ModeInternetOnly:
		return k == TransportRelay || k == TransportLAN
	}
	return true // auto
}

// connectivity reads the current policy. Caller must not hold r.mu.
func (r *Runtime) connectivity() Connectivity {
	return r.GetSettings().Connectivity
}

// TransportAllowed is the gate. It is consulted before a transport is
// opened OR used, so a forbidden transport never produces a connection,
// a packet, or a trace of this device on someone else's server.
func (r *Runtime) TransportAllowed(k TransportKind, tid id.TerminalID) bool {
	return r.connectivity().allows(k, tid)
}

// anySpaceAllows answers the same question for transports that are not
// per-space — a relay connection carries every space at once, so it is
// permitted when ANY space would use it. Refusing on the strictest space
// would let one room's setting silence the others.
func (r *Runtime) anySpaceAllows(k TransportKind) bool {
	c := r.connectivity()
	r.mu.Lock()
	tids := make([]id.TerminalID, 0, len(r.spaces))
	for tid := range r.spaces {
		tids = append(tids, tid)
	}
	r.mu.Unlock()
	if len(tids) == 0 {
		return c.allows(k, id.TerminalID{})
	}
	for _, tid := range tids {
		if c.allows(k, tid) {
			return true
		}
	}
	return false
}

// radioOutboundCap is what a frame must fit in to be worth putting on the
// air. A var so a future radio profile can raise it and every derived
// eligibility answer changes with it.
var radioOutboundCap = func() int { return routing.BetaOutboundCap }

// ErrTransportBlocked is returned when policy forbids the transport a
// caller asked for. It is deliberately distinct from a network failure:
// "you told me not to" and "it did not work" are different things to show
// a person, and merging them produces a client that looks broken when it
// is obeying instructions.
type ErrTransportBlocked struct {
	Transport TransportKind
	Mode      ConnectivityMode
}

func (e ErrTransportBlocked) Error() string {
	return "node: " + e.Transport.String() + " is not permitted in " +
		string(e.Mode) + " mode"
}

// DeliveryView is the honest projection for one tracked message: what this
// device is responsible for, what has actually been proven, and which way
// out is carrying it. It extends nothing on the protocol ladder — Proof is
// the ADR-007 level and nothing here may raise it.
type DeliveryView struct {
	EventID   id.EventID
	Space     id.TerminalID
	State     string
	Proof     string
	Transport string
	// Waiting names a transport that cannot carry this message and why, so
	// the UI can say "waiting for a faster link" instead of showing a retry
	// that will never succeed.
	Waiting string
	Attempt string
	Lease   string
	// LeaseExpires is when the gateway's promise runs out, 0 if none.
	LeaseExpires int64
}

// Delivery projects the tracked state of one event.
func (r *Runtime) Delivery(eid id.EventID) (DeliveryView, bool) {
	r.mu.Lock()
	lg := r.ledger
	r.mu.Unlock()
	if lg == nil {
		return DeliveryView{}, false
	}
	in, ok := lg.Get(eid)
	if !ok {
		return DeliveryView{}, false
	}
	v := DeliveryView{
		EventID:      in.EventID,
		Space:        in.Space,
		State:        in.State.String(),
		Proof:        in.Proof.String(),
		Transport:    in.Transport,
		Attempt:      in.Attempt.Hex(),
		Lease:        in.Lease,
		LeaseExpires: in.LeaseExpires,
	}
	mode := r.connectivity().modeFor(in.Space)
	if in.BlockedOn(TransportRadio) == BlockTooLarge &&
		(mode == ModeMeshtasticOnly || !r.anySpaceAllows(TransportRelay)) {
		v.Waiting = "faster_link"
	}
	return v, true
}

// eligibleTransports lists the ways out that policy allows AND this
// message can actually use, in preference order. Empty means the message
// waits — which is a state, not an error.
func (r *Runtime) eligibleTransports(in DeliveryIntent) []TransportKind {
	c := r.connectivity()
	var out []TransportKind
	for _, k := range []TransportKind{TransportLAN, TransportRelay, TransportRadio} {
		if !c.allows(k, in.Space) {
			continue
		}
		if !in.EligibleOn(k) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// transportOfLink maps a pump label onto a transport kind.
func transportOfLink(label string) TransportKind {
	switch {
	case label == "radio":
		return TransportRadio
	case strings.HasPrefix(label, "relay"):
		return TransportRelay
	case label == "lan":
		return TransportLAN
	}
	return TransportAny
}

// relayGate refuses a relay operation before any connection is opened.
func (r *Runtime) relayGate() error {
	if r.anySpaceAllows(TransportRelay) {
		return nil
	}
	return ErrTransportBlocked{Transport: TransportRelay, Mode: r.connectivity().Mode}
}

// hysteresis keeps Auto from flapping between transports the moment one
// twitches. A link that has just come back is not yet trusted; a link that
// has just failed is not immediately abandoned.
const transportHysteresis = 20 * time.Second

// stableSince reports whether a transport has held its current state long
// enough for Auto to act on it. Caller holds r.mu.
func (r *Runtime) stableSince(k TransportKind, up bool, now time.Time) bool {
	if r.transportFlap == nil {
		r.transportFlap = map[TransportKind]transportState{}
	}
	cur, ok := r.transportFlap[k]
	if !ok || cur.up != up {
		r.transportFlap[k] = transportState{up: up, since: now}
		return false
	}
	return now.Sub(cur.since) >= transportHysteresis
}

type transportState struct {
	up    bool
	since time.Time
}
