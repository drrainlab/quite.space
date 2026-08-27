// SP-3 reducer gates: marker and check-in convergence in any order,
// first-claim-wins for a contested marker_id, deterministic eviction,
// latest-per-member registers, and the quiet feed row.
package reducers

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func markerEvent(t *testing.T, clock uint64, seed byte, markerID [16]byte, text, kind string) ev {
	t.Helper()
	p, err := geo.FromDegrees(59.3+float64(clock)*0.001, 18.0)
	if err != nil {
		t.Fatal(err)
	}
	m := &schemas.PlacedMarker{Text: text, Kind: kind, Point: p, MarkerID: markerID}
	payload, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{Principal: id.PrincipalID{seed}, Schema: schemas.MarkerPlaced,
			LogicalClock: clock, CreatedAt: 1000 + clock, Payload: payload},
		id: id.EventID{seed, byte(clock), 0xF0},
	}
}

func checkinEvent(t *testing.T, clock uint64, seed byte, text string, sos bool) ev {
	t.Helper()
	c := &schemas.Checkin{Text: text, SOS: sos}
	payload, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{Principal: id.PrincipalID{seed}, Schema: schemas.CheckinSent,
			LogicalClock: clock, CreatedAt: 1000 + clock, Payload: payload},
		id: id.EventID{seed, byte(clock), 0xF1},
	}
}

func TestMarkerConvergenceAndContestedID(t *testing.T) {
	var mid [16]byte
	copy(mid[:], []byte("contested-marker"))
	early := markerEvent(t, 2, 1, mid, "searched", "searched")
	late := markerEvent(t, 9, 2, mid, "impostor", "hazard")
	var other [16]byte
	copy(other[:], []byte("another-marker-0"))
	plain := markerEvent(t, 5, 3, other, "мост непроходим", "hazard")
	all := []ev{early, late, plain}

	var want [32]byte
	for trial := 0; trial < 12; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		ms := s.Markers()
		if len(ms) != 2 {
			t.Fatalf("want 2 markers, got %d", len(ms))
		}
		// The contested id belongs to the FIRST claim in the total
		// order, on every replica, in every arrival order.
		for _, m := range ms {
			if m.MarkerID == mid && m.Text != "searched" {
				t.Fatalf("late claim stole the marker id: %+v", m)
			}
		}
		if trial == 0 {
			want = s.Digest()
		} else if s.Digest() != want {
			t.Fatalf("digest diverged on permutation %d", trial)
		}
	}
}

func TestMarkerEvictionDeterminism(t *testing.T) {
	n := maxMarkersPerSpace + 25
	all := make([]ev, 0, n)
	for i := 0; i < n; i++ {
		var mid [16]byte
		mid[0], mid[1], mid[2] = byte(i), byte(i>>8), 0x01 // never all-zero
		e := markerEvent(t, uint64(10+i), byte(1+i%3), mid, fmt.Sprintf("m%d", i), "waypoint")
		e.id = id.EventID{byte(i), byte(i >> 8), 0xF2}
		all = append(all, e)
	}
	var want [32]byte
	var evicted int
	for trial := 0; trial < 6; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		if len(s.Markers()) != maxMarkersPerSpace {
			t.Fatalf("marker list size %d", len(s.Markers()))
		}
		if trial == 0 {
			want, evicted = s.Digest(), s.MarkerEvicted
			if evicted == 0 {
				t.Fatal("no evictions counted")
			}
		} else if s.Digest() != want || s.MarkerEvicted != evicted {
			t.Fatalf("divergence on permutation %d", trial)
		}
	}
}

func TestCheckinRegistersAndFeedRow(t *testing.T) {
	katya, robert := byte(1), byte(2)
	c1 := checkinEvent(t, 3, katya, "✓ check-in", false)
	c2 := checkinEvent(t, 7, katya, "у ручья, всё ок", false)
	c3 := checkinEvent(t, 5, robert, "🆘 SOS", true)
	all := []ev{c1, c2, c3}

	for trial := 0; trial < 12; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		// Latest per member: Katya's later note wins in any order.
		k, ok := s.LatestCheckin(id.PrincipalID{katya})
		if !ok || k.Text != "у ручья, всё ок" {
			t.Fatalf("katya latest wrong: %+v", k)
		}
		r, ok := s.LatestCheckin(id.PrincipalID{robert})
		if !ok || !r.SOS {
			t.Fatalf("robert SOS lost: %+v", r)
		}
		// History holds all three, ascending.
		if h := s.Checkins(); len(h) != 3 || h[0].EventID != c1.id {
			t.Fatalf("history wrong: %d", len(h))
		}
		// The feed carries the quiet rows — the SOS one keyed by the flag.
		var sosSeen bool
		for _, e := range s.Entries() {
			if e.Kind == KindCheckin && e.Content.Checkin.SOS {
				sosSeen = true
			}
		}
		if !sosSeen {
			t.Fatal("SOS row missing from the feed")
		}
	}
}

// The extended shuffled world: a geo-bearing place, a marker, check-ins
// and an SOS beside the SP-1/2 vocabulary — one digest on every node.
func TestShuffledFieldConvergence(t *testing.T) {
	place := testOID(0x31)
	pl, err := geo.FromDegrees(59.33, 18.04)
	if err != nil {
		t.Fatal(err)
	}
	placeEv := objGeoRevisionEvent(t, 1, 1, place, "Sector B4", pl, 400)
	var mid [16]byte
	copy(mid[:], []byte("field-marker-0001"))
	mk := markerEvent(t, 4, 2, mid, "северное здание чисто", "searched")
	ci := checkinEvent(t, 6, 3, "✓ check-in", false)
	sos := checkinEvent(t, 8, 1, "🆘 SOS", true)
	world := []ev{placeEv, mk, ci, sos}

	states := make([]*State, 3)
	for i := range states {
		states[i] = NewState()
		for _, j := range rand.Perm(len(world)) {
			states[i].Apply(world[j].env, world[j].id)
		}
	}
	d0 := states[0].Digest()
	for i, s := range states {
		if s.Digest() != d0 {
			t.Fatalf("node %d digest diverged", i)
		}
		o, ok := s.ObjectByID(place)
		if !ok || o.Record.Geo == nil || o.Record.Geo.RadiusM != 400 {
			t.Fatalf("node %d place geo wrong", i)
		}
		if len(s.Markers()) != 1 || s.Markers()[0].Kind != "searched" {
			t.Fatalf("node %d markers wrong", i)
		}
		if r, ok := s.LatestCheckin(id.PrincipalID{1}); !ok || !r.SOS {
			t.Fatalf("node %d SOS register wrong", i)
		}
	}
}

// objGeoRevisionEvent builds an object.created.v1 whose record carries geo.
func objGeoRevisionEvent(t *testing.T, clock uint64, seed byte, oid [16]byte, name string, p geo.Point, radius uint64) ev {
	t.Helper()
	r := &objects.Record{ObjectID: oid, Kind: "sector", Name: name,
		Geo: &objects.GeoShape{Point: p, RadiusM: radius}}
	enc, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{Principal: id.PrincipalID{seed}, Schema: objects.SchemaCreated,
			LogicalClock: clock, CreatedAt: 1000 + clock,
			Payload: (&objects.RevisionPayload{Fallback: name, Record: enc}).Encode()},
		id: id.EventID{seed, byte(clock), 0xF3},
	}
}
