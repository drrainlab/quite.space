package reducers

import (
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/resonance"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func semantic(key, fb string) resonance.Reaction {
	return resonance.Reaction{Kind: resonance.KindSemantic, Key: key, Fallback: fb}
}

func paletteEv(t *testing.T, clock uint64, principal byte, evSeed byte, pal resonance.Palette) ev {
	t.Helper()
	payload, err := pal.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{principal},
			Schema:       resonance.SchemaPalette,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{evSeed, byte(clock)},
	}
}

// TestResonancePermutationMatrix: 3 actors × set/replace/clear/re-set over
// one entry — every arrival order converges to identical projection AND
// identical ResonanceDigest.
func TestResonancePermutationMatrix(t *testing.T) {
	msg := msgEvent(t, 1, 1, "the moment")
	target := msg.id
	events := []ev{
		msg,
		resSetEv(t, 2, 1, 0xA1, target, semantic("warmth", "♡")),
		resSetEv(t, 4, 1, 0xA2, target, semantic("spark", "✦")), // 1 replaces
		resSetEv(t, 3, 2, 0xA3, target, semantic("warmth", "♡")),
		resClearEv(t, 5, 2, 0xA4, target),                     // 2 clears
		resSetEv(t, 6, 2, 0xA5, target, unicodeReaction("🌲")), // 2 re-sets
		resSetEv(t, 2, 3, 0xA6, target, semantic("warmth", "♡")),
	}
	events = append(events, events[1]) // duplicate delivery

	var wantRes [32]byte
	for trial := 0; trial < 40; trial++ {
		perm := rand.Perm(len(events))
		s := NewState()
		for _, i := range perm {
			s.Apply(events[i].env, events[i].id)
		}
		agg := s.ResonanceFor(target)
		// Final registers: 1→spark, 2→🌲, 3→warmth.
		if agg.Total != 3 || len(agg.Groups) != 3 {
			t.Fatalf("trial %d: wrong aggregate %+v", trial, agg)
		}
		// Canonical group order: s:spark, s:warmth, u:🌲.
		if agg.Groups[0].Reaction.Key != "spark" ||
			agg.Groups[1].Reaction.Key != "warmth" ||
			agg.Groups[2].Reaction.Value != "🌲" {
			t.Fatalf("trial %d: group order wrong %+v", trial, agg.Groups)
		}
		got := s.ResonanceDigest()
		if trial == 0 {
			wantRes = got
		} else if got != wantRes {
			t.Fatalf("resonance state diverged on permutation %d", trial)
		}
	}
}

// A clear from one actor plus a set from another keeps the count but MUST
// bump the revision — the client arrival keys off it.
func TestResonanceRevisionBumpsOnEqualCount(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	s := NewState()
	s.Apply(msg.env, msg.id)
	a := resSetEv(t, 2, 1, 0xB1, msg.id, semantic("warmth", "♡"))
	s.Apply(a.env, a.id)
	agg1 := s.ResonanceFor(msg.id)

	// Actor 1 clears, actor 2 sets warmth: count 1 → 1.
	c := resClearEv(t, 3, 1, 0xB2, msg.id)
	s.Apply(c.env, c.id)
	b := resSetEv(t, 4, 2, 0xB3, msg.id, semantic("warmth", "♡"))
	s.Apply(b.env, b.id)
	agg2 := s.ResonanceFor(msg.id)
	if agg2.Total != 1 || agg1.Total != 1 {
		t.Fatalf("counts wrong: %d then %d", agg1.Total, agg2.Total)
	}
	if agg2.RevClock == agg1.RevClock && agg2.RevEID == agg1.RevEID {
		t.Fatal("revision must change when membership changed at equal count")
	}
}

// Unknown semantic keys keep the deterministically-chosen wire fallback:
// the register latest in (clock, eid) supplies the group's representative.
func TestResonanceUnknownKeyWireFallbackDeterministic(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	e1 := resSetEv(t, 2, 1, 0xC1, msg.id, semantic("pinevibes.drift", "🌫️"))
	e2 := resSetEv(t, 3, 2, 0xC2, msg.id, semantic("pinevibes.drift", "🌀"))
	for _, order := range [][]ev{{msg, e1, e2}, {e2, e1, msg}, {e1, msg, e2}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		agg := s.ResonanceFor(msg.id)
		if len(agg.Groups) != 1 || agg.Groups[0].Count != 2 {
			t.Fatalf("grouping wrong: %+v", agg)
		}
		// Latest register (clock 3) carries 🌀 — same in every order.
		if agg.Groups[0].Reaction.Fallback != "🌀" {
			t.Fatalf("wire fallback not deterministic: %+v", agg.Groups[0].Reaction)
		}
	}
}

// Tombstoned target hides its aggregate; the registers survive.
func TestResonanceTombstoneHides(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	s := NewState()
	s.Apply(msg.env, msg.id)
	r := resSetEv(t, 2, 2, 0xD1, msg.id, semantic("warmth", "♡"))
	s.Apply(r.env, r.id)
	tomb := tombEvent(t, 3, msg.id)
	s.Apply(tomb.env, tomb.id)
	if agg := s.ResonanceFor(msg.id); agg.Total != 0 {
		t.Fatalf("tombstoned target must hide reactions: %+v", agg)
	}
}

// Palette: non-controller events are ignored and counted; controller LWW
// replaces; ActivePalette falls back to the default.
func TestResonancePaletteAuthorization(t *testing.T) {
	s := NewState()
	if pal, own := s.ActivePalette(); own || pal.PaletteID != "pine-vibes.v1" {
		t.Fatalf("default palette expected, got %q own=%v", pal.PaletteID, own)
	}

	ctrl := id.PrincipalID{9}
	s.Controller = &ctrl

	forged := resonance.DefaultPalette()
	forged.PaletteID = "evil.v1"
	f := paletteEv(t, 2, 3, 0xE1, forged) // signer 3 ≠ controller
	s.Apply(f.env, f.id)
	if _, own := s.ActivePalette(); own {
		t.Fatal("forged palette must be ignored")
	}
	if s.Unsupported["resonance:unauthorized_palette"] != 1 {
		t.Fatalf("forged palette not counted: %v", s.Unsupported)
	}

	own1 := resonance.DefaultPalette()
	own1.PaletteID = "studio.v1"
	p1 := paletteEv(t, 3, 9, 0xE2, own1)
	s.Apply(p1.env, p1.id)
	own2 := resonance.DefaultPalette()
	own2.PaletteID = "studio.v2"
	p2 := paletteEv(t, 5, 9, 0xE3, own2)
	// Apply newer first, older second: LWW keeps v2.
	s2 := NewState()
	s2.Controller = &ctrl
	s2.Apply(p2.env, p2.id)
	s2.Apply(p1.env, p1.id)
	if pal, _ := s2.ActivePalette(); pal.PaletteID != "studio.v2" {
		t.Fatalf("palette LWW wrong: %q", pal.PaletteID)
	}
}

// Reactions land on publication stable targets and app-instance events too.
func TestResonanceOnPublicationTarget(t *testing.T) {
	// Fold a minimal publication via the reducers' own pubRecFor path: use a
	// real revision event would need full doc encoding; instead register the
	// target through an app instance which exercises the same status path.
	inst, iid := listenInstance(t, 1)
	s := NewState()
	// React BEFORE the instance exists (unresolved), then instance arrives.
	r := resSetEv(t, 2, 2, 0xF1, inst.id, semantic("join", "↗"))
	s.Apply(r.env, r.id)
	if agg := s.ResonanceFor(inst.id); agg.Total != 0 {
		t.Fatal("unresolved app target must project empty")
	}
	s.Apply(inst.env, inst.id)
	if agg := s.ResonanceFor(inst.id); agg.Total != 1 {
		t.Fatalf("app-instance reaction lost: %+v", agg)
	}
	_ = iid
}

// OwnResonance is a viewer projection over the same registers.
func TestOwnResonance(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	s := NewState()
	s.Apply(msg.env, msg.id)
	me := id.PrincipalID{5}
	r := resSetEv(t, 2, 5, 0xF5, msg.id, semantic("curious", "⌁"))
	s.Apply(r.env, r.id)
	own, ok := s.OwnResonance(msg.id, me)
	if !ok || own.Key != "curious" {
		t.Fatalf("own resonance wrong: %+v %v", own, ok)
	}
	c := resClearEv(t, 3, 5, 0xF6, msg.id)
	s.Apply(c.env, c.id)
	if _, ok := s.OwnResonance(msg.id, me); ok {
		t.Fatal("own resonance must clear")
	}
}
