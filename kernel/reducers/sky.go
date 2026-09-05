package reducers

// SKY (SK-1): strokes are a commutative set keyed by the sky they belong
// to. The log is the merge; this file is only the projection — ordered
// by (clock, id) so every replica plays the same film — plus the two
// laws the wire cannot enforce on its own: an erase removes only the
// erasing person's OWN strokes, and a sky cools at maxStrokesPerSky
// (further strokes are counted, never drawn — the same eviction honesty
// as observations and annotations).

import (
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

const KindSky EntryKind = "sky"

const maxStrokesPerSky = 4000

// Stroke is one drawn gesture as the reader sees it.
type Stroke struct {
	EventID   id.EventID
	Author    id.PrincipalID
	Points    []byte
	Bright    uint8
	CreatedAt uint64
	Clock     uint64
}

type skyRec struct {
	strokes []Stroke // sorted by (Clock, EventID)
	byID    map[id.EventID]int
	// erased holds erase requests by target, with the principal who asked:
	// applied when the target exists AND was authored by that principal;
	// remembered so an erase that outruns its stroke still lands.
	erased  map[id.EventID]id.PrincipalID
	evicted int
}

func (s *State) skyFor(sky id.EventID) *skyRec {
	if s.skies == nil {
		s.skies = map[id.EventID]*skyRec{}
	}
	rec := s.skies[sky]
	if rec == nil {
		rec = &skyRec{byID: map[id.EventID]int{}, erased: map[id.EventID]id.PrincipalID{}}
		s.skies[sky] = rec
	}
	return rec
}

func (s *State) applySkyStroke(env *signal.Envelope, eid id.EventID) {
	st, err := schemas.DecodeSkyStroke(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.skyFor(st.Sky)
	if st.IsErase() {
		for _, target := range st.Erase {
			rec.erased[target] = env.Principal
			if i, ok := rec.byID[target]; ok && rec.strokes[i].Author == env.Principal {
				rec.remove(i)
			}
		}
		return
	}
	if _, dup := rec.byID[eid]; dup {
		return
	}
	// An erase that arrived first, from the stroke's own author, wins.
	if who, ok := rec.erased[eid]; ok && who == env.Principal {
		return
	}
	if len(rec.strokes) >= maxStrokesPerSky {
		rec.evicted++
		return
	}
	bright := st.Bright
	if bright == 0 {
		bright = 2
	}
	stroke := Stroke{EventID: eid, Author: env.Principal, Points: st.Points,
		Bright: bright, CreatedAt: env.CreatedAt, Clock: env.LogicalClock}
	i := sort.Search(len(rec.strokes), func(i int) bool {
		return later(rec.strokes[i].Clock, rec.strokes[i].EventID, stroke.Clock, stroke.EventID)
	})
	rec.strokes = append(rec.strokes, Stroke{})
	copy(rec.strokes[i+1:], rec.strokes[i:])
	rec.strokes[i] = stroke
	rec.reindex(i)
}

func (r *skyRec) remove(i int) {
	delete(r.byID, r.strokes[i].EventID)
	r.strokes = append(r.strokes[:i], r.strokes[i+1:]...)
	r.reindex(i)
}

func (r *skyRec) reindex(from int) {
	for j := from; j < len(r.strokes); j++ {
		r.byID[r.strokes[j].EventID] = j
	}
}

// SkyStrokes is the picture, in drawing order.
func (s *State) SkyStrokes(sky id.EventID) []Stroke {
	rec := s.skies[sky]
	if rec == nil {
		return nil
	}
	return append([]Stroke(nil), rec.strokes...)
}

// SkyStats says how much of a picture there is: strokes drawn, distinct
// hands, and how many strokes the cooled sky refused.
func (s *State) SkyStats(sky id.EventID) (strokes, hands, evicted int) {
	rec := s.skies[sky]
	if rec == nil {
		return 0, 0, 0
	}
	seen := map[id.PrincipalID]bool{}
	for _, st := range rec.strokes {
		seen[st.Author] = true
	}
	return len(rec.strokes), len(seen), rec.evicted
}
