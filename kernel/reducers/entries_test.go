package reducers

import (
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/resonance"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func resSetEv(t *testing.T, clock uint64, principal byte, evSeed byte,
	target id.EventID, r resonance.Reaction) ev {
	t.Helper()
	payload, err := (&resonance.SetPayload{Target: target, Reaction: r}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{principal},
			Schema:       resonance.SchemaSet,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{evSeed, byte(clock)},
	}
}

func resClearEv(t *testing.T, clock uint64, principal byte, evSeed byte, target id.EventID) ev {
	t.Helper()
	payload := (&resonance.ClearPayload{Target: target}).Encode()
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{principal},
			Schema:       resonance.SchemaClear,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{evSeed, byte(clock)},
	}
}

func unicodeReaction(v string) resonance.Reaction {
	return resonance.Reaction{Kind: resonance.KindUnicode, Value: v}
}

// Two devices of ONE principal, both offline, both set. Single cardinality:
// exactly one visible reaction for that principal, the later register wins.
func TestResonanceTwoDevicesOnePrincipal(t *testing.T) {
	msg := msgEvent(t, 1, 1, "the take is up")
	r1 := resSetEv(t, 5, 7, 0x51, msg.id, unicodeReaction("🌲"))
	r2 := resSetEv(t, 6, 7, 0x52, msg.id, unicodeReaction("🔥")) // later device wins

	for _, order := range [][]ev{{msg, r1, r2}, {r2, r1, msg}, {r1, msg, r2}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		agg := s.ResonanceFor(msg.id)
		if agg.Total != 1 || len(agg.Groups) != 1 {
			t.Fatalf("single cardinality violated: %+v", agg)
		}
		if agg.Groups[0].Reaction.Value != "🔥" {
			t.Fatalf("later device must win: %+v", agg.Groups[0])
		}
	}
}

// Clear wins over an older set in every arrival order.
func TestResonanceClearWins(t *testing.T) {
	msg := msgEvent(t, 1, 1, "hello")
	set := resSetEv(t, 5, 7, 0x53, msg.id, unicodeReaction("🔥"))
	clear := resClearEv(t, 9, 7, 0x54, msg.id)

	for _, order := range [][]ev{{msg, set, clear}, {clear, set, msg}, {set, clear, msg}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		if agg := s.ResonanceFor(msg.id); agg.Total != 0 {
			t.Fatalf("clear did not win: %+v", agg)
		}
	}
}

// A reaction folding before its target stays unprojected (no phantom entry,
// no aggregate) and appears once the target installs — with NO drain step.
func TestResonanceBeforeTargetUnresolved(t *testing.T) {
	msg := msgEvent(t, 3, 2, "late arrival")
	react := resSetEv(t, 5, 9, 0x55, msg.id, unicodeReaction("❤️"))
	s := NewState()
	s.Apply(react.env, react.id)
	if len(s.Entries()) != 0 {
		t.Fatal("unresolved reaction created a phantom entry")
	}
	if agg := s.ResonanceFor(msg.id); agg.Total != 0 {
		t.Fatal("unresolved target must project empty")
	}
	s.Apply(msg.env, msg.id)
	if agg := s.ResonanceFor(msg.id); agg.Total != 1 {
		t.Fatalf("reaction lost after target resolved: %+v", agg)
	}
}

// An unknown block.* type stays in the feed as an honest fallback entry and
// is reactable like any other object.
func TestUnknownBlockEntryVisible(t *testing.T) {
	payload := buildUnknownBlockPayload(t, "~ aurora field · 7 voices")
	s := NewState()
	s.Apply(&signal.Envelope{
		Principal:    id.PrincipalID{4},
		Schema:       "block.aurora.v9",
		LogicalClock: 2,
		ProducedBy:   signal.AuthorshipAIAgent,
		Payload:      payload,
	}, id.EventID{0x60})
	entries := s.Entries()
	if len(entries) != 1 {
		t.Fatal("unknown block dropped from the feed")
	}
	e := entries[0]
	if e.Kind != KindUnknown || e.Content.Unknown.Fallback != "~ aurora field · 7 voices" {
		t.Fatalf("unknown entry wrong: %+v", e.Content.Unknown)
	}
	if e.ProducedBy != signal.AuthorshipAIAgent {
		t.Fatal("authorship lost on unknown block")
	}
	r := resSetEv(t, 5, 3, 0x61, id.EventID{0x60}, unicodeReaction("🌲"))
	s.Apply(r.env, r.id)
	if agg := s.ResonanceFor(id.EventID{0x60}); agg.Total != 1 {
		t.Fatal("cannot react to unknown block")
	}
}

func buildUnknownBlockPayload(t *testing.T, fallback string) []byte {
	t.Helper()
	b := &schemas.LiveSignalBlock{FallbackText: fallback, Engine: "qs.x.v1", Preset: "z@1"}
	enc, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

// Legacy block.reaction.v1 events are counted, never rendered: no feed
// entry, no aggregate — and never routed into installUnknown.
func TestLegacyReactionIgnoredExplicitly(t *testing.T) {
	msg := msgEvent(t, 1, 1, "old world")
	payload, err := (&schemas.ReactionBlock{Target: msg.id, Emoji: "🌲", Active: true}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	s := NewState()
	s.Apply(msg.env, msg.id)
	s.Apply(&signal.Envelope{
		Principal:    id.PrincipalID{7},
		Schema:       schemas.BlockReaction,
		LogicalClock: 5,
		Payload:      payload,
	}, id.EventID{0x70})
	if len(s.Entries()) != 1 {
		t.Fatalf("legacy reaction must not surface as an entry: %d", len(s.Entries()))
	}
	if agg := s.ResonanceFor(msg.id); agg.Total != 0 {
		t.Fatal("legacy reaction must not enter the resonance aggregate")
	}
	if s.Unsupported["legacy:block.reaction.v1"] != 1 {
		t.Fatalf("legacy reaction not counted: %v", s.Unsupported)
	}
}

// Main Digest AND ResonanceDigest are order-independent together.
func TestEntriesDigestOrderIndependent(t *testing.T) {
	msg := msgEvent(t, 1, 1, "root")
	all := []ev{
		msg,
		resSetEv(t, 4, 5, 0x71, msg.id, unicodeReaction("🌲")),
		resSetEv(t, 6, 6, 0x72, msg.id, unicodeReaction("🌲")),
		resSetEv(t, 7, 5, 0x73, msg.id, unicodeReaction("🔥")), // 5 replaces own
		msgEvent(t, 8, 2, "second"),
	}
	var want, wantRes [32]byte
	for trial := 0; trial < 30; trial++ {
		perm := rand.Perm(len(all))
		s := NewState()
		for _, i := range perm {
			s.Apply(all[i].env, all[i].id)
		}
		got, gotRes := s.Digest(), s.ResonanceDigest()
		if trial == 0 {
			want, wantRes = got, gotRes
		} else if got != want || gotRes != wantRes {
			t.Fatalf("digest diverged on permutation %d", trial)
		}
	}
}
