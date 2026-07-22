// Package human builds a Human Terminal (plan §5.1): a person acts through
// it. kind: human proves nothing about individual messages — authorship is
// marked per event and enforced by the shared runtime.
package human

import (
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// Template is the human terminal manifest template.
func Template(label string) manifest.Manifest {
	return manifest.Manifest{
		Kind:           manifest.KindHuman,
		DeclaredLabels: []string{label},
		IOMode:         manifest.IODuplex,
		Capabilities: []string{capability.SignalPublish, capability.SignalReceive,
			capability.PresencePublish},
		AgencyMode: manifest.AgencyHuman,
	}
}

// New creates a duplex human terminal.
func New(label string) (*terminals.Participant, error) {
	return terminals.NewParticipant(Template(label))
}

// Say publishes a human text message.
func Say(p *terminals.Participant, s *terminals.Space, text string, at uint64) (eventlog.Applied, error) {
	payload, err := (&schemas.TextMessage{Text: text}).Encode()
	if err != nil {
		return eventlog.Applied{}, err
	}
	return p.Emit(s, schemas.MessageText, payload, signal.AuthorshipHuman, at)
}

// SetPresence publishes a presence state with mandatory TTL (plan §8.3).
func SetPresence(p *terminals.Participant, s *terminals.Space, state string, at, ttl uint64) error {
	payload, err := (&schemas.PresencePayload{State: state, ExpiresAt: at + ttl}).Encode()
	if err != nil {
		return err
	}
	_, err = p.Emit(s, schemas.PresenceUpdate, payload, signal.AuthorshipHuman, at)
	return err
}
