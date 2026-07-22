// Package bot builds a deterministic echo bot (plan §5.3): programmable
// automation, no AI. Every event it emits is marked deterministic_bot — the
// runtime rejects anything else.
package bot

import (
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// NewEcho creates the echo bot terminal.
func NewEcho() (*terminals.Participant, error) {
	return terminals.NewParticipant(manifest.Manifest{
		Kind:           manifest.KindBot,
		DeclaredLabels: []string{"echo"},
		IOMode:         manifest.IODuplex,
		Capabilities:   []string{capability.SignalPublish, capability.SignalReceive},
		AgencyMode:     manifest.AgencyDeterministic,
	})
}

// React echoes human text messages it has not answered yet. Deterministic:
// same input state → same replies.
func React(p *terminals.Participant, s *terminals.Space, answered map[id.EventID]bool, at uint64) (int, error) {
	if err := p.RequireReceive(); err != nil {
		return 0, err
	}
	replies := 0
	for _, m := range s.State.Messages() {
		if m.ProducedBy != signal.AuthorshipHuman || answered[m.ID] {
			continue
		}
		reply := m.ID
		payload, err := (&schemas.TextMessage{Text: "echo: " + m.Text, ReplyTo: &reply}).Encode()
		if err != nil {
			return replies, err
		}
		p.ObserveClock(m.Clock)
		if _, err := p.Emit(s, schemas.MessageText, payload, signal.AuthorshipDeterministicBot, at); err != nil {
			return replies, err
		}
		answered[m.ID] = true
		replies++
	}
	return replies, nil
}

// Applied lets callers advance the bot's clock from synced events.
func Applied(p *terminals.Participant, a eventlog.Applied) { p.ObserveClock(a.Env.LogicalClock) }
