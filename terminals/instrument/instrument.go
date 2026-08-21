// Package instrument builds the manifest for an instrument participant —
// the physical (or reference-simulated) endpoint of the Instrument Plane
// (QI-1, ADR-025).
//
// An instrument is a Participant like the agent and the gateway before
// it: its own device keys, its own terminal identity, the operator's
// principal as controller — a certified endpoint that is a subject of
// the space without being a person. What its channels MEAN lives here,
// in the signed manifest (qp.instr labels); its readings carry only
// channel and value.
package instrument

import (
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/terminals"
)

// Template builds an instrument manifest. kind is "sensor" or
// "actuator"; in v1 both are read-only surfaces — an actuator's channels
// render as state until the command plane (QI-4) exists, so neither
// declares Commands or CommandReceive yet.
func Template(kind, label string, channels []terminals.ChannelDecl) (manifest.Manifest, error) {
	var mk manifest.Kind
	switch kind {
	case "sensor":
		mk = manifest.KindSensor
	case "actuator":
		mk = manifest.KindActuator
	default:
		return manifest.Manifest{}, fmt.Errorf("instrument: kind %q is not sensor or actuator", kind)
	}
	labels, err := terminals.InstrumentLabels(channels)
	if err != nil {
		return manifest.Manifest{}, err
	}
	return manifest.Manifest{
		Kind:           mk,
		DeclaredLabels: append([]string{label}, labels...),
		IOMode:         manifest.IOSourceOnly,
		Capabilities:   []string{capability.SignalPublish},
		Publishes:      []string{schemas.ObservationValue},
		AgencyMode:     manifest.AgencyDeterministic,
		RetentionSecs:  3600,
		AnnounceTTL:    300,
	}, nil
}
