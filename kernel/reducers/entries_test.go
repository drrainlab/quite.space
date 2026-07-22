package reducers

import (
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func reactionEv(t *testing.T, clock uint64, principal byte, evSeed byte,
	target id.EventID, emoji string, active bool) ev {
	t.Helper()
	payload, err := (&schemas.ReactionBlock{Target: target, Emoji: emoji, Active: active}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{principal},
			Schema:       schemas.BlockReaction,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{evSeed, byte(clock)},
	}
}

// Two devices of ONE principal, both offline, both say active=true. With
// toggle semantics the reaction would vanish; with state-based LWW it
// stays — exactly one visible reaction (plan §4).
func TestReactionTwoDevicesBothSetConvergesToSet(t *testing.T) {
	msg := msgEvent(t, 1, 1, "the take is up")
	r1 := reactionEv(t, 5, 7, 0x51, msg.id, "🌲", true) // device A
	r2 := reactionEv(t, 6, 7, 0x52, msg.id, "🌲", true) // device B, later clock

	for _, order := range [][]ev{{msg, r1, r2}, {r2, r1, msg}, {r1, msg, r2}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		entries := s.Entries()
		if len(entries) != 1 {
			t.Fatalf("entries: %d", len(entries))
		}
		ps := entries[0].Reactions["🌲"]
		if len(ps) != 1 || ps[0] != (id.PrincipalID{7}) {
			t.Fatalf("reaction state wrong: %+v", entries[0].Reactions)
		}
	}
}

func TestReactionUnsetWinsByOrder(t *testing.T) {
	msg := msgEvent(t, 1, 1, "hello")
	set := reactionEv(t, 5, 7, 0x53, msg.id, "🔥", true)
	unset := reactionEv(t, 9, 7, 0x54, msg.id, "🔥", false) // later: remove

	for _, order := range [][]ev{{msg, set, unset}, {unset, set, msg}, {set, unset, msg}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		if len(s.Entries()[0].Reactions) != 0 {
			t.Fatalf("unset did not win: %+v", s.Entries()[0].Reactions)
		}
	}
}

func TestReactionBeforeTargetIsPending(t *testing.T) {
	msg := msgEvent(t, 3, 2, "late arrival")
	react := reactionEv(t, 5, 9, 0x55, msg.id, "❤️", true)
	s := NewState()
	s.Apply(react.env, react.id) // reaction first
	if len(s.Entries()) != 0 {
		t.Fatal("pending reaction created a phantom entry")
	}
	s.Apply(msg.env, msg.id)
	entries := s.Entries()
	if len(entries) != 1 || len(entries[0].Reactions["❤️"]) != 1 {
		t.Fatalf("pending reaction lost: %+v", entries)
	}
}

// Plan verification #1 at the reducer level: an unknown block.* type stays
// in the feed as an honest fallback entry.
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
	// Reactions attach to unknown entries like to any other.
	r := reactionEv(t, 5, 3, 0x61, id.EventID{0x60}, "🌲", true)
	s.Apply(r.env, r.id)
	if len(s.Entries()[0].Reactions["🌲"]) != 1 {
		t.Fatal("cannot react to unknown block")
	}
}

func buildUnknownBlockPayload(t *testing.T, fallback string) []byte {
	t.Helper()
	// {1: fallback, 42: {7: "future"}}
	b := &schemas.LiveSignalBlock{FallbackText: fallback, Engine: "qs.x.v1", Preset: "z@1"}
	enc, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestEntriesDigestOrderIndependent(t *testing.T) {
	msg := msgEvent(t, 1, 1, "root")
	all := []ev{
		msg,
		reactionEv(t, 4, 5, 0x71, msg.id, "🌲", true),
		reactionEv(t, 6, 6, 0x72, msg.id, "🌲", true),
		reactionEv(t, 7, 5, 0x73, msg.id, "🔥", true),
		msgEvent(t, 8, 2, "second"),
	}
	var want [32]byte
	for trial := 0; trial < 30; trial++ {
		perm := rand.Perm(len(all))
		s := NewState()
		for _, i := range perm {
			s.Apply(all[i].env, all[i].id)
		}
		got := s.Digest()
		if trial == 0 {
			want = got
		} else if got != want {
			t.Fatalf("digest diverged on permutation %d", trial)
		}
	}
}
