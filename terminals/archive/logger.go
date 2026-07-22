// Package archive builds a sink-only logger terminal (plan §5.9 / M0.8
// "sink-only logger"): it receives and counts, and can never publish — the
// runtime rejects any Emit because signal.publish is not in its manifest.
package archive

import (
	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/capability"
	"github.com/drrainlab/quiet_places/protocol/manifest"
	"github.com/drrainlab/quiet_places/terminals"
)

// Logger counts what it stores, per schema.
type Logger struct {
	P      *terminals.Participant
	Counts map[string]int
}

// NewLogger creates the sink-only terminal.
func NewLogger() (*Logger, error) {
	p, err := terminals.NewParticipant(manifest.Manifest{
		Kind:           manifest.KindArchive,
		DeclaredLabels: []string{"logger"},
		IOMode:         manifest.IOSinkOnly,
		Capabilities:   []string{capability.SignalReceive, capability.ObjectStore},
		AgencyMode:     manifest.AgencyDeterministic,
	})
	if err != nil {
		return nil, err
	}
	return &Logger{P: p, Counts: map[string]int{}}, nil
}

// Record tallies one applied event.
func (l *Logger) Record(a eventlog.Applied) {
	l.Counts[a.Env.Schema]++
	l.P.ObserveClock(a.Env.LogicalClock)
}
