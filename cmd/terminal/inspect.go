package main

import (
	"fmt"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/bundle"
)

// runInspect prints the diagnostic view of a bundle (ADR-003: JSON/text
// diagnostics are derived, never signed).
func runInspect(args []string) error {
	flags := parseFlags(args)
	path := flags["bundle"]
	if path == "" {
		return fmt.Errorf("inspect: --bundle FILE required")
	}
	term, frames, err := bundle.Read(path)
	if err != nil {
		return err
	}
	fmt.Printf("bundle for terminal %s — %d frames\n", term, len(frames))
	for i, f := range frames {
		env, err := signal.Decode(f)
		if err != nil {
			fmt.Printf("%3d. MALFORMED: %v\n", i+1, err)
			continue
		}
		verified := "signature OK"
		if err := signal.VerifyFrame(f, env); err != nil {
			verified = "SIGNATURE INVALID"
		}
		known := "known"
		if !schemas.Known(env.Schema) {
			known = "unknown schema (kept opaque)"
		}
		fmt.Printf("%3d. %s seq=%d clock=%d schema=%s authorship=%s %s, %s\n",
			i+1, id.EventIDOf(f), env.Sequence, env.LogicalClock,
			env.Schema, env.ProducedBy, verified, known)
	}
	return nil
}
