// Package simulator is the T4 low-bandwidth transport (plan §19): loopback
// links constrained to radio-like profiles. Passing sync over these profiles
// is a prerequisite for writing the Reticulum and Meshtastic adapters —
// the same Terminal, projected onto 64-byte frames with 10–30% loss.
package simulator

import "github.com/drrainlab/quiet_places/transports/loopback"

// Profile names a bandwidth/reliability class.
type Profile struct {
	Name          string
	MTU           int
	DropRate      float64
	DuplicateRate float64
	Reorder       bool
}

// The canonical profiles from plan §19 T4.
var (
	Lora64 = Profile{Name: "lora-64", MTU: 64, DropRate: 0.30,
		DuplicateRate: 0.05, Reorder: true}
	Lora128 = Profile{Name: "lora-128", MTU: 128, DropRate: 0.20,
		DuplicateRate: 0.05, Reorder: true}
	Mesh240 = Profile{Name: "mesh-240", MTU: 240, DropRate: 0.10,
		DuplicateRate: 0.02, Reorder: true}
)

// NewPair builds a loopback link constrained to the profile.
func NewPair(p Profile, seed int64) *loopback.Pair {
	return loopback.NewPair(loopback.Faults{
		Seed:          seed,
		DropRate:      p.DropRate,
		DuplicateRate: p.DuplicateRate,
		Reorder:       p.Reorder,
		MTU:           p.MTU,
	})
}
