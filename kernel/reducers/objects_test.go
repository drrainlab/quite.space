// SP-1 reducer gates: revision races, archive/restore law, dual-projection
// observations, deterministic timeline eviction, the fourth oracle branch —
// and the shuffled-world convergence test, the owner's one executable
// statement of the whole architecture: any arrival order, same world.
package reducers

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/keep"
	"github.com/drrainlab/quiet_places/protocol/objects"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func testOID(seed byte) [16]byte {
	var oid [16]byte
	for i := range oid {
		oid[i] = seed
	}
	return oid
}

func objRevisionEvent(t *testing.T, clock uint64, seed byte, oid [16]byte, name, status string, prev *id.EventID) ev {
	t.Helper()
	r := &objects.Record{ObjectID: oid, Kind: "machine", Name: name, Status: status}
	rec, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	schema := objects.SchemaCreated
	if prev != nil {
		schema = objects.SchemaRevised
	}
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: schema,
			LogicalClock: clock, CreatedAt: 1000 + clock,
			Payload: (&objects.RevisionPayload{Fallback: name, Record: rec, PrevRevision: prev}).Encode(),
		},
		id: id.EventID{seed, byte(clock), 0xA0},
	}
}

func objLifecycleEvent(t *testing.T, clock uint64, seed byte, oid [16]byte, schema string, archived *id.EventID) ev {
	t.Helper()
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: schema,
			LogicalClock: clock, CreatedAt: 1000 + clock,
			Payload: (&objects.LifecyclePayload{Fallback: "x", ObjectID: oid, ArchivedRevision: archived}).Encode(),
		},
		id: id.EventID{seed, byte(clock), 0xA1},
	}
}

func obsEvent(t *testing.T, clock uint64, seed byte, text string, oid *[16]byte) ev {
	t.Helper()
	payload, err := (&schemas.NotedObservation{Text: text, ObjectID: oid}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: schemas.ObservationNoted,
			LogicalClock: clock, CreatedAt: 1000 + clock,
			ProducedBy: signal.AuthorshipHuman, Payload: payload,
		},
		id: id.EventID{seed, byte(clock), 0xA2},
	}
}

func cardEvent(t *testing.T, clock uint64, seed byte, title, status string, oid *[16]byte, created *id.EventID) ev {
	t.Helper()
	schema := schemas.CardCreated
	if created != nil {
		schema = schemas.CardUpdated
	}
	payload, err := (&schemas.Card{Title: title, Status: status, ObjectID: oid, Card: created}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: schema,
			LogicalClock: clock, CreatedAt: 1000 + clock, Payload: payload,
		},
		id: id.EventID{seed, byte(clock), 0xA3},
	}
}

func keepEvent(t *testing.T, clock uint64, seed byte, target id.EventID) ev {
	t.Helper()
	payload, err := (&keep.Kept{Target: target}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: keep.SchemaKept,
			LogicalClock: clock, CreatedAt: 1000 + clock, Payload: payload,
		},
		id: id.EventID{seed, byte(clock), 0xA4},
	}
}

func TestObjectRevisionLWWBothOrders(t *testing.T) {
	oid := testOID(1)
	created := objRevisionEvent(t, 1, 1, oid, "CNC-01", "operational", nil)
	rev := objRevisionEvent(t, 5, 2, oid, "CNC-01", "in_repair", &created.id)
	stale := objRevisionEvent(t, 3, 3, oid, "CNC-01", "stale_name", &created.id)

	for _, order := range [][]ev{{created, rev, stale}, {stale, rev, created}, {rev, stale, created}} {
		s := NewState()
		for _, e := range order {
			s.Apply(e.env, e.id)
		}
		objs := s.Objects()
		if len(objs) != 1 {
			t.Fatalf("expected 1 object, got %d", len(objs))
		}
		o := objs[0]
		if o.Record.Status != "in_repair" || o.RevisionEventID != rev.id {
			t.Fatalf("LWW failed: status=%q rev=%x", o.Record.Status, o.RevisionEventID[:4])
		}
		// CreatedAt is frozen at the FIRST accepted revision's wall clock.
		if o.CreatedAt == 0 {
			t.Fatal("created_at not set")
		}
	}
}

func TestObjectArchiveRestoreLaw(t *testing.T) {
	oid := testOID(2)
	created := objRevisionEvent(t, 1, 1, oid, "Lathe", "ok", nil)
	archive := objLifecycleEvent(t, 5, 1, oid, objects.SchemaArchived, nil)

	// A LATER revision does not lift the archive.
	lateRev := objRevisionEvent(t, 7, 2, oid, "Lathe", "renamed", &created.id)
	s := NewState()
	for _, e := range []ev{created, archive, lateRev} {
		s.Apply(e.env, e.id)
	}
	if len(s.Objects()) != 0 || len(s.ArchivedObjects()) != 1 {
		t.Fatal("revision lifted an archive")
	}
	// But the revision content still folded (visible on restore).
	if s.ArchivedObjects()[0].Record.Status != "renamed" {
		t.Fatal("revision content lost under archive")
	}

	// Restore referencing the WRONG archive event does nothing.
	wrong := id.EventID{0xEE}
	badRestore := objLifecycleEvent(t, 9, 1, oid, objects.SchemaRestored, &wrong)
	s.Apply(badRestore.env, badRestore.id)
	if len(s.Objects()) != 0 {
		t.Fatal("restore with wrong archive reference accepted")
	}

	// Restore referencing THE archive event, later than it: lifts.
	restore := objLifecycleEvent(t, 10, 1, oid, objects.SchemaRestored, &archive.id)
	s.Apply(restore.env, restore.id)
	if len(s.Objects()) != 1 || len(s.ArchivedObjects()) != 0 {
		t.Fatal("legitimate restore refused")
	}

	// Order independence of the whole lifecycle.
	all := []ev{created, archive, lateRev, badRestore, restore}
	want := s.Digest()
	for trial := 0; trial < 10; trial++ {
		s2 := NewState()
		for _, i := range rand.Perm(len(all)) {
			s2.Apply(all[i].env, all[i].id)
		}
		if s2.Digest() != want {
			t.Fatalf("lifecycle diverged on permutation %d", trial)
		}
	}
}

func TestObservationDualProjection(t *testing.T) {
	oid := testOID(3)
	created := objRevisionEvent(t, 1, 1, oid, "CNC-01", "ok", nil)
	note := obsEvent(t, 3, 2, "заметил люфт шпинделя", &oid)
	s := NewState()
	s.Apply(created.env, created.id)
	s.Apply(note.env, note.id)

	// Projection one: a quiet feed entry.
	var found bool
	for _, e := range s.Entries() {
		if e.ID == note.id {
			found = true
			if e.Kind != KindObservation || e.Content.Observation.Text != "заметил люфт шпинделя" {
				t.Fatalf("feed entry wrong: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("observation missing from the feed")
	}
	// Projection two: the object's timeline — same event id, no duplicate.
	o, ok := s.ObjectByID(oid)
	if !ok || len(o.Observations) != 1 || o.Observations[0].EventID != note.id {
		t.Fatalf("timeline wrong: %+v", o.Observations)
	}
	// Replay is idempotent on the timeline.
	s.Apply(note.env, note.id)
	if o, _ := s.ObjectByID(oid); len(o.Observations) != 1 {
		t.Fatal("replay duplicated a timeline note")
	}
}

func TestObservationBeforeObjectAndJournal(t *testing.T) {
	oid := testOID(4)
	note := obsEvent(t, 2, 1, "тля на грядке 3", &oid)
	journal := obsEvent(t, 3, 1, "в мастерской пахнет канифолью", nil)
	created := objRevisionEvent(t, 5, 2, oid, "BED 03", "ok", nil)

	s := NewState()
	s.Apply(note.env, note.id) // note arrives before its object
	s.Apply(journal.env, journal.id)
	s.Apply(created.env, created.id)

	o, ok := s.ObjectByID(oid)
	if !ok || len(o.Observations) != 1 {
		t.Fatal("early note lost")
	}
	j := s.JournalObservations()
	if len(j) != 1 || j[0].Text != "в мастерской пахнет канифолью" {
		t.Fatalf("journal wrong: %+v", j)
	}
}

func TestTaskBeforeObjectAndDerivedEdge(t *testing.T) {
	oid := testOID(5)
	task := cardEvent(t, 2, 1, "Проверить люфт", "open", &oid, nil)
	created := objRevisionEvent(t, 4, 2, oid, "CNC-01", "ok", nil)
	s := NewState()
	s.Apply(task.env, task.id)
	s.Apply(created.env, created.id)
	tasks := s.TasksForObject(oid)
	if len(tasks) != 1 || tasks[0].Title != "Проверить люфт" {
		t.Fatalf("derived edge wrong: %+v", tasks)
	}
	// Update-before-create with object: the tolerance keeps the object id.
	cid := id.EventID{0x77}
	upd := cardEvent(t, 6, 1, "Проверить люфт", "done", &oid, &cid)
	s2 := NewState()
	s2.Apply(upd.env, upd.id)
	s2.Apply(created.env, created.id)
	if tasks := s2.TasksForObject(oid); len(tasks) != 1 || tasks[0].Status != "done" {
		t.Fatalf("update-before-create lost the edge: %+v", tasks)
	}
}

func TestObservationEvictionDeterminism(t *testing.T) {
	oid := testOID(6)
	created := objRevisionEvent(t, 1, 1, oid, "CNC-01", "ok", nil)
	all := []ev{created}
	n := maxObservationsPerTimeline + 30
	for i := 0; i < n; i++ {
		e := obsEvent(t, uint64(10+i), byte(2+i%3), fmt.Sprintf("note %d", i), &oid)
		// distinct event ids beyond the byte-sized clock
		e.id = id.EventID{byte(i), byte(i >> 8), 0xB0}
		all = append(all, e)
	}
	var want [32]byte
	var wantEvicted int
	for trial := 0; trial < 8; trial++ {
		s := NewState()
		for _, i := range rand.Perm(len(all)) {
			s.Apply(all[i].env, all[i].id)
		}
		o, ok := s.ObjectByID(oid)
		if !ok || len(o.Observations) != maxObservationsPerTimeline {
			t.Fatalf("timeline size %d", len(o.Observations))
		}
		// Survivors are exactly the NEWEST notes in the total order.
		if o.Observations[0].Text != fmt.Sprintf("note %d", n-maxObservationsPerTimeline) {
			t.Fatalf("wrong eviction edge: %q", o.Observations[0].Text)
		}
		if trial == 0 {
			want = s.Digest()
			wantEvicted = s.ObservationEvicted
			if wantEvicted == 0 {
				t.Fatal("no evictions counted")
			}
			if s.Unsupported["observation:evicted"] != 0 {
				t.Fatal("evictions leaked into Unsupported")
			}
		} else {
			if s.Digest() != want {
				t.Fatalf("digest diverged on permutation %d", trial)
			}
			// The counter is arrival-order-INDEPENDENT too: every node
			// reports the same honesty number.
			if s.ObservationEvicted != wantEvicted {
				t.Fatalf("eviction counter diverged: %d vs %d", s.ObservationEvicted, wantEvicted)
			}
		}
	}
}

func TestObjectKeepAndReactTarget(t *testing.T) {
	oid := testOID(7)
	target := objects.Target(oid)
	created := objRevisionEvent(t, 3, 1, oid, "CNC-01", "ok", nil)
	kept := keepEvent(t, 5, 2, target)

	// Keep BEFORE the object exists: pending, then resolves.
	s := NewState()
	s.Apply(kept.env, kept.id)
	s.Apply(created.env, created.id)
	kind, resolved, keepable := s.KeepTargetStatus(target)
	if kind != "object" || !resolved || !keepable {
		t.Fatalf("oracle: %q %v %v", kind, resolved, keepable)
	}
	if !s.ResonanceTargetStatus(target) {
		t.Fatal("object not reactable")
	}
	if got, ok := s.ObjectByTarget(target); !ok || got != oid {
		t.Fatal("target does not resolve to object")
	}
	found := false
	for _, item := range s.Shelf() {
		if item.Target == target && item.Kind == "object" {
			found = true
		}
	}
	if !found {
		t.Fatal("kept object missing from the shelf")
	}
}

// TestShuffledWorldConvergence is the owner's mandated end-to-end statement:
// three nodes independently receive Task(object=X), Observation(object=X),
// Keep(target=X), ObjectCreated(X), ObjectRevised(X), ObjectArchived(X),
// ObjectRestored(X) in DIFFERENT orders; after full exchange the digests
// and every projection agree.
func TestShuffledWorldConvergence(t *testing.T) {
	oid := testOID(8)
	created := objRevisionEvent(t, 1, 1, oid, "CNC-01", "operational", nil)
	revised := objRevisionEvent(t, 4, 1, oid, "CNC-01", "in_repair", &created.id)
	archived := objLifecycleEvent(t, 6, 1, oid, objects.SchemaArchived, nil)
	restored := objLifecycleEvent(t, 8, 1, oid, objects.SchemaRestored, &archived.id)
	task := cardEvent(t, 3, 2, "Проверить люфт шпинделя", "open", &oid, nil)
	note := obsEvent(t, 5, 3, "заметил люфт шпинделя", &oid)
	kept := keepEvent(t, 7, 2, objects.Target(oid))
	world := []ev{created, revised, archived, restored, task, note, kept}

	states := make([]*State, 3)
	for n := range states {
		states[n] = NewState()
		for _, i := range rand.Perm(len(world)) {
			states[n].Apply(world[i].env, world[i].id)
		}
	}
	d0 := states[0].Digest()
	for n, s := range states {
		if s.Digest() != d0 {
			t.Fatalf("node %d digest diverged", n)
		}
		o, ok := s.ObjectByID(oid)
		if !ok || o.Archived || o.Record.Status != "in_repair" {
			t.Fatalf("node %d object wrong: %+v", n, o)
		}
		if len(o.Observations) != 1 || o.Observations[0].EventID != note.id {
			t.Fatalf("node %d timeline wrong", n)
		}
		tasks := s.TasksForObject(oid)
		if len(tasks) != 1 || tasks[0].Status != "open" {
			t.Fatalf("node %d task edge wrong", n)
		}
		if kind, resolved, keepable := s.KeepTargetStatus(objects.Target(oid)); kind != "object" || !resolved || !keepable {
			t.Fatalf("node %d keep oracle wrong", n)
		}
		if len(s.Objects()) != 1 || len(s.ArchivedObjects()) != 0 {
			t.Fatalf("node %d lifecycle wrong", n)
		}
	}
}
