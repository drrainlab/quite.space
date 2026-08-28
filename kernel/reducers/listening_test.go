package reducers

import (
	"encoding/hex"
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/appdef"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/listening"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func listenInstance(t *testing.T, host byte) (ev, [16]byte) {
	t.Helper()
	var iid [16]byte
	iid[0] = 0x77
	payload, err := appdef.EncodeInstance(&appdef.Instance{
		InstanceID: hex.EncodeToString(iid[:]), AppID: "listening-room",
		Props: map[string]string{"title": "EP demo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{host},
			Schema:       appdef.SchemaInstance,
			LogicalClock: 1,
			Payload:      payload,
		},
		id: id.EventID{0x70},
	}, iid
}

func cmdEvent(t *testing.T, clock uint64, author byte, iid [16]byte,
	epoch, seq uint64, action string, pos uint64) ev {
	t.Helper()
	payload, err := listening.Encode(iid, &listening.Command{
		Action: action, PositionMS: pos, EffectiveAtMS: 1000 + clock,
		SessionEpoch: epoch, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal:    id.PrincipalID{author},
			Schema:       listening.SchemaCommand,
			LogicalClock: clock,
			Payload:      payload,
		},
		id: id.EventID{0xC0, author, byte(clock), byte(epoch), byte(seq)},
	}
}

// TestListeningFoldMatrix: reorder + duplicates converge to the maximum of
// (epoch, sequence, clock, event id); non-host commands are ignored.
func TestListeningFoldMatrix(t *testing.T) {
	inst, iid := listenInstance(t, 1)
	events := []ev{
		inst,
		cmdEvent(t, 2, 1, iid, 1, 1, "play", 0),
		cmdEvent(t, 3, 1, iid, 1, 2, "pause", 42_000),
		cmdEvent(t, 4, 1, iid, 1, 3, "play", 42_000),
		cmdEvent(t, 5, 1, iid, 2, 1, "play", 0),       // Start session → new epoch wins
		cmdEvent(t, 6, 2, iid, 9, 9, "seek", 999_999), // follower forgery: ignored
	}
	events = append(events, events[3]) // duplicate delivery

	for trial := 0; trial < 30; trial++ {
		perm := rand.Perm(len(events))
		s := NewState()
		for _, i := range perm {
			s.Apply(events[i].env, events[i].id)
		}
		sess, ok := s.ListeningSession(iid)
		if !ok {
			t.Fatal("instance not folded")
		}
		if !sess.HasCommand {
			t.Fatal("no command folded")
		}
		if sess.Command.SessionEpoch != 2 || sess.Command.Sequence != 1 ||
			sess.Command.Action != "play" {
			t.Fatalf("wrong winner: %+v", sess.Command)
		}
		if sess.IgnoredNonHost != 1 {
			t.Fatalf("forged command not counted: %d", sess.IgnoredNonHost)
		}
	}
}

// TestListeningEpochBeatsClock: a HIGHER epoch with a LOWER logical clock
// still wins — ordering is (epoch, sequence, clock, id), never wall/clock
// first.
func TestListeningEpochBeatsClock(t *testing.T) {
	inst, iid := listenInstance(t, 1)
	s := NewState()
	s.Apply(inst.env, inst.id)
	late := cmdEvent(t, 100, 1, iid, 1, 50, "pause", 10_000)
	early := cmdEvent(t, 5, 1, iid, 2, 1, "play", 0)
	s.Apply(late.env, late.id)
	s.Apply(early.env, early.id)
	sess, _ := s.ListeningSession(iid)
	if sess.Command.SessionEpoch != 2 {
		t.Fatalf("epoch must dominate clock: %+v", sess.Command)
	}
}

// TestListeningLatePosition: a late joiner folding the same log computes the
// same session state (same winning command → same position formula inputs).
func TestListeningLatePosition(t *testing.T) {
	inst, iid := listenInstance(t, 1)
	events := []ev{
		inst,
		cmdEvent(t, 2, 1, iid, 1, 1, "play", 0),
		cmdEvent(t, 3, 1, iid, 1, 2, "seek", 60_000),
	}
	a, b := NewState(), NewState()
	for _, e := range events {
		a.Apply(e.env, e.id)
	}
	// Late joiner receives the log in reverse.
	for i := len(events) - 1; i >= 0; i-- {
		b.Apply(events[i].env, events[i].id)
	}
	sa, _ := a.ListeningSession(iid)
	sb, _ := b.ListeningSession(iid)
	if sa.Command != sb.Command || sa.EventID != sb.EventID {
		t.Fatalf("late joiner diverged: %+v vs %+v", sa.Command, sb.Command)
	}
}
