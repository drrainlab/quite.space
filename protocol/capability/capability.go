// Package capability defines the atomic capability vocabulary (engineering
// plan §6.2). Capabilities are the only source of "what is allowed"
// (invariant §2.2); kind and labels grant nothing.
package capability

import "sort"

// Atomic capabilities, v0 closed vocabulary.
const (
	SignalPublish    = "signal.publish"
	SignalReceive    = "signal.receive"
	QueryReceive     = "query.receive"
	QueryRespond     = "query.respond"
	CommandReceive   = "command.receive"
	CommandExecute   = "command.execute"
	ObjectStore      = "object.store"
	ObjectServe      = "object.serve"
	PacketRelay      = "packet.relay"
	TerminalDiscover = "terminal.discover"
	PresencePublish  = "presence.publish"
)

// Known reports whether s is part of the v0 vocabulary. Unknown capability
// names are never granted implicitly (fail closed, invariant §2.7).
func Known(s string) bool {
	switch s {
	case SignalPublish, SignalReceive, QueryReceive, QueryRespond,
		CommandReceive, CommandExecute, ObjectStore, ObjectServe,
		PacketRelay, TerminalDiscover, PresencePublish:
		return true
	}
	return false
}

// Set is an immutable-by-convention capability set.
type Set map[string]struct{}

// NewSet builds a set from names, keeping unknown names out.
func NewSet(names ...string) Set {
	s := make(Set, len(names))
	for _, n := range names {
		if Known(n) {
			s[n] = struct{}{}
		}
	}
	return s
}

// Has reports whether the capability is present.
func (s Set) Has(name string) bool {
	_, ok := s[name]
	return ok
}

// List returns the sorted capability names.
func (s Set) List() []string {
	out := make([]string, 0, len(s))
	for n := range s {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Diff returns capabilities present in s but not in old — used for manifest
// revision review (M0.3 capability diff).
func (s Set) Diff(old Set) (added, removed []string) {
	for n := range s {
		if !old.Has(n) {
			added = append(added, n)
		}
	}
	for n := range old {
		if !s.Has(n) {
			removed = append(removed, n)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
