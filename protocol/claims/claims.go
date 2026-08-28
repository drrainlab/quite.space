// Package claims defines the Truth Contract vocabulary (engineering plan §8,
// ADR-008): claim origins, the delivery-status ladder, and presence with TTL.
// Evaluation lives in kernel/trust; this package is pure vocabulary so that
// no other package can invent a stronger status than the protocol defines.
package claims

import "github.com/drrainlab/quiet_places/protocol/id"

// Origin says where a claim's value comes from. Closed v0 enum (ADR-008).
type Origin uint8

const (
	OriginUnknown Origin = iota
	OriginProtocolDerived
	OriginSelfDeclared
	OriginPeerObserved
	OriginThirdPartyVerified
	OriginLocallyAssigned
)

func (o Origin) String() string {
	names := []string{"unknown", "protocol_derived", "self_declared",
		"peer_observed", "third_party_verified", "locally_assigned"}
	if int(o) < len(names) {
		return names[o]
	}
	return "unknown"
}

// Claim is a statement with explicit provenance. Claims are not facts
// (invariant §2.3).
type Claim struct {
	Key       string // what is being claimed, e.g. "calibrated"
	Value     string
	Origin    Origin
	Issuer    []byte // public key of whoever made the statement
	IssuedAt  uint64 // unix seconds
	ExpiresAt uint64 // 0 = no expiry
}

// Expired reports whether the claim is past its TTL at the given time.
func (c Claim) Expired(nowUnix uint64) bool {
	return c.ExpiresAt != 0 && nowUnix >= c.ExpiresAt
}

// DeliveryLevel is the ordered delivery-status ladder (plan §8.2). A level
// may only be displayed when a proof event for it exists (ADR-008); the
// kernel enforces that no API yields a level without its proof.
type DeliveryLevel uint8

const (
	DeliveryUnknown DeliveryLevel = iota
	DeliveryCreatedLocal
	DeliveryQueued
	DeliveryHandedToTransport
	DeliveryAcceptedByRelay
	DeliveryReceivedByTerminal
	DeliveryDecryptedByTerminal
	DeliveryProcessedBySoftware
	DeliveryPresentedToHuman
	DeliveryAcknowledgedByHuman
)

func (l DeliveryLevel) String() string {
	names := []string{"unknown", "created_local", "queued",
		"handed_to_transport", "accepted_by_relay", "received_by_terminal",
		"decrypted_by_terminal", "processed_by_software",
		"presented_to_human", "acknowledged_by_human"}
	if int(l) < len(names) {
		return names[l]
	}
	return "unknown"
}

// RequiresDestinationProof reports whether the level can only be proven by a
// signed receipt from the destination itself — transports can never assert
// these (ADR-007).
func (l DeliveryLevel) RequiresDestinationProof() bool {
	return l >= DeliveryReceivedByTerminal
}

// Presence is a self-declared state with mandatory TTL (plan §8.3).
type Presence struct {
	State     string // e.g. "listening", "open_to_talk", "busy"
	EmittedAt uint64
	ExpiresAt uint64
	Source    id.TerminalID
}

// Current reports whether the announce is still within its TTL. Expired
// presence must be projected as "last known + age", never as current state.
func (p Presence) Current(nowUnix uint64) bool {
	return nowUnix < p.ExpiresAt
}

// Position is a member's geographic claim (SP-3, ADR-031): the last
// signed statement about where this terminal was, with the AUTHOR'S
// stated accuracy — never a measured truth. EventID rides along as the
// LWW tiebreak: presence's ">" guard leaves equal-EmittedAt claims
// arrival-order dependent, and the position twin closes that hole.
type Position struct {
	LatE7U     uint64
	LonE7U     uint64
	AccuracyM  uint64 // 0 = undeclared
	EmittedAt  uint64
	ExpiresAt  uint64
	ObservedAt uint64 // 0 = EmittedAt
	Source     id.TerminalID
	EventID    id.EventID
}

// Current reports whether the claim is inside its signed TTL. Expired
// positions project as "last known + age of knowledge", never as now.
func (p Position) Current(nowUnix uint64) bool {
	return nowUnix < p.ExpiresAt
}

// AgeSeconds is how old the knowledge is.
func (p Position) AgeSeconds(nowUnix uint64) uint64 {
	at := p.ObservedAt
	if at == 0 {
		at = p.EmittedAt
	}
	if nowUnix <= at {
		return 0
	}
	return nowUnix - at
}

// AgeSeconds returns how old the announce is at the given time.
func (p Presence) AgeSeconds(nowUnix uint64) uint64 {
	if nowUnix <= p.EmittedAt {
		return 0
	}
	return nowUnix - p.EmittedAt
}
