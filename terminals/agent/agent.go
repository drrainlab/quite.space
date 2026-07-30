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

// Template is the manifest for a PERSONAL assistant (AI-0) — a machine
// actor somebody runs on their own device, distinct from the summarizer
// stub below.
//
// It exists as its own terminal because a human participant cannot pretend
// to be one: Participant.allowedAuthorship switches on the EMITTING
// participant's manifest, and a human one permits only human authorship.
// There is no flag on a space that changes that, and there should not be —
// "who wrote this" is a property of the writer, not of the room.
//
// AutonomyAssistive (A1), not delegated: it answers when it is asked and
// has no trigger of its own, and A2 would be a claim the code cannot back.
// No PresencePublish: an assistant does not announce that it is around,
// because it is not around in any sense a person means.
func Template(label string) manifest.Manifest {
	return manifest.Manifest{
		Kind:           manifest.KindAgent,
		DeclaredLabels: []string{label},
		IOMode:         manifest.IODuplex,
		Capabilities:   []string{capability.SignalPublish, capability.SignalReceive},
		AgencyMode:     manifest.AgencyAIAgent,
		AIPresent:      true,
		Autonomy:       manifest.AutonomyAssistive,
	}
}

// Say publishes an answer, marked machine-authored and naming the model
// that produced it. Both marks are the signer's own claims; what makes them
// worth something is that the signer is a different key from the person's.
func Say(p *terminals.Participant, s *terminals.Space, text, model string,
	at uint64) (eventlog.Applied, error) {

	payload, err := (&schemas.TextMessage{Text: text, ProducedModel: model}).Encode()
	if err != nil {
		return eventlog.Applied{}, err
	}
	return p.Emit(s, schemas.MessageText, payload, signal.AuthorshipAIAgent, at)
}

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
