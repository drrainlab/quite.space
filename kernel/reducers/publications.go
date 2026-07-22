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

	archived     bool
	archiveEvent id.EventID
	archiveClock uint64

	comments  map[[16]byte]*PublicationComment
	reactions map[reactionKey]reactionState
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
	Archived        bool
	Comments        []PublicationComment
	// Reactions: emoji → principals with active reactions (LWW-merged).
	Reactions map[string][]id.PrincipalID
}

func (s *State) pubRecFor(docID [16]byte) *pubRec {
	if s.publications == nil {
		s.publications = map[[16]byte]*pubRec{}
	}
	rec, ok := s.publications[docID]
	if !ok {
		rec = &pubRec{
			comments:  map[[16]byte]*PublicationComment{},
			reactions: map[reactionKey]reactionState{},
		}
		s.publications[docID] = rec
		// Register the stable reaction target and drain reactions that
		// arrived before the document (order tolerance, as installEntry).
		if s.pubTargets == nil {
			s.pubTargets = map[id.EventID][16]byte{}
		}
		target := PubReactionTarget(docID)
		s.pubTargets[target] = docID
		for _, p := range s.pendingReactions[target] {
			s.applyPubReaction(docID, p.author, p.emoji, p.active, p.clock, p.eid)
		}
		delete(s.pendingReactions, target)
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

// applyPubReaction is the LWW merge for publication reactions.
func (s *State) applyPubReaction(docID [16]byte, author id.PrincipalID, emoji string,
	active bool, clock uint64, eid id.EventID) {

	rec := s.pubRecFor(docID)
	key := reactionKey{principal: author, emoji: emoji}
	cur, exists := rec.reactions[key]
	if exists && !later(clock, eid, cur.clock, cur.event) {
		return
	}
	rec.reactions[key] = reactionState{active: active, clock: clock, event: eid}
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
		if rec.archived && !later(env.LogicalClock, eid, rec.archiveClock, rec.archiveEvent) {
			return
		}
		rec.archived = true
		rec.archiveEvent = eid
		rec.archiveClock = env.LogicalClock
		return
	}
	// Restore lifts an archive only when it is LATER than the archive and
	// explicitly references it (ADR-014 invariant 6).
	if !rec.archived {
		return
	}
	if p.ArchivedRevision == nil || *p.ArchivedRevision != rec.archiveEvent {
		return
	}
	if !later(env.LogicalClock, eid, rec.archiveClock, rec.archiveEvent) {
		return
	}
	rec.archived = false
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
		Author: env.Principal, Clock: env.LogicalClock, EventID: eid,
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
		if rec.docRaw == nil || rec.archived != archived {
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
			Clock: rec.revClock, Archived: rec.archived,
		}
		for _, c := range rec.comments {
			pub.Comments = append(pub.Comments, *c)
		}
		pub.Reactions = map[string][]id.PrincipalID{}
		for key, st := range rec.reactions {
			if st.active {
				pub.Reactions[key.emoji] = append(pub.Reactions[key.emoji], key.principal)
			}
		}
		for _, ps := range pub.Reactions {
			sort.Slice(ps, func(i, j int) bool { return string(ps[i][:]) < string(ps[j][:]) })
		}
		sort.Slice(pub.Comments, func(i, j int) bool {
			if pub.Comments[i].Clock != pub.Comments[j].Clock {
				return pub.Comments[i].Clock < pub.Comments[j].Clock
			}
			return string(pub.Comments[i].EventID[:]) < string(pub.Comments[j].EventID[:])
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
	for _, p := range s.projectPublications(rec.archived) {
		if p.DocumentID == docID {
			return p, true
		}
	}
	return Publication{}, false
}
