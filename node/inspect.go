// Looking at a public space without becoming its reader (CAT-0b).
//
// Browsing used to be implemented by OPENING: catalog.js pressed "look
// inside", which called POST /api/public/open, which created a durable
// reader replica, a SpaceMeta, a keystore write, an adopted global relay
// and — because membership in r.spaces IS the sync registration — an
// ongoing obligation to keep that space in step forever. Looking at a
// folder should not subscribe you to it.
//
// Inspect is the same verified reading the transient post preview already
// performs (PS-3), asked a different question: not "what does this post
// say" but "what is this space, and what does it list". It shares the
// session store, the TTL, the cap, the verification and the fetcher
// discipline — one dial, one materialisation, one budget — because a person
// who looks inside a directory and then opens a post in it is doing one
// thing, not two.
package node

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// maxInspectCards bounds a listing. Not paging: the session already holds
// the whole materialized state in memory, so a cursor would be ceremony
// over a slice; the projection is itself bounded (MaxProjectionBytes), so
// a space cannot carry an unbounded number of entries; and Publications()
// is deterministically ordered (clock desc, then revision id), which makes
// "the newest N" a stable, explainable rule rather than an arbitrary cut.
//
// The answer always carries the true total and says when it truncated, so
// it never pretends to be complete. If a real directory ever exceeds this,
// that is the signal to design a cursor — not to raise the constant.
const maxInspectCards = 200

// InspectState is the outcome of one inspection. Four expected outcomes;
// none of them indicates corruption or an exceptional node state — access
// refused is a policy ANSWER, and "this is not a directory" is the answer
// inspect exists to settle.
const (
	InspectResolved = "inspect_resolved" // read, and nothing was saved
	// existing_local and preview_waiting are shared with the post preview
	// deliberately: they mean the same thing and a client switching on the
	// state should not have to learn a second vocabulary for it.
)

// SpaceInspection is what a look answers with.
type SpaceInspection struct {
	PreviewID string
	// Space is the id this node VERIFIED, not the one the link claimed.
	// The client keys its cycle guard, its level cache and its rows on
	// this, and a share link is not an identity: two links through
	// different relays name one space.
	Space  id.TerminalID
	State  string
	Reason string

	Title   string
	Kind    string // "" ordinary | "directory" — the signed declaration
	Publish string
	Join    string
	Frozen  bool

	Cards      []InspectedCard
	CardsTotal int
	Truncated  bool
}

// InspectedCard is one publication of the inspected space. Every kind is
// returned, not only space-cards: inspect must be able to answer "this is
// not a directory", and it cannot do that by returning an empty list.
type InspectedCard struct {
	DocumentID string
	Kind       string // the PUBLICATION's kind: article | note | space | …
	Title      string
	Summary    string
	Cover      string
	Tags       []string
	Author     string
	Clock      uint64
	// Link is the target of a space-card, absent for every other kind.
	// Produced by spaceCardTarget — the same one definition the ordinary
	// publications list uses, so the two cannot disagree about where a
	// card points.
	Link string
}

// InspectPublicSpace reads a public space behind a share link and keeps
// nothing.
//
// A reference naming a post is accepted and its document id IGNORED:
// inspecting a post's link inspects its space, which is what a person
// pasting any link they have means.
//
// refresh drops a cached session and reads again. It is not a knob for its
// own sake: the session TTL is ten minutes, and a person pressing refresh
// on a directory must be able to move a stale list. The alternative — a
// shorter TTL for space-shaped reads — would fork the store, which is the
// one thing this gate refuses to do.
func (r *Runtime) InspectPublicSpace(reference string, refresh bool) (*SpaceInspection, error) {
	relayAddr, tid, _, err := ParsePublicLink(strings.TrimPrefix(strings.TrimSpace(reference), "qs:"))
	if err != nil {
		return nil, err
	}

	// Already held: plain navigation, no session at all. The same answer
	// the post preview gives, for the same reason.
	r.mu.Lock()
	_, held := r.spaces[tid]
	r.mu.Unlock()
	if held {
		return &SpaceInspection{Space: tid, State: PreviewExistingLocal}, nil
	}

	sess := r.previews.bySpace(tid)
	if sess != nil && refresh {
		r.previews.drop(sess.id)
		sess = nil
	}
	if sess == nil {
		s, waiting, err := r.openPublicSession(relayAddr, tid)
		if err != nil {
			return nil, err
		}
		if waiting != "" {
			return &SpaceInspection{Space: tid, State: PreviewWaiting, Reason: waiting}, nil
		}
		sess = s
	}

	out := &SpaceInspection{
		PreviewID: sess.id, Space: tid, State: InspectResolved,
		Title: sess.title, Kind: sess.kind, Publish: sess.publish,
		Join: sess.join, Frozen: sess.frozen,
	}
	pubs := sess.state.Publications()
	out.CardsTotal = len(pubs)
	if len(pubs) > maxInspectCards {
		pubs = pubs[:maxInspectCards]
		out.Truncated = true
	}
	out.Cards = make([]InspectedCard, 0, len(pubs))
	for _, p := range pubs {
		c := InspectedCard{
			DocumentID: hex.EncodeToString(p.DocumentID[:]),
			Title:      p.Title,
			Author:     p.Author.String(),
			Clock:      p.Clock,
		}
		if p.Document != nil {
			c.Kind = p.Document.Kind
			c.Summary = p.Document.Summary
			c.Cover = p.Document.Cover
			c.Tags = p.Document.Tags
			if p.Document.Kind == "space" {
				c.Link = spaceCardTarget(p.Document)
			}
		}
		out.Cards = append(out.Cards, c)
	}

	// The covers of the cards THIS LOOK RETURNED, and nothing else.
	//
	// PM-1's rule is that a session's asset allowlist grows only by what a
	// person actually opened, so that a transient reading never becomes a
	// browse handle on a space's whole store. A directory listing is the
	// one place that rule needed arguing one level down: the person DID
	// open this directory, and the entry covers are that page's own graph.
	// So the widening is exactly that — cover role, these cards, nothing
	// deeper, and no walk of anything the listing does not show.
	if sess.fetcher != nil {
		covers := map[string]string{}
		for _, c := range out.Cards {
			if c.Cover != "" {
				covers[c.Cover] = "cover"
			}
		}
		if len(covers) > 0 {
			sess.fetcher.extendGraph(covers, sess.assets)
		}
	}
	return out, nil
}

// ---- API ----

func (a *APIServer) handlePublicInspect(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Reference string `json:"reference"`
		Refresh   bool   `json:"refresh"`
	}](r)
	if err != nil || strings.TrimSpace(body.Reference) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("reference required"))
		return
	}
	in, err := a.rt.InspectPublicSpace(body.Reference, body.Refresh)
	if err != nil {
		// 403 for every refusal, matching the post preview: "not a public
		// space" and "malformed link" are both answers about what may be
		// read, and splitting them here without splitting them there would
		// leave the client two vocabularies for one act.
		httpErr(w, http.StatusForbidden, err)
		return
	}
	cards := make([]map[string]any, 0, len(in.Cards))
	for _, c := range in.Cards {
		row := map[string]any{
			"document_id":      c.DocumentID,
			"publication_kind": c.Kind,
			"title":            c.Title,
			"summary":          c.Summary,
			"cover":            c.Cover,
			"tags":             c.Tags,
			"author":           c.Author,
			"clock":            c.Clock,
		}
		if c.Link != "" {
			row["link"] = c.Link
		}
		cards = append(cards, row)
	}
	out := map[string]any{
		"preview_id":  in.PreviewID,
		"space_id":    in.Space.Hex(),
		"state":       in.State,
		"space_title": in.Title,
		"publish":     in.Publish,
		"join":        in.Join,
		"frozen":      in.Frozen,
		"cards":       cards,
		"cards_total": in.CardsTotal,
		"truncated":   in.Truncated,
	}
	if in.Kind != "" {
		out["kind"] = in.Kind
	}
	if in.Reason != "" {
		out["reason"] = in.Reason
	}
	writeJSON(w, out)
}

// handlePreviewClose ends a session now rather than at its TTL. Browsing
// opens one per level, and a reader who walked five levels should not leave
// five fetchers holding budget behind them.
//
// A missing pid answers 200: closing twice is not an error, and a client
// that lost track of a session must be able to say so without branching.
func (a *APIServer) handlePreviewClose(w http.ResponseWriter, r *http.Request) {
	a.rt.previews.drop(r.PathValue("pid"))
	writeJSON(w, map[string]bool{"ok": true})
}

