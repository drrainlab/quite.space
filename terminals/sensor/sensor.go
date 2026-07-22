// Package sensor builds a source-only temperature sensor (plan §5.5, Demo
// B): publishes observations with provenance, accepts nothing, keeps one
// hour of data, declares simulated data as simulated.
package sensor

import (
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// NewTemperature creates a source-only climate sensor terminal.
func NewTemperature(label string) (*terminals.Participant, error) {
	return terminals.NewParticipant(manifest.Manifest{
		Kind:           manifest.KindSensor,
		DeclaredLabels: []string{label, "temperature"},
		IOMode:         manifest.IOSourceOnly,
		Capabilities:   []string{capability.SignalPublish},
		Publishes:      []string{schemas.ObservationTemp},
		AgencyMode:     manifest.AgencyDeterministic,
		RetentionSecs:  3600,
		AnnounceTTL:    300,
	})
}

// Publish emits one observation. simulated must be declared truthfully —
// the payload schema requires stale_after so every reading ages honestly.
func Publish(p *terminals.Participant, s *terminals.Space,
	centiCelsius uint64, negative, simulated bool, observedAt uint64) (eventlog.Applied, error) {

	payload, err := (&schemas.Observation{
		CentiValue: centiCelsius,
		Negative:   negative,
		Unit:       "celsius",
		ObservedAt: observedAt,
		StaleAfter: 600,
		Simulated:  simulated,
	}).Encode()
	if err != nil {
		return eventlog.Applied{}, err
	}
	return p.Emit(s, schemas.ObservationTemp, payload, signal.AuthorshipSensor, observedAt)
}
