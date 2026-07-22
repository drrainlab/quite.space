package reducers

import (
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

type ev struct {
	env *signal.Envelope
	id  id.EventID
}

func msgEvent(t *testing.T, clock uint64, seed byte, text string) ev {
	t.Helper()
	payload, err := (&schemas.TextMessage{Text: text}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{seed},
			Schema:       schemas.MessageText,
			LogicalClock: clock,
			ProducedBy:   signal.AuthorshipHuman,
			Payload:      payload,
		},
		id: id.EventID{seed, byte(clock)},
	}
}

func tombEvent(t *testing.T, clock uint64, target id.EventID) ev {
	t.Helper()
	payload := (&schemas.Tombstone{Target: target}).Encode()
	return ev{
		env: &signal.Envelope{
			Schema:       schemas.MessageTombstoned,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{0xF0, byte(clock)},
	}
}

func TestOrderIndependentConvergence(t *testing.T) {
	events := []ev{
		msgEvent(t, 1, 1, "first"),
		msgEvent(t, 2, 2, "second"),
		msgEvent(t, 3, 1, "third"),
	}
	tomb := tombEvent(t, 4, events[1].id)
	all := append(events, tomb)

	// Apply in 20 random permutations; digests must match.
	var want [32]byte
	for trial := 0; trial < 20; trial++ {
		perm := rand.Perm(len(all))
		s := NewState()
		for _, i := range perm {
			s.Apply(all[i].env, all[i].id)
		}
		got := s.Digest()
		if trial == 0 {
			want = got
			msgs := s.Messages()
			if len(msgs) != 2 {
				t.Fatalf("expected 2 live messages, got %d", len(msgs))
			}
			if msgs[0].Text != "first" || msgs[1].Text != "third" {
				t.Fatalf("order wrong: %q %q", msgs[0].Text, msgs[1].Text)
			}
		} else if got != want {
			t.Fatalf("digest diverged on permutation %d", trial)
		}
	}
}

func TestTombstoneBeforeCreate(t *testing.T) {
	m := msgEvent(t, 5, 3, "doomed")
	tomb := tombEvent(t, 6, m.id)
	s := NewState()
	s.Apply(tomb.env, tomb.id) // tombstone arrives first
	s.Apply(m.env, m.id)
	if len(s.Messages()) != 0 {
		t.Fatal("tombstoned message visible")
	}
}

func TestRevisionLWW(t *testing.T) {
	m := msgEvent(t, 1, 4, "original")
	target := m.id
	rev := func(clock uint64, text string, seed byte) ev {
		payload, err := (&schemas.TextMessage{Text: text, ReplyTo: &target}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		return ev{
			env: &signal.Envelope{Schema: schemas.MessageRevised,
				LogicalClock: clock, Payload: payload},
			id: id.EventID{seed, byte(clock)},
		}
	}
	r1 := rev(3, "edit one", 5)
	r2 := rev(7, "edit two", 6)

	for _, order := range [][]ev{{m, r1, r2}, {r2, r1, m}, {r1, m, r2}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		msgs := s.Messages()
		if len(msgs) != 1 || msgs[0].Text != "edit two" || !msgs[0].Revised {
			t.Fatalf("LWW failed: %+v", msgs)
		}
	}
}

func TestCardLifecycle(t *testing.T) {
	created := func() ev {
		payload, err := (&schemas.Card{Title: "Record ambience", Status: "open"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		return ev{
			env: &signal.Envelope{Schema: schemas.CardCreated,
				LogicalClock: 1, Payload: payload},
			id: id.EventID{0x10},
		}
	}()
	cardID := created.id
	updated := func() ev {
		payload, err := (&schemas.Card{Title: "Record ambience", Status: "done", Card: &cardID}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		return ev{
			env: &signal.Envelope{Schema: schemas.CardUpdated,
				LogicalClock: 5, Payload: payload},
			id: id.EventID{0x11},
		}
	}()

	for _, order := range [][]ev{{created, updated}, {updated, created}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		cards := s.Cards()
		if len(cards) != 1 || cards[0].Status != "done" {
			t.Fatalf("card state wrong: %+v", cards)
		}
	}
}

func TestUnknownSchemaCounted(t *testing.T) {
	s := NewState()
	s.Apply(&signal.Envelope{Schema: "future.thing.v9", LogicalClock: 1,
		Payload: []byte{0xa0}}, id.EventID{0x20})
	if s.Unsupported["future.thing.v9"] != 1 {
		t.Fatal("unknown schema not counted")
	}
	if len(s.Messages()) != 0 {
		t.Fatal("unknown schema leaked into views")
	}
}

func TestAuthorshipSurvivesReduction(t *testing.T) {
	payload, err := (&schemas.TextMessage{Text: "summary of the thread"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	s := NewState()
	s.Apply(&signal.Envelope{
		Schema:       schemas.MessageText,
		LogicalClock: 1,
		ProducedBy:   signal.AuthorshipAIAgent,
		Payload:      payload,
	}, id.EventID{0x30})
	msgs := s.Messages()
	if len(msgs) != 1 || msgs[0].ProducedBy != signal.AuthorshipAIAgent {
		t.Fatal("AI authorship lost in materialized view")
	}
}
