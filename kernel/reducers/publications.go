// Publications projection (ADR-014, PUB-0): folds publication.* events into
// per-document state. The author is the envelope's principal (the signer),
// never the document's display metadata. Convergence is deterministic LWW by
// (LogicalClock, EventID); archive is a tombstone that only an explicit,
// later restore referencing that archive can lift — a stale revision never
// resurrects an archived document.
package reducers

import (
	"crypto/sha256"
	"sort"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

type pubRec struct {
	docRaw   []byte
	title    string
	author   id.PrincipalID
	revID    id.EventID
	revClock uint64
	prevRev  *id.EventID
	// createdAt is the wall clock of the FIRST accepted revision; later
	// revisions never move it (PS-2).
	createdAt uint64

	// Archive and restore are two LWW REGISTERS and "archived" is a pure
	// function of both — the SP-1 objects form (objRec), ported back after
	// its convergence test exposed the flag form's ordering hole here: a
	// restore folded BEFORE its archive was dropped ("not archived yet")
	// and never reconsidered, so a node that saw restore-first stayed
	// archived forever while a node that saw archive-first went live.
	hasArchive   bool
	archiveEvent id.EventID
	archiveClock uint64
	restoreRef   *id.EventID // which archive event the restore undoes
	restoreEID   id.EventID
	restoreClock uint64

	comments map[[16]byte]*PublicationComment
}

// archived derives the lifecycle state: archived unless the latest restore
// explicitly references the latest archive and is later than it (ADR-014
// invariant 6).
func (r *pubRec) archived() bool {
	if !r.hasArchive {
		return false
	}
	if r.restoreRef == nil || *r.restoreRef != r.archiveEvent {
		return true
	}
	return !later(r.restoreClock, r.restoreEID, r.archiveClock, r.archiveEvent)
}

// PubReactionTarget derives the STABLE reaction target for a document —
// independent of revisions, so reactions survive edits (ADR-014 inv. 7).
func PubReactionTarget(docID [16]byte) id.EventID {
	h := sha256.Sum256(append([]byte("qs.pub.react.v1"), docID[:]...))
	return id.EventID(h)
}

// PublicationComment is one thread node (flat rendering is a UI choice).
type PublicationComment struct {
	CommentID  [16]byte
	ParentID   *[16]byte
	Text       string
	Author     id.PrincipalID
	Clock      uint64
	CreatedAt  uint64 // author wall-clock (advisory) — display order
	EventID    id.EventID
	DocumentID [16]byte
}

// Publication is the projection of one document.
type Publication struct {
	DocumentID      [16]byte
	Document        *publication.Document
	Raw             []byte
	Title           string
	Author          id.PrincipalID
	RevisionEventID id.EventID
	PrevRevision    *id.EventID
	Clock           uint64
	// CreatedAt is the first accepted revision's wall clock (author-clock
	// advisory); an edit never refreshes it.
	CreatedAt uint64
	Archived  bool
	Comments  []PublicationComment
}

func (s *State) pubRecFor(docID [16]byte) *pubRec {
	if s.publications == nil {
		s.publications = map[[16]byte]*pubRec{}
	}
	rec, ok := s.publications[docID]
	if !ok {
		rec = &pubRec{comments: map[[16]byte]*PublicationComment{}}
		s.publications[docID] = rec
		// Register the stable target. Resonance registers need no drain —
		// unresolved registers project as soon as the target resolves.
		if s.pubTargets == nil {
			s.pubTargets = map[id.EventID][16]byte{}
		}
		target := PubReactionTarget(docID)
		s.pubTargets[target] = docID
		// A keep of this publication may have folded before the document.
		s.resolveKeepTarget(target)
	}
	return rec
}

// PublicationDocByTarget resolves a stable reaction/keep target to its
// document id.
func (s *State) PublicationDocByTarget(target id.EventID) ([16]byte, bool) {
	d, ok := s.pubTargets[target]
	return d, ok
}

func (s *State) applyPublicationRevision(env *signal.Envelope, eid id.EventID) {
	p, err := publication.DecodeRevisionPayload(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	doc, err := publication.Decode(p.Document)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.pubRecFor(doc.DocumentID)
	if rec.docRaw != nil && !later(env.LogicalClock, eid, rec.revClock, rec.revID) {
		return // stale revision
	}
	// CreatedAt is the wall clock of the FIRST accepted revision and never
	// moves after that (PS-2): an edit must not masquerade as a new post.
	// Author-clock advisory, like every CreatedAt in this codebase.
	if rec.docRaw == nil {
		rec.createdAt = env.CreatedAt
	}
	rec.docRaw = append([]byte(nil), p.Document...)
	rec.title = doc.Title
	rec.author = env.Principal
	rec.revID = eid
	rec.revClock = env.LogicalClock
	rec.prevRev = p.PrevRevision
	// NOTE: a later revision does NOT clear an archive — only restore does.
}

func (s *State) applyPublicationLifecycle(env *signal.Envelope, eid id.EventID, archive bool) {
	p, err := publication.DecodeLifecyclePayload(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.pubRecFor(p.DocumentID)
	if archive {
		if rec.hasArchive && !later(env.LogicalClock, eid, rec.archiveClock, rec.archiveEvent) {
			return
		}
		rec.hasArchive = true
		rec.archiveEvent = eid
		rec.archiveClock = env.LogicalClock
		return
	}
	// Restore register: latest restore wins, ARRIVAL ORDER IRRELEVANT — a
	// restore folded before its archive still counts once the archive
	// lands, because archived() re-derives from both registers. It lifts
	// the archive only when it explicitly references that archive event
	// and is later than it (ADR-014 invariant 6).
	if rec.restoreRef != nil && !later(env.LogicalClock, eid, rec.restoreClock, rec.restoreEID) {
		return
	}
	rec.restoreRef = p.ArchivedRevision
	rec.restoreEID = eid
	rec.restoreClock = env.LogicalClock
}

func (s *State) applyPublicationComment(env *signal.Envelope, eid id.EventID) {
	p, err := publication.DecodeCommentPayload(env.Payload)
	if err != nil {
		s.Unsupported["malformed:"+env.Schema]++
		return
	}
	rec := s.pubRecFor(p.DocumentID)
	// Comments are immutable in M1: first sight of a comment_id wins; a
	// replayed duplicate is ignored (edited/removed schemas are reserved).
	if _, seen := rec.comments[p.CommentID]; seen {
		return
	}
	rec.comments[p.CommentID] = &PublicationComment{
		CommentID: p.CommentID, ParentID: p.ParentID, Text: p.Text,
		Author: env.Principal, Clock: env.LogicalClock, CreatedAt: env.CreatedAt, EventID: eid,
		DocumentID: p.DocumentID,
	}
}

// Publications lists live documents, newest revision first. Archived
// documents are omitted here; ArchivedPublications lists them.
func (s *State) Publications() []Publication {
	return s.projectPublications(false)
}

// ArchivedPublications lists archived documents (for a future restore UI).
func (s *State) ArchivedPublications() []Publication {
	return s.projectPublications(true)
}

func (s *State) projectPublications(archived bool) []Publication {
	out := make([]Publication, 0, len(s.publications))
	for docID, rec := range s.publications {
		if rec.docRaw == nil || rec.archived() != archived {
			continue
		}
		doc, err := publication.Decode(rec.docRaw)
		if err != nil {
			continue
		}
		pub := Publication{
			DocumentID: docID, Document: doc, Raw: rec.docRaw,
			Title: rec.title, Author: rec.author,
			RevisionEventID: rec.revID, PrevRevision: rec.prevRev,
			Clock: rec.revClock, CreatedAt: rec.createdAt, Archived: rec.archived(),
		}
		for _, c := range rec.comments {
			pub.Comments = append(pub.Comments, *c)
		}
		// Display order is human wall-clock (CreatedAt): logical-clock order
		// puts a later comment before an earlier one when authors' own log
		// lengths differ. Logical clock then EventID are deterministic
		// tiebreaks (and cover comments authored in the same second).
		sort.Slice(pub.Comments, func(i, j int) bool {
			a, b := pub.Comments[i], pub.Comments[j]
			if a.CreatedAt != b.CreatedAt {
				return a.CreatedAt < b.CreatedAt
			}
			if a.Clock != b.Clock {
				return a.Clock < b.Clock
			}
			return string(a.EventID[:]) < string(b.EventID[:])
		})
		out = append(out, pub)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Clock != out[j].Clock {
			return out[i].Clock > out[j].Clock
		}
		return string(out[i].RevisionEventID[:]) > string(out[j].RevisionEventID[:])
	})
	return out
}

// PublicationByID returns one document's projection (live or archived).
func (s *State) PublicationByID(docID [16]byte) (Publication, bool) {
	rec, ok := s.publications[docID]
	if !ok || rec.docRaw == nil {
		return Publication{}, false
	}
	for _, p := range s.projectPublications(rec.archived()) {
		if p.DocumentID == docID {
			return p, true
		}
	}
	return Publication{}, false
}
