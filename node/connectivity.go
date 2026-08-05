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
	"errors"
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
	// ModeRadioOnly: the radio and nothing else. No relay connection is
	// opened at all.
	//
	// The value used to be "meshtastic", which named one CARRIER inside an
	// enum about transports. A second radio driver would have had to either
	// answer to another vendor's name or need a migration of everybody's
	// settings file; renaming the value now, while the old spelling is still
	// accepted on read forever, costs nothing and closes both.
	ModeRadioOnly ConnectivityMode = "radio"

	// ModeMeshtasticOnly is the original spelling, kept as an alias so a
	// settings file written by any earlier build keeps meaning what it meant.
	// It is never WRITTEN again — see normalizeMode.
	ModeMeshtasticOnly ConnectivityMode = "meshtastic"
	// ModeOffline: nothing leaves the device. Messages are still written
	// and still tracked; they wait.
	ModeOffline ConnectivityMode = "offline"
)

// Valid reports whether a mode is one this build understands.
func (m ConnectivityMode) Valid() bool {
	switch m {
	case ModeAuto, ModeInternetOnly, ModeRadioOnly, ModeMeshtasticOnly, ModeOffline:
		return true
	}
	return false
}

// normalizeMode folds the legacy spelling onto the current one, so everything
// downstream compares against a single value.
func normalizeMode(m ConnectivityMode) ConnectivityMode {
	if m == ModeMeshtasticOnly {
		return ModeRadioOnly
	}
	return m
}

// ErrBadConnectivityMode refuses a mode this build does not understand,
// at the moment someone tries to store it.
type ErrBadConnectivityMode struct{ Mode ConnectivityMode }

func (e ErrBadConnectivityMode) Error() string {
	return "node: unknown connectivity mode " + string(e.Mode) +
		" (want auto, internet, meshtastic or offline)"
}

// Validate rejects an unreadable policy rather than interpreting it.
//
// An UNSET mode is a fresh install and means Auto. An unknown mode is
// something else entirely: a typo, a downgrade, a corrupted file. Falling
// back to Auto there would WIDEN what is permitted — "meshtastic_onyl"
// would quietly open an internet relay for someone who was explicitly
// trying not to have one. Refusing to store it keeps the typo out; refusing
// to act on it (see modeFor) keeps a corrupted file from doing the same.
func (c Connectivity) Validate() error {
	if c.Mode != "" && !c.Mode.Valid() {
		return ErrBadConnectivityMode{Mode: c.Mode}
	}
	for _, m := range c.PerSpace {
		if m != "" && !m.Valid() {
			return ErrBadConnectivityMode{Mode: m}
		}
	}
	return nil
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
//
// Unset means Auto — the fresh-install default. UNREADABLE means hold: a
// value this build cannot interpret is never resolved to something more
// permissive than what it might have meant. Holding is visible (see
// ConnectivityStatus) and recoverable by re-choosing; widening would be
// neither, and would be discovered by the relay operator rather than by
// the person who set it.
func (c Connectivity) modeFor(tid id.TerminalID) ConnectivityMode {
	// NORMALISED at the one place every read passes through, so the legacy
	// spelling never has to be remembered anywhere else.
	if m, ok := c.PerSpace[tid.Hex()]; ok {
		if m.Valid() {
			return normalizeMode(m)
		}
		return ModeOffline
	}
	switch {
	case c.Mode == "":
		return ModeAuto
	case c.Mode.Valid():
		return normalizeMode(c.Mode)
	}
	return ModeOffline
}

// ConnectivityStatus is what the UI needs to explain the current state,
// including the case where the stored policy cannot be read.
type ConnectivityStatus struct {
	Mode ConnectivityMode `json:"mode"`
	// Unreadable is set when the stored policy names a mode this build does
	// not understand. Nothing is being sent, and it is NOT a network
	// problem — the person has to re-choose.
	Unreadable bool   `json:"unreadable,omitempty"`
	Stored     string `json:"stored,omitempty"`
}

// Connectivity reports the effective policy and whether it is readable.
func (r *Runtime) Connectivity() ConnectivityStatus {
	c := r.connectivity()
	st := ConnectivityStatus{Mode: c.modeFor(id.TerminalID{})}
	if c.Mode != "" && !c.Mode.Valid() {
		st.Unreadable = true
		st.Stored = string(c.Mode)
	}
	return st
}

// allows reports whether a transport may be used for a space.
func (c Connectivity) allows(k TransportKind, tid id.TerminalID) bool {
	switch c.modeFor(tid) {
	case ModeOffline:
		return false
	case ModeRadioOnly:
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
//
// Kept as the FLOOR for a node with no radio attached: there is nothing to
// ask, and a constant is the only honest answer left.
var radioOutboundCap = func() int { return routing.BetaOutboundCap }

// radioCarryCeiling asks the ATTACHED RADIO what it will carry.
//
// One ceiling, or none. The carrier derives its own from its own airtime
// (MaxEventBytes), and the moment this ledger answers from a separate
// constant the two disagree — which is not an abstract tidiness problem: at
// 1536 against a carrier's 2586 the delivery view would tell somebody their
// message is "waiting for a faster link" while sync cheerfully carried it.
// A person reading a status that contradicts what happened stops reading the
// status.
func (r *Runtime) radioCarryCeiling() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.radioCarryCeilingLocked()
}

// radioCarryCeilingLocked is the same answer for callers already holding
// r.mu — the pattern this package already uses for assetStatusLocked, and for
// the same reason: the alternative is a deadlock nobody sees until the one
// path that takes both locks runs.
func (r *Runtime) radioCarryCeilingLocked() int {
	if r.rnodeEP != nil {
		if n := r.rnodeEP.Capabilities().MaxEventBytes; n > 0 {
			return n
		}
	}
	return radioOutboundCap()
}

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
	EventID id.EventID
	Space   id.TerminalID
	State   string
	Proof   string
	// Transport is the route that last carried it; Route is the one that
	// would carry it now. They differ while a route is failing over.
	Transport string
	Route     string
	// Waiting says why nothing is moving, when nothing is: "connectivity"
	// when policy permits no route, "faster_link" when the message cannot
	// fit the only route permitted. Empty when something is in hand — a
	// message in a gateway's custody is not waiting on us.
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
	// Why is nothing happening? The three answers a person can act on are
	// different, and collapsing them into "pending" is what makes a client
	// feel broken: they turned something off, this message needs a bigger
	// pipe, or a gateway is holding it and there is nothing to do but wait.
	if k, ok := r.SelectTransport(in, time.Now()); ok {
		v.Route = k.String()
	} else {
		// Order matters. If policy permits no route at all, a faster link
		// would not help and saying so would send someone hunting for a
		// better connection when they had simply switched everything off.
		// Only when something IS permitted, and this message cannot use it,
		// is size the answer.
		v.Waiting = "connectivity"
		c := r.connectivity()
		for _, k := range transportPreference {
			if c.allows(k, in.Space) {
				if in.blockedOn(k, r.radioCarryCeiling()) != BlockNone {
					v.Waiting = "faster_link"
				}
				break
			}
		}
	}
	if in.State == IntentCustody {
		v.Waiting = "" // someone else has it; that is not waiting on us
	}
	return v, true
}

// eligibleTransports lists the ways out that policy allows AND this
// message can actually use, in preference order. Empty means the message
// waits — which is a state, not an error.
func (r *Runtime) eligibleTransports(in DeliveryIntent) []TransportKind {
	ceiling := r.radioCarryCeiling()
	c := r.connectivity()
	var out []TransportKind
	for _, k := range transportPreference {
		if c.allows(k, in.Space) && in.blockedOn(k, ceiling) == BlockNone {
			out = append(out, k)
		}
	}
	return out
}

// routeForSpaceLocked picks the route a SPACE should be sending over right
// now, or reports that it has nothing to send.
//
// Route selection gates SENDING only. A space with nothing outstanding
// still pumps every policy-permitted link, because refusing to listen on a
// route we are simply not choosing to send over would make a node deaf on
// its own mesh whenever the internet happened to be preferred.
func (r *Runtime) routeForSpaceLocked(c Connectivity, tid id.TerminalID,
	now time.Time) (TransportKind, bool) {

	if r.ledger == nil {
		return TransportAny, false
	}
	var oldest *DeliveryIntent
	for _, in := range r.ledger.Due(now, 0) {
		if in.Space != tid {
			continue
		}
		if oldest == nil || in.UpdatedAt < oldest.UpdatedAt {
			cp := in
			oldest = &cp
		}
	}
	if oldest == nil {
		return TransportAny, false // nothing to send
	}
	avail := map[TransportKind]bool{}
	for k, n := range r.liveLinks {
		if n > 0 {
			avail[k] = true
		}
	}
	return r.selectAmongLocked(c, *oldest, now, avail)
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

// Hysteresis is deliberately ASYMMETRIC, and that asymmetry is the whole
// design. Damping exists to stop Auto oscillating between two healthy
// routes; it must never slow down escape from a broken one. So:
//
//	the current route fails      → switch immediately
//	a better route reappears     → wait until it has been stable
//
// A symmetric delay would mean a node whose radio just died sits mute for
// twenty seconds with a working internet connection in front of it.
const (
	// transportHysteresis is how long a recovered route must hold before
	// Auto will move BACK to it. Only the promotion edge waits.
	transportHysteresis = 20 * time.Second
	// unhealthyAfter is how many consecutive failures mark a route down.
	// One failure is a packet; three is a link.
	unhealthyAfter = 3
)

// transportState tracks one route's health for Auto.
type transportState struct {
	up       bool
	since    time.Time
	failures int
}

// preference orders routes by what they cost the person: a local link is
// free and instant, the relay costs bandwidth, the radio costs airtime and
// battery and is shared with everyone else on the segment.
var transportPreference = []TransportKind{TransportLAN, TransportRelay, TransportRadio}

// noteTransportResult records what actually happened on a route. Caller
// must not hold r.mu.
//
// ErrTooLarge is NOT a health signal. The link is fine; this message does
// not fit it. Counting it would let one oversized message mark a perfectly
// good radio as broken and drag every other message off it.
func (r *Runtime) noteTransportResult(k TransportKind, err error, now time.Time) {
	if errors.Is(err, routing.ErrTooLarge) {
		return
	}
	if _, blocked := err.(ErrTransportBlocked); blocked {
		return // policy, not health
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.transportFlap == nil {
		r.transportFlap = map[TransportKind]transportState{}
	}
	cur, seen := r.transportFlap[k]
	if !seen {
		cur = transportState{up: true, since: now}
	}
	if err == nil {
		cur.failures = 0
		if !cur.up {
			cur.up = true
			cur.since = now // starts the promotion clock
		}
	} else {
		cur.failures++
		if cur.up && cur.failures >= unhealthyAfter {
			cur.up = false
			cur.since = now
		}
	}
	r.transportFlap[k] = cur
}

// healthOf reports a route's state, defaulting to healthy: a route nobody
// has tried yet is worth trying.
func (r *Runtime) healthOf(k TransportKind, now time.Time) (up bool, stable bool) {
	cur, seen := r.transportFlap[k]
	if !seen {
		return true, true
	}
	return cur.up, now.Sub(cur.since) >= transportHysteresis
}

// SelectTransport picks the route for one message, in the order that makes
// the choice explainable: eligibility, then health, then stickiness to the
// route already carrying it, then preference, and only then promotion.
//
// ok=false means the message waits — which is a state, not a failure.
func (r *Runtime) SelectTransport(in DeliveryIntent, now time.Time) (TransportKind, bool) {
	c := r.connectivity()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.selectTransportLocked(c, in, now)
}

// selectTransportLocked is SelectTransport for a caller that already holds
// r.mu and has already read the policy — the pump, which must not re-enter
// the lock while choosing where to send.
func (r *Runtime) selectTransportLocked(c Connectivity, in DeliveryIntent,
	now time.Time) (TransportKind, bool) {
	return r.selectAmongLocked(c, in, now, nil)
}

// selectAmongLocked restricts the choice to routes that actually EXIST.
//
// avail=nil means "do not filter" — the projection answers a policy
// question and may name a route the person could enable. The pump must
// pass the links it really has: choosing a preferred transport with no
// link attached is not a preference, it is silence. A radio-only node
// would sit still waiting for an internet link nobody ever adopted.
func (r *Runtime) selectAmongLocked(c Connectivity, in DeliveryIntent,
	now time.Time, avail map[TransportKind]bool) (TransportKind, bool) {

	// The locked variant: this runs with r.mu already held.
	ceiling := r.radioCarryCeilingLocked()
	var eligible []TransportKind
	for _, k := range transportPreference {
		if avail != nil && !avail[k] {
			continue
		}
		if c.allows(k, in.Space) && in.blockedOn(k, ceiling) == BlockNone {
			eligible = append(eligible, k)
		}
	}
	if len(eligible) == 0 {
		return TransportAny, false
	}

	allowed := map[TransportKind]bool{}
	for _, k := range eligible {
		allowed[k] = true
	}
	var healthy []TransportKind
	for _, k := range transportPreference {
		if !allowed[k] {
			continue
		}
		if up, _ := r.healthOf(k, now); up {
			healthy = append(healthy, k)
		}
	}

	current := transportOfLink(in.Transport)
	if allowed[current] {
		if up, _ := r.healthOf(current, now); up {
			// Stick with what is working unless something strictly better
			// has been stable long enough to trust. Moving an attempt that
			// is already under way costs a re-send for no gain.
			for _, k := range healthy {
				if k == current {
					break // nothing better is available
				}
				if _, stable := r.healthOf(k, now); stable {
					return k, true
				}
			}
			return current, true
		}
		// The current route is down: leave NOW. No waiting, no stability
		// requirement — the alternative is silence while a link that works
		// sits unused.
	}
	if len(healthy) > 0 {
		return healthy[0], true
	}
	// Nothing is healthy. Try the most preferred eligible route anyway:
	// "everything failed recently" is not evidence that everything is still
	// failing, and refusing to try guarantees the outage continues.
	for _, k := range transportPreference {
		if allowed[k] {
			return k, true
		}
	}
	return TransportAny, false
}
