// ADR-014 lifecycle law for publications, mirroring the SP-1 objects law
// (TestObjectArchiveRestoreLaw): archive is a tombstone only an explicit,
// later restore referencing that archive can lift — under ANY arrival
// order. The shuffled trials are the regression for the flag-form hole:
// a restore folded before its archive used to be dropped permanently.
package reducers

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

func pubRevisionEvent(t *testing.T, clock uint64, seed byte, docID [16]byte, title string, prev *id.EventID) ev {
	t.Helper()
	doc := &publication.Document{DocumentID: docID, Kind: "post", Title: title}
	schema := publication.SchemaPublished
	if prev != nil {
		schema = publication.SchemaRevised
	}
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: schema,
			LogicalClock: clock, CreatedAt: 1000 + clock,
			Payload: (&publication.RevisionPayload{Fallback: title, Document: doc.Encode(), PrevRevision: prev}).Encode(),
		},
		id: id.EventID{seed, byte(clock), 0xB0},
	}
}

func pubLifecycleEvent(t *testing.T, clock uint64, seed byte, docID [16]byte, schema string, archived *id.EventID) ev {
	t.Helper()
	return ev{
		env: &signal.Envelope{
			Principal: id.PrincipalID{seed}, Schema: schema,
			LogicalClock: clock, CreatedAt: 1000 + clock,
			Payload: (&publication.LifecyclePayload{Fallback: "x", DocumentID: docID, ArchivedRevision: archived}).Encode(),
		},
		id: id.EventID{seed, byte(clock), 0xB1},
	}
}

// pubFingerprint is the convergence oracle: publications stay out of
// State.Digest (the keep.go precedent), so the law test hashes the
// projection itself — every document, live and archived.
func pubFingerprint(s *State) string {
	out := ""
	for _, p := range s.Publications() {
		out += fmt.Sprintf("live %x %q rev=%x;", p.DocumentID, p.Title, p.RevisionEventID[:4])
	}
	for _, p := range s.ArchivedPublications() {
		out += fmt.Sprintf("dead %x %q rev=%x;", p.DocumentID, p.Title, p.RevisionEventID[:4])
	}
	return out
}

func TestPublicationArchiveRestoreLaw(t *testing.T) {
	docID := testOID(0x21)
	created := pubRevisionEvent(t, 1, 1, docID, "Тихое место", nil)
	archive := pubLifecycleEvent(t, 5, 1, docID, publication.SchemaArchived, nil)

	// A LATER revision does not lift the archive.
	lateRev := pubRevisionEvent(t, 7, 2, docID, "Тихое место (ред.)", &created.id)
	s := NewState()
	for _, e := range []ev{created, archive, lateRev} {
		s.Apply(e.env, e.id)
	}
	if len(s.Publications()) != 0 || len(s.ArchivedPublications()) != 1 {
		t.Fatal("revision lifted an archive")
	}
	// But the revision content still folded (visible on restore).
	if s.ArchivedPublications()[0].Title != "Тихое место (ред.)" {
		t.Fatal("revision content lost under archive")
	}

	// Restore referencing the WRONG archive event does nothing.
	wrong := id.EventID{0xEE}
	badRestore := pubLifecycleEvent(t, 9, 1, docID, publication.SchemaRestored, &wrong)
	s.Apply(badRestore.env, badRestore.id)
	if len(s.Publications()) != 0 {
		t.Fatal("restore with wrong archive reference accepted")
	}

	// Restore referencing THE archive event, later than it: lifts.
	restore := pubLifecycleEvent(t, 10, 1, docID, publication.SchemaRestored, &archive.id)
	s.Apply(restore.env, restore.id)
	if len(s.Publications()) != 1 || len(s.ArchivedPublications()) != 0 {
		t.Fatal("legitimate restore refused")
	}
	want := pubFingerprint(s)

	// The exact hole the two-register form closes: restore arrives BEFORE
	// its archive and must still count once the archive lands.
	s3 := NewState()
	for _, e := range []ev{created, restore, archive, lateRev, badRestore} {
		s3.Apply(e.env, e.id)
	}
	if got := pubFingerprint(s3); got != want {
		t.Fatalf("restore-before-archive dropped:\n got %s\nwant %s", got, want)
	}

	// Order independence of the whole lifecycle.
	all := []ev{created, archive, lateRev, badRestore, restore}
	for trial := range 10 {
		s2 := NewState()
		for _, i := range rand.Perm(len(all)) {
			s2.Apply(all[i].env, all[i].id)
		}
		if got := pubFingerprint(s2); got != want {
			t.Fatalf("lifecycle diverged on permutation %d:\n got %s\nwant %s", trial, got, want)
		}
	}
}
