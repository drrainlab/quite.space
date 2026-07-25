// RoutedOpaqueEnvelope (RB-0B): the two-layer view a forwarding element is
// allowed to have of a frame — a route it may read, and bytes it may only
// carry.
//
// ADR-015 §6 described this as a TYPE that makes blindness structural. It
// was never written: the bridge passed raw []byte around and stayed blind
// by discipline plus an import-boundary test. Discipline held, but nothing
// in the code said where the line was, so the next person to add a feature
// had to already know.
//
// The type does not enforce blindness on its own — a determined caller can
// always decode Ciphertext itself, and the import-boundary test remains the
// thing that actually prevents it. What the type does is make the boundary
// legible and give the tests a name to assert against.
package routing

import (
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// OpaqueRoute is everything a forwarding element may read: cleartext
// envelope header fields plus where the frame came in. No payload, no
// author identity beyond the device that signed it, no schema semantics —
// only what is needed to decide where a frame goes and when it stops
// mattering.
type OpaqueRoute struct {
	EventID         id.EventID
	DestinationHint id.TerminalID // the space/terminal on the wire
	ReturnHint      id.DeviceID   // author device — where an ACK goes back
	Sequence        uint64
	Priority        signal.Priority
	Scope           signal.ForwardingScope
	ExpiresAt       uint64
	Size            int
	LinkID          LinkID
	LoopDomain      LoopDomainID
}

// RoutedOpaqueEnvelope pairs a readable route with unreadable bytes.
type RoutedOpaqueEnvelope struct {
	Route      OpaqueRoute
	Ciphertext []byte
}

// RouteOf builds the envelope from a frame's cleartext header. The frame is
// retained VERBATIM: custody must hand back exactly what it was given, so
// this deliberately does not copy or normalize the bytes.
func RouteOf(frame []byte, link LinkID, domain LoopDomainID) (RoutedOpaqueEnvelope, error) {
	m, err := MetaOf(frame, link)
	if err != nil {
		return RoutedOpaqueEnvelope{}, err
	}
	return RoutedOpaqueEnvelope{
		Route: OpaqueRoute{
			EventID:         m.EventID,
			DestinationHint: m.Destination,
			ReturnHint:      m.Author,
			Sequence:        m.Sequence,
			Priority:        m.Priority,
			Scope:           m.Scope,
			ExpiresAt:       m.ExpiresAt,
			Size:            m.Size,
			LinkID:          link,
			LoopDomain:      domain,
		},
		Ciphertext: frame,
	}, nil
}

// Meta renders the route back as a FrameMeta for the queue and filters.
func (e RoutedOpaqueEnvelope) Meta() FrameMeta {
	return FrameMeta{
		EventID:     e.Route.EventID,
		Destination: e.Route.DestinationHint,
		Priority:    e.Route.Priority,
		ExpiresAt:   e.Route.ExpiresAt,
		Scope:       e.Route.Scope,
		Size:        e.Route.Size,
		IngressLink: e.Route.LinkID,
		Author:      e.Route.ReturnHint,
		Sequence:    e.Route.Sequence,
	}
}
