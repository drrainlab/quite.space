// Listening-room projection (LR-2). The session state of an instance is the
// MAXIMUM command in (session_epoch, sequence, event_clock, event_id) order,
// authored by the PERMANENT host (the instance creator). Commands from
// anyone else are ignored and counted — the API refuses them too, but the
// reducer never trusts the API. Wall time never participates in ordering;
// it only parameterizes position computation at render time.
package reducers

import (
	"encoding/json"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/listening"
)

// ListeningSession is the folded session of one listening-room instance.
type ListeningSession struct {
	Host       id.PrincipalID
	HasCommand bool
	Command    listening.Command
	Clock      uint64
	EventID    id.EventID
	// IgnoredNonHost counts commands dropped because the signer was not the
	// host (surfaced in Protocol view, never silently).
	IgnoredNonHost  int
	IgnoredMalformed int
}

// ListeningSession folds the command partition of an instance. ok is false
// when the instance is unknown.
func (s *State) ListeningSession(iid [16]byte) (*ListeningSession, bool) {
	rec, ok := s.appInstances[iid]
	if !ok {
		return nil, false
	}
	sess := &ListeningSession{Host: rec.Author}
	for _, ev := range s.appEvents[iid] {
		if ev.Schema != listening.SchemaCommand {
			continue
		}
		if ev.Author != rec.Author {
			sess.IgnoredNonHost++
			continue
		}
		var c listening.Command
		if err := decodeCommandData(ev.Data, &c); err != nil {
			sess.IgnoredMalformed++
			continue
		}
		if !sess.HasCommand || listening.Later(
			c.SessionEpoch, c.Sequence, ev.Clock, ev.EventID,
			sess.Command.SessionEpoch, sess.Command.Sequence, sess.Clock, sess.EventID) {
			sess.HasCommand = true
			sess.Command = c
			sess.Clock = ev.Clock
			sess.EventID = ev.EventID
		}
	}
	return sess, true
}

func decodeCommandData(data []byte, c *listening.Command) error {
	if err := json.Unmarshal(data, c); err != nil {
		return err
	}
	return c.Validate()
}
