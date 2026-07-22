// Package trust is the Trust & Claims Engine (M0.5, ADR-008): the only
// component that evaluates claims, delivery status, and presence. Its APIs
// speak exclusively in (value, origin, proof, age) tuples — there is no way
// to obtain a bare "online" or "delivered" boolean from this package.
package trust

import (
	"errors"

	"github.com/drrainlab/quiet_places/protocol/claims"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/receipts"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// ProofKind says what kind of evidence backs a delivery level.
type ProofKind uint8

const (
	ProofNone ProofKind = iota
	ProofLocal
	ProofTransportReceipt
	ProofSignedReceipt // a receipt Signal from the destination
)

type deliveryRecord struct {
	level claims.DeliveryLevel
	kind  ProofKind
	proof id.EventID // the receipt event, when kind == ProofSignedReceipt
}

type deliveryKey struct {
	event id.EventID
	dest  id.TerminalID
}

// Engine tracks proven delivery levels and projects claims and presence.
// Pure state, rebuildable from the log.
type Engine struct {
	delivery map[deliveryKey]deliveryRecord
	presence map[id.TerminalID]claims.Presence
}

func NewEngine() *Engine {
	return &Engine{
		delivery: map[deliveryKey]deliveryRecord{},
		presence: map[id.TerminalID]claims.Presence{},
	}
}

// ErrTransportOverclaim is returned when a transport tries to assert a level
// only the destination can prove (ADR-007).
var ErrTransportOverclaim = errors.New("trust: transports cannot prove destination-level delivery")

// RecordLocal marks created_local / queued / handed_to_transport progress.
func (e *Engine) RecordLocal(event id.EventID, dest id.TerminalID, level claims.DeliveryLevel) error {
	if level > claims.DeliveryHandedToTransport {
		return ErrTransportOverclaim
	}
	e.upgrade(deliveryKey{event, dest}, deliveryRecord{level: level, kind: ProofLocal})
	return nil
}

// RecordTransportReceipt records what a transport proved. Anything above
// accepted_by_relay is rejected outright — a transport cannot sign for the
// destination, so it cannot prove more (ADR-007, honesty test "relay ACK is
// not delivered").
func (e *Engine) RecordTransportReceipt(event id.EventID, dest id.TerminalID, level claims.DeliveryLevel) error {
	if level.RequiresDestinationProof() {
		return ErrTransportOverclaim
	}
	e.upgrade(deliveryKey{event, dest}, deliveryRecord{level: level, kind: ProofTransportReceipt})
	return nil
}

// IngestReceiptSignal records a signed delivery receipt that arrived through
// the event log. The receipt author terminal is the destination asserting
// its own level; the proof is the receipt event itself.
func (e *Engine) IngestReceiptSignal(env *signal.Envelope, receiptEventID id.EventID) error {
	if env.Schema != receipts.SchemaDelivery {
		return errors.New("trust: not a delivery receipt")
	}
	r, err := receipts.Decode(env.Payload)
	if err != nil {
		return err
	}
	e.upgrade(deliveryKey{r.Subject, env.Terminal},
		deliveryRecord{level: r.Level, kind: ProofSignedReceipt, proof: receiptEventID})
	return nil
}

// upgrade keeps the highest proven level; a lower or equal claim never
// downgrades and never re-labels existing proof.
func (e *Engine) upgrade(k deliveryKey, rec deliveryRecord) {
	if cur, ok := e.delivery[k]; ok && cur.level >= rec.level {
		return
	}
	e.delivery[k] = rec
}

// DeliveryStatus is the full projection: level plus what proves it. There
// is deliberately no method returning the level alone.
type DeliveryStatus struct {
	Level claims.DeliveryLevel
	Proof ProofKind
	Event id.EventID // proof event when Proof == ProofSignedReceipt
}

// Delivery projects the proven status for (event, destination). Unproven
// pairs return level unknown (fail closed).
func (e *Engine) Delivery(event id.EventID, dest id.TerminalID) DeliveryStatus {
	rec, ok := e.delivery[deliveryKey{event, dest}]
	if !ok {
		return DeliveryStatus{Level: claims.DeliveryUnknown, Proof: ProofNone}
	}
	return DeliveryStatus{Level: rec.level, Proof: rec.kind, Event: rec.proof}
}

// ---- Presence (plan §8.3) ----

// UpdatePresence stores the latest announce for a terminal.
func (e *Engine) UpdatePresence(p claims.Presence) {
	cur, ok := e.presence[p.Source]
	if ok && cur.EmittedAt > p.EmittedAt {
		return
	}
	e.presence[p.Source] = p
}

// ProjectedPresence can express current state OR last-known-with-age, never
// a current-tense state for an expired announce.
type ProjectedPresence struct {
	Known      bool
	Current    bool
	State      string
	AgeSeconds uint64
}

// Presence projects a terminal's presence at the given time.
func (e *Engine) Presence(t id.TerminalID, nowUnix uint64) ProjectedPresence {
	p, ok := e.presence[t]
	if !ok {
		return ProjectedPresence{}
	}
	if p.Current(nowUnix) {
		return ProjectedPresence{Known: true, Current: true, State: p.State}
	}
	return ProjectedPresence{Known: true, Current: false, State: p.State,
		AgeSeconds: p.AgeSeconds(nowUnix)}
}

// ---- Claim projection ----

// ProjectedClaim is what UIs may show: value with origin and age, nothing
// stronger.
type ProjectedClaim struct {
	Value      string
	Origin     claims.Origin
	AgeSeconds uint64
	Expired    bool
}

// ProjectClaim renders a claim honestly at the given time.
func ProjectClaim(c claims.Claim, nowUnix uint64) ProjectedClaim {
	age := uint64(0)
	if nowUnix > c.IssuedAt {
		age = nowUnix - c.IssuedAt
	}
	return ProjectedClaim{
		Value:      c.Value,
		Origin:     c.Origin,
		AgeSeconds: age,
		Expired:    c.Expired(nowUnix),
	}
}

// IsVerified reports whether a claim may be presented as verified: only
// third_party_verified, unexpired claims qualify. Self-declared claims can
// never pass, no matter their content (honesty test).
func IsVerified(c claims.Claim, nowUnix uint64) bool {
	return c.Origin == claims.OriginThirdPartyVerified && !c.Expired(nowUnix)
}

// ---- Observation freshness (plan §9) ----

// Freshness of a sensor value.
type Freshness uint8

const (
	FreshnessUnknown Freshness = iota
	FreshnessCurrent
	FreshnessStale
)

// ObservationFreshness applies the value's own stale_after declaration.
func ObservationFreshness(observedAt, staleAfter, nowUnix uint64) Freshness {
	if observedAt == 0 || staleAfter == 0 {
		return FreshnessUnknown
	}
	if nowUnix > observedAt+staleAfter {
		return FreshnessStale
	}
	return FreshnessCurrent
}
