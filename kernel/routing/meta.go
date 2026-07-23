// FrameMeta (TN-1): everything a routing/filter decision may need, read
// from the CLEARTEXT envelope header — never the payload. This is the one
// filter signature (rev 2.1): a bare TerminalID predicate would breed a
// cascade of extra callbacks.
package routing

import (
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// FrameMeta is the routing view of one signed frame.
type FrameMeta struct {
	EventID        id.EventID
	SourceTerminal id.TerminalID // author terminal (provenance)
	Destination    id.TerminalID // the space/terminal the frame belongs to
	Schema         string
	Priority       signal.Priority
	ExpiresAt      uint64
	Scope          signal.ForwardingScope
	Size           int
	IngressLink    LinkID
}

// MetaOf decodes the header of a frame into FrameMeta. The payload is never
// examined; decode errors surface (a forwarding element drops undecodable
// frames — they could not be verified anywhere anyway).
func MetaOf(frame []byte, ingress LinkID) (FrameMeta, error) {
	env, err := signal.Decode(frame)
	if err != nil {
		return FrameMeta{}, err
	}
	m := FrameMeta{
		EventID:     id.EventIDOf(frame),
		Destination: env.Terminal,
		Schema:      env.Schema,
		Priority:    env.Priority,
		ExpiresAt:   env.ExpiresAt,
		Scope:       env.ForwardingScope(),
		Size:        len(frame),
		IngressLink: ingress,
	}
	if env.SourceTerminal != nil {
		m.SourceTerminal = *env.SourceTerminal
	}
	if m.Priority == 0 {
		m.Priority = signal.PriorityMessage
	}
	return m, nil
}
