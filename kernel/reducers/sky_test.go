package reducers

import (
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func strokeEvent(t *testing.T, clock uint64, who byte, sky id.EventID, pts []byte, erase ...id.EventID) ev {
	t.Helper()
	payload, err := (&schemas.SkyStrokeEvent{Sky: sky, Points: pts, Bright: 2, Erase: erase}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{Principal: id.PrincipalID{who}, Schema: schemas.SkyStroke,
			LogicalClock: clock, ProducedBy: signal.AuthorshipHuman, Payload: payload},
		id: id.EventID{who, byte(clock), 0xAA},
	}
}

// The picture is a set: any arrival order gives the same film, an erase
// that outruns its stroke still lands, and only the author's own erase
// counts.
func TestSkyStrokesCommuteAndErasesAreSovereign(t *testing.T) {
	sky := id.EventID{0x5C}
	a1 := strokeEvent(t, 1, 1, sky, []byte{1, 1, 2, 2})
	b1 := strokeEvent(t, 2, 2, sky, []byte{5, 5, 6, 6})
	a2 := strokeEvent(t, 3, 1, sky, []byte{9, 9, 9, 8})
	bErasesA := strokeEvent(t, 4, 2, sky, nil, a1.id) // not his to erase
	aErasesA := strokeEvent(t, 5, 1, sky, nil, a1.id) // her own

	orders := [][]ev{
		{a1, b1, a2, bErasesA, aErasesA},
		{aErasesA, a2, b1, a1, bErasesA}, // the erase before the stroke it names
		{b1, bErasesA, a1, aErasesA, a2},
	}
	var want []id.EventID
	for i, order := range orders {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		got := s.SkyStrokes(sky)
		ids := make([]id.EventID, 0, len(got))
		for _, st := range got {
			ids = append(ids, st.EventID)
		}
		if len(ids) != 2 || ids[0] != b1.id || ids[1] != a2.id {
			t.Fatalf("order %d: film is %v, want [b1 a2] (a1 erased by its author, b's erase of it ignored)", i, ids)
		}
		if i == 0 {
			want = ids
		} else if len(want) != len(ids) {
			t.Fatalf("order %d diverged", i)
		}
		n, hands, _ := s.SkyStats(sky)
		if n != 2 || hands != 2 {
			t.Fatalf("stats: %d strokes %d hands", n, hands)
		}
	}
}

func TestASkyCools(t *testing.T) {
	sky := id.EventID{0x5D}
	s := NewState()
	for i := 0; i < maxStrokesPerSky+5; i++ {
		e := strokeEvent(t, uint64(i+1), byte(1+i%3), sky, []byte{byte(i % 128), 1})
		e.id = id.EventID{byte(i), byte(i >> 8), byte(i >> 16), 1}
		s.Apply(e.env, e.id)
	}
	n, _, evicted := s.SkyStats(sky)
	if n != maxStrokesPerSky || evicted != 5 {
		t.Fatalf("a cooled sky drew %d and refused %d", n, evicted)
	}
}
