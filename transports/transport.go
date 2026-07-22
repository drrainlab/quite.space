// Package transports defines the kernel↔transport boundary for the M0
// harness (ADR-007): endpoints carry opaque packets and declare their
// capabilities; they never parse, decrypt, or re-serialize what they carry.
//
// The M0 endpoint is synchronous and deterministic (Send/Poll) so network
// misbehavior can be simulated reproducibly in tests; the async adapter
// interface from plan §18 wraps this in M1 (LAN transport).
package transports

// AckLevel says which receipts a transport can legitimately produce. A
// transport can never prove destination-level delivery (ADR-007).
type AckLevel uint8

const (
	AckNone AckLevel = iota
	AckTransport
)

// Capabilities declared by an endpoint (plan §4.6). The kernel routes and
// projects based on these fields only — never on the transport's name.
type Capabilities struct {
	MaxPayload int // maximum packet size in bytes; 0 = unlimited
	Realtime   bool
	Ack        AckLevel
}

// Endpoint is one side of a packet channel.
type Endpoint interface {
	// Send queues one opaque packet toward the peer. Oversized packets are
	// an error: the caller must fragment to MaxPayload.
	Send(pkt []byte) error
	// Poll drains packets that have arrived since the last call.
	Poll() [][]byte
	// Capabilities reports declared limits.
	Capabilities() Capabilities
}
