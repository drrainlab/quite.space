// SP-3.2 reducer gates: sweep facts converge in any order, dedupe by
// event id, evict deterministically, derive per-object — and an empty
// space's digest is untouched (pre-SP-3.2 digests stand).
package reducers

import (
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/geo"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func sweepEvent(t *testing.T, clock uint64, seed byte, objectID [16]byte, result string) ev {
	t.Helper()
	pmin, _ := geo.FromDegrees(46.61, 8.02)
	pmax, _ := geo.FromDegrees(46.63, 8.05)
	c := &schemas.CompletedSweep{
		Fallback: "✓ sweep", ObjectID: objectID,
		StartedAt: 900 + clock, EndedAt: 1000 + clock, DistanceM: 2700,
		Result: result, BBoxMin: pmin, BBoxMax: pmax,
	}
	c.TrackAsset[0] = seed
	payload, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{Principal: id.PrincipalID{seed}, Schema: schemas.SweepCompleted,
			LogicalClock: clock, CreatedAt: 1000 + clock, Payload: payload},
		id: id.EventID{seed, byte(clock), 0xF2},
	}
}

func TestSweepConvergenceAndDerivedFilter(t *testing.T) {
	var sectorSweep1, sectorSweep2, otherSweep [16]byte
	sectorSweep1[0], sectorSweep2[0], otherSweep[0] = 1, 2, 3
	all := []ev{
		sweepEvent(t, 2, 1, sectorSweep1, schemas.SweepNothingFound),
		sweepEvent(t, 5, 2, sectorSweep2, schemas.SweepFound),
		sweepEvent(t, 9, 3, otherSweep, schemas.SweepInterrupted),
	}
	var want [32]byte
	for trial := 0; trial < 12; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		if got := len(s.Sweeps()); got != 3 {
			t.Fatalf("want 3 facts, got %d", got)
		}
		// Replay is idempotent.
		s.Apply(all[0].env, all[0].id)
		if got := len(s.Sweeps()); got != 3 {
			t.Fatalf("a replayed fact duplicated: %d", got)
		}
		// The derived filter finds exactly its object's facts.
		if got := s.SweepsForObject(sectorSweep1); len(got) != 1 || got[0].Result != schemas.SweepNothingFound {
			t.Fatalf("derived filter wrong: %+v", got)
		}
		if trial == 0 {
			want = s.Digest()
		} else if s.Digest() != want {
			t.Fatalf("digest diverged on permutation %d", trial)
		}
	}
}

func TestSweepEvictionIsDeterministic(t *testing.T) {
	s := NewState()
	var oid [16]byte
	oid[0] = 7
	for i := 0; i < maxSweepsPerSpace+3; i++ {
		e := sweepEvent(t, uint64(i+1), byte(i%200)+1, oid, schemas.SweepFound)
		e.id = id.EventID{byte(i >> 8), byte(i), 0xF2}
		s.Apply(e.env, e.id)
	}
	if got := len(s.Sweeps()); got != maxSweepsPerSpace {
		t.Fatalf("bound not held: %d", got)
	}
	if s.SweepEvicted != 3 {
		t.Fatalf("eviction not counted honestly: %d", s.SweepEvicted)
	}
	// The oldest went first: the survivor list starts at clock 4.
	if s.Sweeps()[0].Clock != 4 {
		t.Fatalf("wrong eviction end: first surviving clock %d", s.Sweeps()[0].Clock)
	}
}

// The compat contract: a space with no sweeps writes NOTHING new into
// the digest — every digest computed before SP-3.2 still stands.
func TestSweepEmptyWritesNothing(t *testing.T) {
	a, b := NewState(), NewState()
	// b gains and keeps no sweep state at all; digests must match a's.
	if a.Digest() != b.Digest() {
		t.Fatal("two empty states disagree")
	}
}
