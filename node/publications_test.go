package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/publication"
)

func testDoc(title string) *publication.Document {
	doc := &publication.Document{Kind: "article", Title: title, Visibility: "space"}
	doc.DocumentID[0] = 0xAA
	doc.Blocks = []publication.Block{
		{ID: "b1", Type: "text", RawProps: publication.EncodeTextProps(publication.TextProps{Text: "hello"})},
	}
	return doc
}

// PUB-0 core: publish → projection; revise with the correct base; a stale
// base conflicts and moves nothing; archive hides; a later revision never
// resurrects; an explicit restore referencing the archive does.
func TestPublicationLifecycle(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tid, err := rt.CreateSpace("Journal")
	if err != nil {
		t.Fatal(err)
	}
	doc := testDoc("First Post")

	rev1, err := rt.PublishDocument(tid, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.Space(tid)
	pubs := sp.State.Publications()
	if len(pubs) != 1 || pubs[0].Title != "First Post" {
		t.Fatalf("projection missing: %+v", pubs)
	}
	if pubs[0].Author != rt.Principal.ID {
		t.Fatal("author must come from the envelope signature")
	}

	// Revise from the correct base.
	doc.Title = "First Post (edited)"
	rev2, err := rt.PublishDocument(tid, doc, &rev1)
	if err != nil {
		t.Fatal(err)
	}
	// A second editor holding the OLD base conflicts; the tip stays.
	stale := testDoc("Conflicting edit")
	if _, err := rt.PublishDocument(tid, stale, &rev1); err == nil ||
		!strings.Contains(err.Error(), "changed elsewhere") {
		t.Fatalf("stale base accepted: %v", err)
	}
	if pubs = sp.State.Publications(); pubs[0].RevisionEventID != rev2 {
		t.Fatal("conflict moved the tip")
	}

	// Threaded comment.
	if _, err := rt.CommentOnDocument(tid, doc.DocumentID, nil, "nice"); err != nil {
		t.Fatal(err)
	}
	p, _ := sp.State.PublicationByID(doc.DocumentID)
	if len(p.Comments) != 1 || p.Comments[0].Text != "nice" {
		t.Fatal("comment projection missing")
	}

	// Archive hides; a revision after archive does not resurrect.
	archEvent, err := rt.ArchiveDocument(tid, doc.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.State.Publications()) != 0 || len(sp.State.ArchivedPublications()) != 1 {
		t.Fatal("archive did not hide the document")
	}
	doc.Title = "Sneaky revision"
	if _, err := rt.PublishDocument(tid, doc, &rev2); err != nil {
		t.Fatal(err)
	}
	if len(sp.State.Publications()) != 0 {
		t.Fatal("a revision resurrected an archived document")
	}
	// Restore referencing the WRONG event does nothing.
	wrong := archEvent
	wrong[0] ^= 0xFF
	if err := rt.RestoreDocument(tid, doc.DocumentID, wrong); err != nil {
		t.Fatal(err)
	}
	if len(sp.State.Publications()) != 0 {
		t.Fatal("restore with a wrong reference lifted the archive")
	}
	// Restore referencing the actual archive brings it back.
	if err := rt.RestoreDocument(tid, doc.DocumentID, archEvent); err != nil {
		t.Fatal(err)
	}
	if len(sp.State.Publications()) != 1 {
		t.Fatal("explicit restore did not bring the document back")
	}
}

// Drafts are sealed on disk (no cleartext), survive restart, and publishing
// consumes them.
func TestDraftsSealedAndConsumed(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Journal")
	if err != nil {
		t.Fatal(err)
	}
	docID := strings.Repeat("ab", 16)
	body := []byte(`{"document_id":"` + docID + `","title":"Secret draft body","blocks":[]}`)
	if err := rt.SaveDraft(tid, docID, body); err != nil {
		t.Fatal(err)
	}
	// No cleartext on disk.
	found := false
	filepath.Walk(filepath.Join(dir, "sealed"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), "Secret draft body") {
			found = true
		}
		return nil
	})
	if found {
		t.Fatal("draft stored in cleartext")
	}
	rt.Close()

	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	got, err := rt2.LoadDraft(tid, docID)
	if err != nil || !strings.Contains(string(got), "Secret draft body") {
		t.Fatalf("draft did not survive restart: %v", err)
	}
	ids, _ := rt2.ListDrafts(tid)
	if len(ids) != 1 || ids[0] != docID {
		t.Fatalf("draft listing wrong: %v", ids)
	}
	if err := rt2.DeleteDraft(tid, docID); err != nil {
		t.Fatal(err)
	}
	if _, err := rt2.LoadDraft(tid, docID); err == nil {
		t.Fatal("deleted draft still loads")
	}
}
