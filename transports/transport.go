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

	// MaxEventBytes is the largest SINGLE event worth putting on this
	// carrier. Zero means no ceiling, which is every carrier that has one
	// today and stays their behaviour exactly.
	//
	// It is NOT MaxPayload, and conflating the two is the mistake this field
	// exists to prevent. MaxPayload asks what the carrier CAN move — the
	// radio transfer layer fragments, so the honest answer there is
	// kilobytes. This asks what it SHOULD, and the two diverge by orders of
	// magnitude: an inline preview of 40 KiB is entirely movable over LoRa
	// and takes SIX AND A HALF MINUTES of air, measured, during which nothing
	// else on that radio moves at all.
	//
	// So this is a POLICY, declared by the carrier because the carrier is the
	// only thing that knows what its own seconds cost. Sync reads it and
	// declines rather than discovering the cost afterwards.
	MaxEventBytes int

	// ServesBlobs reports whether asset chunks may be answered here.
	//
	// A separate flag rather than a size, because the question is different
	// in kind: a blob request is unbounded work somebody ELSE asked for, and
	// on a shared half-duplex segment answering it is a decision about
	// everyone's air, not just ours. False is the honest answer for a radio;
	// the assets still arrive, over any wider path, whenever one exists.
	//
	// Zero value is TRUE by construction — the field is negated on purpose
	// (see BlobsRefused) so that every existing carrier keeps serving blobs
	// without being edited, and only a carrier that opts OUT changes.
	BlobsRefused bool
}

// CarriesEvent reports whether one event of this size belongs on this carrier.
func (c Capabilities) CarriesEvent(n int) bool {
	return c.MaxEventBytes <= 0 || n <= c.MaxEventBytes
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
