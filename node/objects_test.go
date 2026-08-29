// SP-1 node gates: optimistic concurrency (409-class conflict), lifecycle,
// observations landing in both projections, preserve-on-update for cards,
// and the refusal regression — card paths historically skipped canWrite,
// so a frozen space must now refuse EVERY SP-1 write path.
package node

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/schemas"

	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/objects"
)

func testRecord(name string) *objects.Record {
	return &objects.Record{
		Kind: "machine", Name: name, Status: "operational",
		Props: []objects.Prop{{Key: "location", Value: "workshop corner"}},
	}
}

func TestObjectLifecycleAtNode(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}

	// Create mints the id.
	oid, rev1, err := rt.CreateObject(tid, testRecord("CNC-01"))
	if err != nil {
		t.Fatal(err)
	}
	if oid == ([16]byte{}) {
		t.Fatal("no object id minted")
	}
	sp, _ := rt.spaceForTest(tid)
	objs := sp.State.Objects()
	if len(objs) != 1 || objs[0].Record.Name != "CNC-01" {
		t.Fatalf("projection missing: %+v", objs)
	}
	if objs[0].Author != rt.PrincipalID {
		t.Fatal("author must come from the envelope signature")
	}
	// Creating the same id again is a refusal, not a second birth.
	dup := testRecord("CNC-01 again")
	dup.ObjectID = oid
	if _, _, err := rt.CreateObject(tid, dup); err == nil {
		t.Fatal("duplicate create accepted")
	}

	// Revise from the correct base; props are the FULL new set.
	next := testRecord("CNC-01")
	next.ObjectID = oid
	next.Status = "in_repair"
	next.Props = nil // full replace: the location prop is now DELETED
	rev2, err := rt.ReviseObject(tid, next, &rev1)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := sp.State.ObjectByID(oid)
	if o.Record.Status != "in_repair" || len(o.Record.Props) != 0 {
		t.Fatalf("full-replace contract broken: %+v", o.Record)
	}
	// A second editor holding the OLD base conflicts; the tip stays.
	stale := testRecord("CNC-01")
	stale.ObjectID = oid
	if _, err := rt.ReviseObject(tid, stale, &rev1); !errors.Is(err, ErrObjectConflict) {
		t.Fatalf("stale base accepted: %v", err)
	}
	if o, _ := sp.State.ObjectByID(oid); o.RevisionEventID != rev2 {
		t.Fatal("conflict moved the tip")
	}

	// Observation: one event, both projections.
	if _, err := rt.NoteObservation(tid, "заметил люфт шпинделя", &oid, 0, nil); err != nil {
		t.Fatal(err)
	}
	o, _ = sp.State.ObjectByID(oid)
	if len(o.Observations) != 1 || o.Observations[0].Text != "заметил люфт шпинделя" {
		t.Fatalf("timeline missing: %+v", o.Observations)
	}
	var inFeed bool
	for _, e := range sp.State.Entries() {
		if e.Kind == reducers.KindObservation && e.Content.Observation.Text == "заметил люфт шпинделя" {
			inFeed = true
		}
	}
	if !inFeed {
		t.Fatal("observation missing from the feed")
	}

	// Archive hides; restore by explicit reference lifts.
	archEvent, err := rt.ArchiveObject(tid, oid)
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.State.Objects()) != 0 || len(sp.State.ArchivedObjects()) != 1 {
		t.Fatal("archive did not hide the object")
	}
	if err := rt.RestoreObject(tid, oid, archEvent); err != nil {
		t.Fatal(err)
	}
	if len(sp.State.Objects()) != 1 {
		t.Fatal("restore did not bring the object back")
	}
}

func TestCardPreserveOnUpdate(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}
	oid, _, err := rt.CreateObject(tid, testRecord("CNC-01"))
	if err != nil {
		t.Fatal(err)
	}
	self := rt.PrincipalID
	cid, err := rt.MakeCard(tid, "Проверить люфт", CardOptions{ObjectID: &oid, Assignee: &self})
	if err != nil {
		t.Fatal(err)
	}
	// A bare status toggle — title omitted too — must preserve everything.
	if err := rt.SetCardStatus(tid, cid, "", "done"); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	tasks := sp.State.TasksForObject(oid)
	if len(tasks) != 1 {
		t.Fatalf("card lost its object on update: %+v", sp.State.Cards())
	}
	c := tasks[0]
	if c.Status != "done" || c.Title != "Проверить люфт" || c.Assignee == nil || *c.Assignee != self {
		t.Fatalf("update stripped fields: %+v", c)
	}
}

// The refusal regression: freezing a space must refuse every SP-1 write —
// including the card paths, which historically skipped canWrite.
func TestFrozenSpaceRefusesObjectAndCardWrites(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Workshop")
	if err != nil {
		t.Fatal(err)
	}
	oid, rev1, err := rt.CreateObject(tid, testRecord("CNC-01"))
	if err != nil {
		t.Fatal(err)
	}
	cid, err := rt.MakeCard(tid, "task", CardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// ReadOnly is canWrite's first refusal branch (a private space carries
	// no freeze policy; the frozen/curated branches are covered by the
	// public-space suites). Flip it directly: this gate is about every
	// SP-1 write path CALLING canWrite, not about policy plumbing.
	sp, _ := rt.spaceForTest(tid)
	sp.ReadOnly = true
	frozen := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "join this space") {
			t.Fatalf("%s not refused on a read-only space: %v", name, err)
		}
	}
	_, _, err = rt.CreateObject(tid, testRecord("Lathe"))
	frozen("CreateObject", err)
	rec := testRecord("CNC-01")
	rec.ObjectID = oid
	_, err = rt.ReviseObject(tid, rec, &rev1)
	frozen("ReviseObject", err)
	_, err = rt.ArchiveObject(tid, oid)
	frozen("ArchiveObject", err)
	frozen("RestoreObject", rt.RestoreObject(tid, oid, rev1))
	_, err = rt.NoteObservation(tid, "note", &oid, 0, nil)
	frozen("NoteObservation", err)
	_, err = rt.MakeCard(tid, "task2", CardOptions{})
	frozen("MakeCard", err)
	frozen("SetCardStatus", rt.SetCardStatus(tid, cid, "", "done"))
}

// SP-3.2 follow-up: an observation may point at ONE piece of evidence.
// The asset must already be indexed in the space (carrier first, note
// second — the sweep finalize's own order), a note pointing at bytes the
// space never heard of is refused, and the reference survives into both
// projections.
func TestObservationCarriesOnePieceOfEvidence(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Field")
	if err != nil {
		t.Fatal(err)
	}
	oid, _, err := rt.CreateObject(tid, testRecord("Sector B3"))
	if err != nil {
		t.Fatal(err)
	}

	// A reference into the void is refused: nothing indexed yet.
	var phantom [32]byte
	phantom[0] = 0xEE
	if _, err := rt.NoteObservation(tid, "фото без байтов", &oid, 0, &phantom); err == nil {
		t.Fatal("an un-indexed asset reference was accepted")
	}

	// Upload the evidence the honest way: bytes in, carrier emitted.
	photo := []byte("not-really-a-jpeg-but-bytes-enough")
	ref, err := rt.IngestAsset(bytes.NewReader(photo), int64(len(photo)),
		assets.Metadata{MediaType: "image/jpeg", Role: "original"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := (&schemas.AttachedBlock{
		Filename: "boot.jpg", MediaType: "image/jpeg", Original: ref,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.EmitBlock(tid, schemas.BlockAttached, payload); err != nil {
		t.Fatal(err)
	}

	var asset [32]byte
	ab, _ := hex.DecodeString(ref.PublicIDHex())
	copy(asset[:], ab)
	if _, err := rt.NoteObservation(tid, "нашёл ботинок, вот фото", &oid, 0, &asset); err != nil {
		t.Fatalf("an indexed reference was refused: %v", err)
	}

	sp, _ := rt.spaceForTest(tid)
	o, _ := sp.State.ObjectByID(oid)
	found := false
	for _, n := range o.Observations {
		if n.Text == "нашёл ботинок, вот фото" {
			found = true
			if n.Asset == nil || *n.Asset != asset {
				t.Fatalf("the timeline lost the evidence: %+v", n)
			}
		}
	}
	if !found {
		t.Fatal("the observation never reached the timeline")
	}
	for _, e := range sp.State.Entries() {
		if e.Kind == reducers.KindObservation && e.Content.Observation.Text == "нашёл ботинок, вот фото" {
			if e.Content.Observation.Asset == nil || *e.Content.Observation.Asset != asset {
				t.Fatalf("the feed projection lost the evidence: %+v", e.Content.Observation)
			}
			return
		}
	}
	t.Fatal("the observation never reached the feed")
}
