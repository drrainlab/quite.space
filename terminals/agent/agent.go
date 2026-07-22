// Package agent builds an honest AI-agent stub (plan §5.4, §10, Demo C):
// it reads one space, produces summaries, and every event it emits is
// marked ai_agent with human_approved=false. The runtime makes signing as
// human structurally impossible for it. The model identity is undeclared,
// so it projects as "AI-agent, model not specified" — never guessed.
package agent

import (
	"fmt"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// NewSummarizer creates the agent terminal: delegated autonomy (A2), scoped
// to reading and publishing in its space; no membership or permission
// capabilities exist in its manifest at all.
func NewSummarizer() (*terminals.Participant, error) {
	return terminals.NewParticipant(manifest.Manifest{
		Kind:           manifest.KindAgent,
		DeclaredLabels: []string{"summarizer"},
		IOMode:         manifest.IODuplex,
		Capabilities:   []string{capability.SignalPublish, capability.SignalReceive},
		AgencyMode:     manifest.AgencyAIAgent,
		AIPresent:      true,
		Autonomy:       manifest.AutonomyDelegated,
	})
}

// Summarize emits a deterministic stub summary of the space's messages,
// marked as AI output. A real model plugs in here later (optional Block,
// vision §7) — the honesty contract stays identical.
func Summarize(p *terminals.Participant, s *terminals.Space, at uint64) (eventlog.Applied, error) {
	if err := p.RequireReceive(); err != nil {
		return eventlog.Applied{}, err
	}
	msgs := s.State.Messages()
	humans := 0
	var maxClock uint64
	for _, m := range msgs {
		if m.ProducedBy == signal.AuthorshipHuman {
			humans++
		}
		if m.Clock > maxClock {
			maxClock = m.Clock
		}
	}
	p.ObserveClock(maxClock)
	text := fmt.Sprintf("[AI summary] %d messages in space, %d from humans.", len(msgs), humans)
	payload, err := (&schemas.TextMessage{Text: text}).Encode()
	if err != nil {
		return eventlog.Applied{}, err
	}
	return p.Emit(s, schemas.MessageText, payload, signal.AuthorshipAIAgent, at)
}
