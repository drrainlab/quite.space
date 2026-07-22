// Apps projection (ADR-014, APP-0): definitions are immutable per revision
// event (pinning resolves by exact event id); instances are first-sight
// immutable with their own state partition — every state event folds under
// its instance_id, so two instances of one definition can never mix state.
package reducers

import (
	"sort"

	"github.com/drrainlab/quiet_places/protocol/appdef"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// AppDefinitionRec is one definition revision (immutable).
type AppDefinitionRec struct {
	Definition *appdef.Definition
	EventID    id.EventID
	Author     id.PrincipalID
	Clock      uint64
}

// AppInstanceRec is one placed instance (immutable, first sight wins).
type AppInstanceRec struct {
	Instance *appdef.Instance
	EventID  id.EventID
	Author   id.PrincipalID
	Clock    uint64
}

// AppStateEvent is one folded state event of an instance partition.
type AppStateEvent struct {
	Schema  string
	Data    []byte
	Author  id.PrincipalID
	Clock   uint64
	EventID id.EventID
}

func (s *State) applyAppDefinition(env *signal.Envelope, eid id.EventID) {
	def, err := appdef.DecodeDefinition(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	if s.appDefs == nil {
		s.appDefs = map[id.EventID]*AppDefinitionRec{}
	}
	s.appDefs[eid] = &AppDefinitionRec{
		Definition: def, EventID: eid, Author: env.Principal, Clock: env.LogicalClock,
	}
}

func (s *State) applyAppInstance(env *signal.Envelope, eid id.EventID) {
	inst, err := appdef.DecodeInstance(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	var iid [16]byte
	copy(iid[:], mustHex16(inst.InstanceID))
	if s.appInstances == nil {
		s.appInstances = map[[16]byte]*AppInstanceRec{}
	}
	// Instances are immutable: the first sight wins, replays are ignored.
	if _, seen := s.appInstances[iid]; seen {
		return
	}
	s.appInstances[iid] = &AppInstanceRec{
		Instance: inst, EventID: eid, Author: env.Principal, Clock: env.LogicalClock,
	}
	if s.appInstanceEvents == nil {
		s.appInstanceEvents = map[id.EventID][16]byte{}
	}
	s.appInstanceEvents[eid] = iid
	s.resolveKeepTarget(eid)
}

func (s *State) applyAppState(env *signal.Envelope, eid id.EventID) {
	ev, err := appdef.DecodeStateEvent(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	if s.appEvents == nil {
		s.appEvents = map[[16]byte][]AppStateEvent{}
	}
	s.appEvents[ev.InstanceID] = append(s.appEvents[ev.InstanceID], AppStateEvent{
		Schema: env.Schema, Data: ev.Data, Author: env.Principal,
		Clock: env.LogicalClock, EventID: eid,
	})
}

// AppDefinitionByEvent resolves a PINNED definition revision.
func (s *State) AppDefinitionByEvent(eid id.EventID) (*AppDefinitionRec, bool) {
	rec, ok := s.appDefs[eid]
	return rec, ok
}

// AppInstances lists placed instances (deterministic order).
func (s *State) AppInstances() []AppInstanceRec {
	out := make([]AppInstanceRec, 0, len(s.appInstances))
	for _, rec := range s.appInstances {
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Clock != out[j].Clock {
			return out[i].Clock < out[j].Clock
		}
		return string(out[i].EventID[:]) < string(out[j].EventID[:])
	})
	return out
}

// AppInstanceByID returns one instance.
func (s *State) AppInstanceByID(iid [16]byte) (*AppInstanceRec, bool) {
	rec, ok := s.appInstances[iid]
	return rec, ok
}

// AppInstanceByEvent resolves an instance by its creation event id (keep
// targets address app instances this way).
func (s *State) AppInstanceByEvent(eid id.EventID) (*AppInstanceRec, bool) {
	iid, ok := s.appInstanceEvents[eid]
	if !ok {
		return nil, false
	}
	rec, ok := s.appInstances[iid]
	return rec, ok
}

// AppEvents returns an instance partition's state events for ONE schema, in
// deterministic (clock, event id) order. The caller (node) enforces
// capability scoping — this is the partition, not an oracle.
func (s *State) AppEvents(iid [16]byte, schema string, limit int) []AppStateEvent {
	var out []AppStateEvent
	for _, ev := range s.appEvents[iid] {
		if ev.Schema == schema {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Clock != out[j].Clock {
			return out[i].Clock < out[j].Clock
		}
		return string(out[i].EventID[:]) < string(out[j].EventID[:])
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func mustHex16(s string) []byte {
	out := make([]byte, 16)
	for i := 0; i+1 < len(s) && i/2 < 16; i += 2 {
		hi := hexVal(s[i])
		lo := hexVal(s[i+1])
		out[i/2] = hi<<4 | lo
	}
	return out
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
