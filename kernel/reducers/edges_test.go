// SP-2 reducer gates: edge LWW in every order, detach/re-attach, the
// candidate register (set/steal/clear), lineage chains incl. a cycle,
// annotation eviction determinism, the retention feed — and the extended
// shuffled world: any arrival order, same studio.
package reducers

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

const (
	mix11 = "1111111111111111111111111111111111111111111111111111111111111111"
	mix12 = "1212121212121212121212121212121212121212121212121212121212121212"
	take1 = "7777777777777777777777777777777777777777777777777777777777777777"
)

func edgeEvent(t *testing.T, clock uint64, seed byte, oid [16]byte, p objects.AttachPayload) ev {
	t.Helper()
	p.ObjectID = oid
	if p.Fallback == "" {
		p.Fallback = "edge"
	}
	payload, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: objects.SchemaAttached,
			LogicalClock: clock, CreatedAt: 1000 + clock, Payload: payload,
		},
		id: id.EventID{seed, byte(clock), 0xE0},
	}
}

func annEvent(t *testing.T, clock uint64, seed byte, asset, text string, posMs uint64, hasPos bool) ev {
	t.Helper()
	a := &schemas.AssetAnnotation{Text: text, Asset: asset}
	a.AnnotationID = [16]byte{seed, byte(clock), 0xAA}
	if hasPos {
		a.SetPosition(posMs)
	}
	payload, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: schemas.AssetAnnotated,
			LogicalClock: clock, CreatedAt: 1000 + clock, Payload: payload,
		},
		id: id.EventID{seed, byte(clock), 0xE1},
	}
}

func TestEdgeLWWAllOrders(t *testing.T) {
	oid := testOID(0x21)
	created := objRevisionEvent(t, 1, 1, oid, "Winter Song", "mixing", nil)
	attach := edgeEvent(t, 3, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Label: "mix-11"})
	relabel := edgeEvent(t, 6, 2, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Label: "mix-11 final?"})
	stale := edgeEvent(t, 4, 3, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Label: "stale"})
	all := []ev{created, attach, relabel, stale}

	var want [32]byte
	for trial := 0; trial < 12; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		edges := s.EdgesForObject(oid)
		if len(edges) != 1 || edges[0].Label != "mix-11 final?" || edges[0].EventID != relabel.id {
			t.Fatalf("edge LWW failed: %+v", edges)
		}
		if trial == 0 {
			want = s.Digest()
		} else if s.Digest() != want {
			t.Fatalf("digest diverged on permutation %d", trial)
		}
	}
}

func TestEdgeDetachReattachBothOrders(t *testing.T) {
	oid := testOID(0x22)
	created := objRevisionEvent(t, 1, 1, oid, "Winter Song", "mixing", nil)
	attach := edgeEvent(t, 3, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix"})
	detach := edgeEvent(t, 5, 2, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Detached: true})
	reattach := edgeEvent(t, 8, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Label: "back"})
	all := []ev{created, attach, detach, reattach}
	for trial := 0; trial < 12; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		edges := s.EdgesForObject(oid)
		if len(edges) != 1 || edges[0].Detached || edges[0].Label != "back" {
			t.Fatalf("re-attach lost: %+v", edges)
		}
	}
	// And a detach that IS the latest word stays detached — and stops
	// pinning the asset.
	s := NewState()
	for _, e := range []ev{created, attach, detach} {
		s.Apply(e.env, e.id)
	}
	if edges := s.EdgesForObject(oid); !edges[0].Detached {
		t.Fatal("detach did not hold")
	}
	if _, pinned := s.ObjectLiveAssetIDs()[mix11]; pinned {
		t.Fatal("detached edge still pins the asset")
	}
}

func TestCandidateRegisterSetStealClear(t *testing.T) {
	oid := testOID(0x23)
	created := objRevisionEvent(t, 1, 1, oid, "Winter Song", "mixing", nil)
	a11 := edgeEvent(t, 3, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Candidate: objects.CandidateSet})
	a12 := edgeEvent(t, 5, 2, oid, objects.AttachPayload{Asset: mix12, Role: "mix", Candidate: objects.CandidateSet})
	// A LABEL EDIT of mix-11 after the steal must NOT move the star back
	// (candidate key absent = don't touch).
	edit11 := edgeEvent(t, 7, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Label: "renamed"})
	all := []ev{created, a11, a12, edit11}
	for trial := 0; trial < 12; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		if got := s.CandidateAsset(oid); got != mix12 {
			t.Fatalf("candidate wrong after permutation: %q", got)
		}
	}
	// Clear beats an earlier set, in both orders.
	clear := edgeEvent(t, 9, 1, oid, objects.AttachPayload{Asset: mix12, Role: "mix", Candidate: objects.CandidateClear})
	for _, order := range [][]ev{{created, a12, clear}, {clear, a12, created}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		if got := s.CandidateAsset(oid); got != "" {
			t.Fatalf("clear lost: %q", got)
		}
	}
	// A detached candidate stops being the candidate (but the register
	// stands — re-attach restores it without a new star).
	s := NewState()
	for _, e := range []ev{created, a12} {
		s.Apply(e.env, e.id)
	}
	det := edgeEvent(t, 11, 2, oid, objects.AttachPayload{Asset: mix12, Role: "mix", Detached: true})
	s.Apply(det.env, det.id)
	if got := s.CandidateAsset(oid); got != "" {
		t.Fatalf("detached asset still candidate: %q", got)
	}
}

func TestEdgeBeforeObject(t *testing.T) {
	oid := testOID(0x24)
	attach := edgeEvent(t, 2, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix"})
	created := objRevisionEvent(t, 5, 2, oid, "Winter Song", "mixing", nil)
	s := NewState()
	s.Apply(attach.env, attach.id) // edge arrives before its object
	s.Apply(created.env, created.id)
	if edges := s.EdgesForObject(oid); len(edges) != 1 {
		t.Fatalf("early edge lost: %+v", edges)
	}
}

func TestVersionChainsAndCurrent(t *testing.T) {
	oid := testOID(0x25)
	created := objRevisionEvent(t, 1, 1, oid, "Winter Song", "mixing", nil)
	e11 := edgeEvent(t, 3, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix"})
	e12 := edgeEvent(t, 5, 1, oid, objects.AttachPayload{Asset: mix12, Role: "mix", Supersedes: mix11})
	s := NewState()
	for _, e := range []ev{created, e11, e12} {
		s.Apply(e.env, e.id)
	}
	chains := s.VersionChains(oid)
	if len(chains) != 1 || chains[0].Head != mix12 || len(chains[0].Chain) != 2 ||
		chains[0].Chain[1].Asset != mix11 {
		t.Fatalf("chain wrong: %+v", chains)
	}
	// No candidate → current = newest head.
	if got := s.CurrentAsset(oid); got != mix12 {
		t.Fatalf("current wrong: %q", got)
	}
	// Candidate overrides the head.
	star11 := edgeEvent(t, 7, 2, oid, objects.AttachPayload{Asset: mix11, Role: "mix", Candidate: objects.CandidateSet})
	s.Apply(star11.env, star11.id)
	if got := s.CurrentAsset(oid); got != mix11 {
		t.Fatalf("candidate did not override: %q", got)
	}
	// A cycle A↔B terminates and still yields chains (flat, no hang).
	oid2 := testOID(0x26)
	c2 := objRevisionEvent(t, 1, 3, oid2, "Loop", "x", nil)
	ea := edgeEvent(t, 3, 3, oid2, objects.AttachPayload{Asset: mix11, Role: "mix", Supersedes: mix12})
	eb := edgeEvent(t, 5, 3, oid2, objects.AttachPayload{Asset: mix12, Role: "mix", Supersedes: mix11})
	s2 := NewState()
	for _, e := range []ev{c2, ea, eb} {
		s2.Apply(e.env, e.id)
	}
	chains2 := s2.VersionChains(oid2)
	if len(chains2) != 0 {
		// both superseded → no heads; ALSO acceptable: chains exist but
		// terminate. Either way CurrentAsset must not hang and may be "".
		for _, c := range chains2 {
			if len(c.Chain) > 2 {
				t.Fatalf("cycle did not terminate: %+v", c)
			}
		}
	}
	_ = s2.CurrentAsset(oid2) // must return, not hang
}

func TestAnnotationTimelineAndEviction(t *testing.T) {
	s := NewState()
	// Point-in-time and whole-asset notes coexist.
	a1 := annEvent(t, 2, 1, mix11, "вокал суховат", 102_000, true)
	a2 := annEvent(t, 3, 2, mix11, "в целом отлично", 0, false)
	s.Apply(a1.env, a1.id)
	s.Apply(a2.env, a2.id)
	notes := s.AnnotationsForAsset(mix11)
	if len(notes) != 2 || !notes[0].HasPosition || notes[1].HasPosition {
		t.Fatalf("annotations wrong: %+v", notes)
	}
	// Replay is idempotent.
	s.Apply(a1.env, a1.id)
	if len(s.AnnotationsForAsset(mix11)) != 2 {
		t.Fatal("replay duplicated an annotation")
	}

	// Eviction determinism: 230 shuffled notes → same survivors, same
	// counter, same digest on every node.
	n := maxObservationsPerTimeline + 30
	all := make([]ev, 0, n)
	for i := 0; i < n; i++ {
		e := annEvent(t, uint64(10+i), byte(1+i%3), mix12, fmt.Sprintf("note %d", i), uint64(i)*500, true)
		e.id = id.EventID{byte(i), byte(i >> 8), 0xB1}
		all = append(all, e)
	}
	var want [32]byte
	var wantEvicted int
	for trial := 0; trial < 8; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		notes := s.AnnotationsForAsset(mix12)
		if len(notes) != maxObservationsPerTimeline {
			t.Fatalf("timeline size %d", len(notes))
		}
		if notes[0].Text != fmt.Sprintf("note %d", n-maxObservationsPerTimeline) {
			t.Fatalf("wrong eviction edge: %q", notes[0].Text)
		}
		if trial == 0 {
			want, wantEvicted = s.Digest(), s.AnnotationEvicted
			if wantEvicted == 0 {
				t.Fatal("no evictions counted")
			}
		} else if s.Digest() != want || s.AnnotationEvicted != wantEvicted {
			t.Fatalf("divergence on permutation %d", trial)
		}
	}
}

func TestParentEdgeDerivedBothOrders(t *testing.T) {
	release := testOID(0x27)
	track := testOID(0x28)
	relEv := objRevisionEvent(t, 1, 1, release, "Night Signals", "production", nil)
	trEvRec := &objects.Record{ObjectID: track, Kind: "track", Name: "Winter Song", Parent: &release}
	enc, err := trEvRec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	trEv := ev{
		env: &signal.Envelope{Principal: id.PrincipalID{2}, Schema: objects.SchemaCreated,
			LogicalClock: 3, CreatedAt: 1003,
			Payload: (&objects.RevisionPayload{Fallback: "Winter Song", Record: enc}).Encode()},
		id: id.EventID{2, 3, 0xC0},
	}
	for _, order := range [][]ev{{relEv, trEv}, {trEv, relEv}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		kids := s.ChildrenOf(release)
		if len(kids) != 1 || kids[0].Record.Name != "Winter Song" {
			t.Fatalf("children wrong: %+v", kids)
		}
		if len(s.ChildrenOf(track)) != 0 {
			t.Fatal("phantom grandchildren")
		}
	}
}

func TestObjectLiveAssetIDsFeed(t *testing.T) {
	oid := testOID(0x29)
	created := objRevisionEvent(t, 1, 1, oid, "Winter Song", "mixing", nil)
	e11 := edgeEvent(t, 3, 1, oid, objects.AttachPayload{Asset: mix11, Role: "mix"})
	e12 := edgeEvent(t, 5, 1, oid, objects.AttachPayload{Asset: mix12, Role: "mix", Supersedes: mix11})
	note := annEvent(t, 6, 2, take1, "annotation on unattached asset", 0, false)
	s := NewState()
	for _, e := range []ev{created, e11, e12, note} {
		s.Apply(e.env, e.id)
	}
	live := s.ObjectLiveAssetIDs()
	// Superseded-but-attached mix-11 STAYS pinned (it must remain
	// playable); the annotated-but-unattached asset does NOT pin.
	if _, ok := live[mix11]; !ok {
		t.Fatal("superseded mix lost its pin")
	}
	if _, ok := live[mix12]; !ok {
		t.Fatal("current mix not pinned")
	}
	if _, ok := live[take1]; ok {
		t.Fatal("an annotation pinned an asset — commentary must never do that")
	}
}

// TestShuffledStudioConvergence extends the SP-1 shuffled world with the
// SP-2 vocabulary: release→track parent, two mixes with lineage, a steal
// of the star, a detach, annotations. Any arrival order, same studio.
func TestShuffledStudioConvergence(t *testing.T) {
	release := testOID(0x2A)
	track := testOID(0x2B)
	relEv := objRevisionEvent(t, 1, 1, release, "Night Signals", "production", nil)
	trRec := &objects.Record{ObjectID: track, Kind: "track", Name: "Winter Song", Parent: &release}
	enc, _ := trRec.Encode()
	trEv := ev{
		env: &signal.Envelope{Principal: id.PrincipalID{2}, Schema: objects.SchemaCreated,
			LogicalClock: 2, CreatedAt: 1002,
			Payload: (&objects.RevisionPayload{Fallback: "Winter Song", Record: enc}).Encode()},
		id: id.EventID{2, 2, 0xC1},
	}
	e11 := edgeEvent(t, 3, 1, track, objects.AttachPayload{Asset: mix11, Role: "mix", Candidate: objects.CandidateSet})
	e12 := edgeEvent(t, 5, 2, track, objects.AttachPayload{Asset: mix12, Role: "mix", Supersedes: mix11, Candidate: objects.CandidateSet})
	tk := edgeEvent(t, 6, 3, track, objects.AttachPayload{Asset: take1, Role: "take", Label: "take 04"})
	det := edgeEvent(t, 8, 1, track, objects.AttachPayload{Asset: take1, Role: "take", Detached: true})
	n1 := annEvent(t, 4, 3, mix11, "вокал суховат", 102_000, true)
	n2 := annEvent(t, 7, 1, mix12, "переход оставить", 77_000, true)
	world := []ev{relEv, trEv, e11, e12, tk, det, n1, n2}

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
		if got := s.CurrentAsset(track); got != mix12 {
			t.Fatalf("node %d current wrong: %q", i, got)
		}
		chains := s.VersionChains(track)
		if len(chains) != 1 || chains[0].Head != mix12 || len(chains[0].Chain) != 2 {
			t.Fatalf("node %d chain wrong: %+v", i, chains)
		}
		if kids := s.ChildrenOf(release); len(kids) != 1 || kids[0].ObjectID != track {
			t.Fatalf("node %d children wrong", i)
		}
		// The note stayed on mix-11, not the successor.
		if n := s.AnnotationsForAsset(mix11); len(n) != 1 || n[0].Text != "вокал суховат" {
			t.Fatalf("node %d annotation moved: %+v", i, n)
		}
		var detached bool
		for _, e := range s.EdgesForObject(track) {
			if e.Asset == take1 {
				detached = e.Detached
			}
		}
		if !detached {
			t.Fatalf("node %d take not detached", i)
		}
		live := s.ObjectLiveAssetIDs()
		if _, ok := live[take1]; ok {
			t.Fatalf("node %d detached take still pinned", i)
		}
	}
}
