package node

import (
	"bytes"
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

// SC-3: a manual customization commits a new signed appearance revision that
// chains to the previous tip; invalid patches are refused.
func TestAppearancePatchRevisionChain(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpaceWithCharacter("Studio", terminals.DefaultCharacter("studio"))
	if err != nil {
		t.Fatal(err)
	}
	// Tip starts at revision 1 (ephemeral projection).
	f1, _ := rt.AppearanceFrame(tid)
	s1, _ := composition.DecodeSnapshot(f1)
	if s1.Revision != 1 || s1.PreviousHash != nil {
		t.Fatalf("base revision = %d", s1.Revision)
	}

	// A valid customization → revision 2 chained to revision 1.
	f2, err := rt.PatchAppearance(tid, AppearanceOverride{Accent: "#33aa88", Dim: 500, Motion: "still"})
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := composition.DecodeSnapshot(f2)
	if s2.Revision != 2 || s2.PreviousHash == nil || *s2.PreviousHash != composition.Hash(f1) {
		t.Fatalf("revision 2 does not chain to revision 1: %+v", s2)
	}
	if err := composition.ValidateContract(f2); err != nil {
		t.Fatalf("customized frame invalid: %v", err)
	}
	// The applied accent is present.
	a2, _ := composition.DecodeAppearance(s2.Payload)
	found := false
	for _, p := range a2.Palette {
		if p.Name == "accent" && p.Hex == "#33aa88" {
			found = true
		}
	}
	if !found {
		t.Fatal("accent override not applied")
	}

	// Another edit → revision 3 chained to 2.
	f3, err := rt.PatchAppearance(tid, AppearanceOverride{Accent: "#33aa88", Density: "dense"})
	if err != nil {
		t.Fatal(err)
	}
	s3, _ := composition.DecodeSnapshot(f3)
	if s3.Revision != 3 || *s3.PreviousHash != composition.Hash(f2) {
		t.Fatalf("revision 3 chain wrong: %+v", s3)
	}

	// Invalid patch (bad color) is refused; the tip does not move.
	if _, err := rt.PatchAppearance(tid, AppearanceOverride{Accent: "notacolor"}); err == nil {
		t.Fatal("invalid accent accepted")
	}
	fnow, _ := rt.AppearanceFrame(tid)
	if !bytes.Equal(fnow, f3) {
		t.Fatal("a rejected patch moved the tip")
	}

	// Survives restart.
	rt.Close()
	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	fr, _ := rt2.AppearanceFrame(tid)
	sr, _ := composition.DecodeSnapshot(fr)
	if sr.Revision != 3 {
		t.Fatalf("customized appearance did not persist: rev %d", sr.Revision)
	}
}
