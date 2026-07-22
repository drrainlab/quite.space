package reducers

import (
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/keep"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func keptEvent(t *testing.T, clock uint64, author byte, target id.EventID, note string) ev {
	t.Helper()
	payload, err := (&keep.Kept{Target: target, Note: note}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{author},
			Schema:       keep.SchemaKept,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{0xA0, author, byte(clock)},
	}
}

func unkeptEvent(t *testing.T, clock uint64, signer, keepAuthor byte, target id.EventID) ev {
	t.Helper()
	payload := (&keep.Unkept{Target: target, KeepAuthor: id.PrincipalID{keepAuthor}}).Encode()
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{signer},
			Schema:       keep.SchemaUnkept,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{0xB0, signer, byte(clock)},
	}
}

// TestKeepORAcrossPeopleLWWWithin is the LR-1 semantics matrix.
func TestKeepORAcrossPeopleLWWWithin(t *testing.T) {
	msg := msgEvent(t, 1, 1, "the moment")
	target := msg.id

	alice, bob := byte(1), byte(2)

	// Double keep by Alice (idempotent), keep by Bob, then Alice unkeeps:
	// her whole state clears in one step, Bob's keep remains (OR).
	events := []ev{
		msg,
		keptEvent(t, 2, alice, target, "love this"),
		keptEvent(t, 3, alice, target, "love this even more"), // duplicate keep, LWW note
		keptEvent(t, 4, bob, target, ""),
		unkeptEvent(t, 5, alice, alice, target),
	}
	s := NewState()
	for _, e := range events {
		s.Apply(e.env, e.id)
	}
	shelf := s.Shelf()
	if len(shelf) != 1 {
		t.Fatalf("expected 1 shelf item, got %d", len(shelf))
	}
	if len(shelf[0].Keepers) != 1 || shelf[0].Keepers[0].Author != (id.PrincipalID{bob}) {
		t.Fatalf("expected only Bob keeping, got %+v", shelf[0].Keepers)
	}

	// Alice re-keeps → returns.
	rekeep := keptEvent(t, 6, alice, target, "back")
	s.Apply(rekeep.env, rekeep.id)
	if n := s.KeepCount(target); n != 2 {
		t.Fatalf("after re-keep expected 2 keepers, got %d", n)
	}

	// Bob unkeeps → only Alice left; then Alice unkeeps → shelf empty (OR).
	s.Apply(unkeptEvent(t, 7, bob, bob, target).env, id.EventID{0xB0, bob, 7})
	s.Apply(unkeptEvent(t, 8, alice, alice, target).env, id.EventID{0xB0, alice, 8})
	if len(s.Shelf()) != 0 {
		t.Fatalf("shelf should be empty, got %+v", s.Shelf())
	}
}

// TestKeepReorderDuplicateConvergence applies the same event set in random
// permutations; the Shelf projection must converge.
func TestKeepReorderDuplicateConvergence(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	target := msg.id
	events := []ev{
		msg,
		keptEvent(t, 2, 1, target, "a"),
		keptEvent(t, 4, 1, target, "b"),   // later note wins
		unkeptEvent(t, 3, 1, 1, target),   // between the two keeps
		keptEvent(t, 2, 2, target, "bob"), // second person
	}
	// duplicate delivery of the same keep event
	events = append(events, events[1])

	check := func(s *State) {
		shelf := s.Shelf()
		if len(shelf) != 1 {
			t.Fatalf("expected 1 item, got %d", len(shelf))
		}
		if len(shelf[0].Keepers) != 2 {
			t.Fatalf("expected 2 keepers, got %+v", shelf[0].Keepers)
		}
		for _, k := range shelf[0].Keepers {
			if k.Author == (id.PrincipalID{1}) && k.Note != "b" {
				t.Fatalf("LWW note wrong: %q", k.Note)
			}
		}
	}
	for trial := 0; trial < 30; trial++ {
		perm := rand.Perm(len(events))
		s := NewState()
		for _, i := range perm {
			s.Apply(events[i].env, events[i].id)
		}
		check(s)
	}
}

// TestUnkeepAuthorization: signer ≠ keep_author is ignored unless the signer
// is the space controller (moderation).
func TestUnkeepAuthorization(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	target := msg.id

	s := NewState()
	s.Apply(msg.env, msg.id)
	k := keptEvent(t, 2, 1, target, "")
	s.Apply(k.env, k.id)

	// Mallory (3) tries to unkeep Alice's (1) keep — ignored.
	evil := unkeptEvent(t, 3, 3, 1, target)
	s.Apply(evil.env, evil.id)
	if n := s.KeepCount(target); n != 1 {
		t.Fatalf("forged unkeep must be ignored, count=%d", n)
	}
	if s.Unsupported["keep:unauthorized_unkeep"] != 1 {
		t.Fatalf("unauthorized unkeep not counted: %v", s.Unsupported)
	}

	// The controller (9) moderates Alice's keep away — honored.
	ctrl := id.PrincipalID{9}
	s.Controller = &ctrl
	mod := unkeptEvent(t, 4, 9, 1, target)
	s.Apply(mod.env, mod.id)
	if n := s.KeepCount(target); n != 0 {
		t.Fatalf("controller moderation must apply, count=%d", n)
	}
}

// TestKeepAllowlist: a keep of a non-keepable event (a reaction-like or
// unknown target kind) is rejected at fold — regardless of arrival order.
func TestKeepAllowlist(t *testing.T) {
	// Target arrives first: a live signal entry (not keepable).
	sig := ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{1},
			Schema:       "block.live_signal.v1",
			LogicalClock: 1,
			Payload:      mustSignalPayload(t),
		},
		id: id.EventID{0x51},
	}
	s := NewState()
	s.Apply(sig.env, sig.id)
	k := keptEvent(t, 2, 2, sig.id, "")
	s.Apply(k.env, k.id)
	if len(s.Shelf()) != 0 {
		t.Fatalf("live signal must not be keepable")
	}
	if s.Unsupported["keep:not_keepable"] == 0 {
		t.Fatalf("allowlist rejection not counted: %v", s.Unsupported)
	}

	// Reverse order: keep folds first (pending), the target resolves to a
	// non-keepable kind → the keep is discarded on resolution.
	s2 := NewState()
	s2.Apply(k.env, k.id)
	s2.Apply(sig.env, sig.id)
	if len(s2.Shelf()) != 0 {
		t.Fatalf("keep-then-target must also reject")
	}
	if s2.Unsupported["keep:not_keepable"] == 0 {
		t.Fatalf("resolution rejection not counted: %v", s2.Unsupported)
	}
}

// TestKeepTombstonedPlaceholder: a kept then tombstoned target stays on the
// Shelf as Removed — the keep never vanishes silently.
func TestKeepTombstonedPlaceholder(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	s := NewState()
	s.Apply(msg.env, msg.id)
	k := keptEvent(t, 2, 2, msg.id, "important")
	s.Apply(k.env, k.id)
	tomb := tombEvent(t, 3, msg.id)
	s.Apply(tomb.env, tomb.id)

	shelf := s.Shelf()
	if len(shelf) != 1 || !shelf[0].Removed {
		t.Fatalf("expected removed placeholder, got %+v", shelf)
	}
}

// TestKeepPendingLimits: unresolved keeps are bounded by count with oldest
// eviction; a resolved target is never evicted.
func TestKeepPendingLimits(t *testing.T) {
	s := NewState()
	msg := msgEvent(t, 1, 1, "live")
	s.Apply(msg.env, msg.id)
	liveKeep := keptEvent(t, 2, 1, msg.id, "")
	s.Apply(liveKeep.env, liveKeep.id)

	// Flood with keeps of unknown targets.
	for i := 0; i < maxPendingKeepTargets+50; i++ {
		var target id.EventID
		target[0], target[1], target[2] = 0xEE, byte(i>>8), byte(i)
		k := keptEvent(t, uint64(10+i), 3, target, "")
		s.Apply(k.env, k.id)
	}
	if len(s.keepPending) > maxPendingKeepTargets {
		t.Fatalf("pending overflow: %d", len(s.keepPending))
	}
	if s.Unsupported["keep:pending_evicted"] == 0 {
		t.Fatalf("eviction not counted")
	}
	// The resolved keep survives the flood.
	if n := s.KeepCount(msg.id); n != 1 {
		t.Fatalf("resolved keep lost in eviction, count=%d", n)
	}

	// Memory bound: unresolved keeps with fat notes evict by bytes long
	// before the count cap.
	s2 := NewState()
	note := string(make([]byte, keep.MaxNoteLen))
	for i := 0; i < 200; i++ {
		var target id.EventID
		target[0], target[1] = 0xDD, byte(i)
		k := keptEvent(t, uint64(10+i), 3, target, note)
		s2.Apply(k.env, k.id)
	}
	if s2.Unsupported["keep:pending_evicted"] == 0 {
		t.Fatalf("byte-budget eviction not triggered")
	}
}

// TestKeepUnkeepBeforeKeep: an unkeep folding before an OLDER keep must win
// (the register exists before the target's keep arrives).
func TestKeepUnkeepBeforeKeep(t *testing.T) {
	msg := msgEvent(t, 1, 1, "m")
	s := NewState()
	s.Apply(msg.env, msg.id)
	// Unkeep at clock 5 folds first; keep at clock 3 folds after — loses.
	u := unkeptEvent(t, 5, 1, 1, msg.id)
	s.Apply(u.env, u.id)
	k := keptEvent(t, 3, 1, msg.id, "old")
	s.Apply(k.env, k.id)
	if n := s.KeepCount(msg.id); n != 0 {
		t.Fatalf("older keep must lose to newer unkeep, count=%d", n)
	}
}

func mustSignalPayload(t *testing.T) []byte {
	t.Helper()
	p, err := (&schemas.LiveSignalBlock{
		FallbackText: "aurora", Engine: "qs.live_signal.v1", Preset: "aurora@1",
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
