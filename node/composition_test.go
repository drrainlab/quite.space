package node

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/composition"
	"github.com/drrainlab/quiet_places/terminals"
)

// The owner projects + signs; a holder of the frame validates and renders it
// with no ownership and no replay (ADR-013 visitor property).
func TestComposeSnapshotForeignValidation(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpaceWithCharacter("Studio", terminals.DefaultCharacter("studio"))
	if err != nil {
		t.Fatal(err)
	}

	frames := map[string]func() ([]byte, error){
		"appearance":   func() ([]byte, error) { return rt.AppearanceFrame(tid) },
		"composition":  func() ([]byte, error) { return rt.CompositionFrame(tid) },
		"bundle_index": func() ([]byte, error) { return rt.BundleIndexFrame(tid) },
	}
	for kind, fn := range frames {
		frame, err := fn()
		if err != nil {
			t.Fatalf("%s frame: %v", kind, err)
		}
		// Foreign validation: only the bytes, no runtime, no replay.
		if err := composition.ValidateContract(frame); err != nil {
			t.Fatalf("%s failed foreign validation: %v", kind, err)
		}
		s, err := composition.DecodeSnapshot(frame)
		if err != nil {
			t.Fatal(err)
		}
		if s.SpaceID != tid {
			t.Fatalf("%s space id mismatch", kind)
		}
	}
}
