// Sending a message somewhere else (SHARE-1).
//
// The one thing to understand before reading the rest: FORWARDING IS
// QUOTATION, NOT TRANSMISSION OF A SIGNED STATEMENT. The original's payload
// is sealed under its own space's epoch and its signature covers that
// ciphertext, so nothing about it can be verified in the destination. What
// arrives is "alice sent something she says came from bob" — bob's
// signature does not travel, and the interface has to say so.
//
// Everything else follows from that.
//
// The QUOTATION IS DERIVED HERE, from the source log, never accepted from
// the caller. If the client composed it, this node would sign whatever it
// was handed, and a buggy or hostile client could attribute to bob words
// bob never wrote — with a signature making the lie durable.
//
// The COMMENT IS A SEPARATE MESSAGE, in order: the person's words, then the
// quotation. Five things get better at once — an older client shows both,
// an instruction to the assistant stays an ordinary human turn instead of a
// hidden prompt, the comment can be edited and replied to, the copy is
// identical for every target, and no second contract exists.
//
// The LOOP IS THE PLAIN LOOP. One user act, one copy per target, each
// signed and sealed with THAT space's epoch and handed to the ordinary
// pipeline. No batch event, no new subsystem, and nothing in this file
// touches ForwardingRole or the routing planner — transport forwarding and
// user sharing share no name here, on purpose.
package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/share"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// maxShareTargets bounds one act. Eight is more people than anybody sends
// the same thing to deliberately, and past that it stops being sharing.
const maxShareTargets = 8

// ShareOptions is what the person decided in the picker.
type ShareOptions struct {
	Comment string
	// NameAuthor is on by default — that is the point of a quotation. It is
	// a toggle rather than a text field: an editable attribution is a
	// forgery tool.
	NameAuthor bool
	// NameSource is OFF by default for every source, public ones included:
	// the name discloses that YOU are in that space regardless of whether
	// the space itself is public.
	NameSource bool
}

// ShareResult is one destination's outcome. Partial success is the normal
// case, not an error path, so every target gets its own row.
type ShareResult struct {
	Space string `json:"space"`
	OK    bool   `json:"ok"`
	// Comment and Copy are the two events this target received, in order.
	Comment string `json:"comment,omitempty"`
	Copy    string `json:"copy,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Share quotes one message into each target.
func (r *Runtime) Share(source id.TerminalID, event id.EventID,
	targets []id.TerminalID, o ShareOptions) ([]ShareResult, error) {

	if len(targets) == 0 {
		return nil, errors.New("node: nowhere to send it")
	}
	if len(targets) > maxShareTargets {
		return nil, fmt.Errorf("node: %d places at once is more than this sends", len(targets))
	}
	origin, quoted, err := r.quotationOf(source, event, o)
	if err != nil {
		return nil, err
	}

	// The composed form is built ONCE and is byte-identical in every copy —
	// the message is the same message wherever it went.
	body := composeQuote(origin, quoted)

	out := make([]ShareResult, 0, len(targets))
	for _, dest := range targets {
		res := ShareResult{Space: dest.Hex()}
		if dest == source {
			res.Error = "it is already in that space"
			out = append(out, res)
			continue
		}
		// The comment first, as an ordinary message of your own. If it
		// fails the copy is not attempted: a quotation arriving without the
		// sentence that framed it is worse than nothing arriving.
		if o.Comment != "" {
			eid, err := r.Say(dest, o.Comment, SayOptions{})
			if err != nil {
				res.Error = err.Error()
				out = append(out, res)
				continue
			}
			res.Comment = eid.Hex()
		}
		eid, err := r.sayQuote(dest, body, origin)
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		res.OK, res.Copy = true, eid.Hex()
		out = append(out, res)
		r.noteShared(dest)
	}
	return out, nil
}

// quotationOf runs every SOURCE gate and builds the provenance. It touches
// nothing: a refusal here writes no event in the source and none in any
// candidate target.
func (r *Runtime) quotationOf(source id.TerminalID, event id.EventID,
	o ShareOptions) (*schemas.ShareOrigin, string, error) {

	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[source]
	if !ok {
		return nil, "", errors.New("node: unknown space")
	}
	// Gate 2, before anything is read: a space that promised the past stays
	// with those who lived it does not get quoted out of.
	if _, ch := st.space.Character(); ch.Memory == "private_history" {
		return nil, "", errors.New(
			"node: the past stays with those who lived it — nothing leaves this space")
	}
	// Gate 3: unknown and tombstoned in one check, and a revised message
	// resolves to its CURRENT text, because that is what it says now.
	e, ok := st.space.State.EntryByID(event)
	if !ok {
		return nil, "", errors.New("node: that message is not here any more")
	}
	// Gate 1: what may be quoted at all, and what this build can quote yet.
	// Two different sentences, both true.
	schema := entrySchema(e)
	if !share.Shareable(schema) {
		return nil, "", errors.New("node: this kind of message cannot be forwarded")
	}
	if !share.EnabledInBeta(schema) {
		return nil, "", errors.New("node: this kind of message cannot be forwarded yet")
	}

	quoted, truncated := clipQuote(quotableText(e))
	if quoted == "" {
		return nil, "", errors.New("node: there is nothing here to quote")
	}

	origin := &schemas.ShareOrigin{OriginalAt: e.CreatedAt, Truncated: truncated}
	if o.NameAuthor {
		origin.AuthorLabel = clipName(authorNameLocked(r, st, e.Author))
	}
	if o.NameSource {
		origin.SourceLabel = clipName(r.ks.Spaces[source].Title)
	}
	// Gate 5, held BY CONSTRUCTION: nothing above reads e.Content.Text.Origin.
	// A share of a share quotes the message you are looking at — whose text
	// already carries the earlier attribution as prose — and attributes it
	// to the person who put it in front of you. That is what stops a claim
	// laundering itself into a fact across three hops.
	return origin, quoted, nil
}

// authorNameLocked resolves a principal to the name their manifest claims,
// which is what the feed shows and therefore what a quotation should say.
// Self-declared, like every name in this codebase — and here it is the
// SENDER repeating it, which is weaker still. Caller holds r.mu.
func authorNameLocked(r *Runtime, st *spaceState, who id.PrincipalID) string {
	if who == r.Principal.ID {
		return r.displayNameLocked()
	}
	for _, c := range st.space.MemberCards(0) {
		// Skip machine cards: the assistant shares this principal, and a
		// quotation must not attribute somebody's words to it.
		if c.Agency == "ai_agent" || c.Kind == "agent" || c.Kind == "bot" {
			continue
		}
		if c.Principal == who && c.Name != "" {
			return c.Name
		}
	}
	return ""
}

// sayQuote emits the copy. It goes through Say's gate rather than Emit's,
// because for a PRIVATE space canWrite is the only place a freeze is
// checked at all.
func (r *Runtime) sayQuote(dest id.TerminalID, body string,
	origin *schemas.ShareOrigin) (id.EventID, error) {

	r.mu.Lock()
	st, ok := r.spaces[dest]
	if !ok {
		r.mu.Unlock()
		return id.EventID{}, errors.New("node: unknown space")
	}
	if err := r.canWrite(st); err != nil {
		r.mu.Unlock()
		return id.EventID{}, err
	}
	// Key 4 rides alongside the composed text. A reader that understands it
	// renders from it; one that does not renders key 1 and is not misled,
	// because the node built both from the same read.
	payload, err := (&schemas.TextMessage{Text: body, Origin: origin}).Encode()
	if err != nil {
		r.mu.Unlock()
		return id.EventID{}, err
	}
	a, err := r.Self.Emit(st.space, schemas.MessageText, payload,
		signal.AuthorshipHuman, uint64(time.Now().Unix()))
	if err != nil {
		r.mu.Unlock()
		return id.EventID{}, err
	}
	r.persistEpochsLocked(dest, st.space)
	r.mu.Unlock()
	return a.ID, nil
}

// composeQuote renders key 1: what a client that has never heard of key 4
// will show. Language-neutral ON PURPOSE — a quote marker, names, an ISO
// date. English scaffolding baked into every payload would be
// unlocalizable forever, and this is a signed field.
func composeQuote(o *schemas.ShareOrigin, quoted string) string {
	var head []string
	if o.AuthorLabel != "" {
		head = append(head, o.AuthorLabel)
	}
	if o.SourceLabel != "" {
		head = append(head, o.SourceLabel)
	}
	if o.OriginalAt != 0 {
		head = append(head, time.Unix(int64(o.OriginalAt), 0).UTC().Format("2006-01-02"))
	}
	var b strings.Builder
	if len(head) > 0 {
		b.WriteString("> " + strings.Join(head, " · ") + "\n")
	}
	for _, line := range strings.Split(quoted, "\n") {
		b.WriteString("> " + line + "\n")
	}
	if o.Truncated {
		b.WriteString("> …\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// quotableText is what the entry says, in the form a quotation can carry.
// For a link that is the URL and its title; for anything media the fallback
// line would go here when media sharing lands.
func quotableText(e reducers.Entry) string {
	switch {
	case e.Content.Text != nil:
		return e.Content.Text.Text
	case e.Content.Link != nil:
		l := e.Content.Link
		if l.Title != "" && l.Title != l.URL {
			return l.Title + "\n" + l.URL
		}
		return l.URL
	}
	return ""
}

// entrySchema maps a materialised entry back to the schema it came from,
// so the allowlist can be consulted without re-reading the log.
func entrySchema(e reducers.Entry) string {
	switch e.Kind {
	case reducers.KindText:
		return schemas.MessageText
	case reducers.KindLink:
		return schemas.BlockLink
	case reducers.KindVisual:
		return schemas.BlockVisual
	case reducers.KindVideo:
		return schemas.BlockVideo
	case reducers.KindVoice:
		return schemas.BlockVoice
	case reducers.KindAudio:
		return schemas.BlockAudio
	case reducers.KindFile:
		return schemas.BlockFile
	}
	return ""
}

// clipQuote cuts on a RUNE boundary and reports that it cut, so nothing is
// shown as a whole sentence when it is half of one.
func clipQuote(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) <= schemas.MaxQuoteLen {
		return s, false
	}
	cut := s[:schemas.MaxQuoteLen]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut), true
}

func clipName(s string) string {
	s = strings.TrimSpace(s)
	for utf8.RuneCountInString(s) > 0 && len(s) > schemas.MaxShareName {
		s = string([]rune(s)[:utf8.RuneCountInString(s)-1])
	}
	return s
}

// noteShared remembers a destination for the picker's Recent list.
func (r *Runtime) noteShared(dest id.TerminalID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := []storage.NavRef{{Terminal: dest, Label: r.ks.Spaces[dest].Title}}
	for _, x := range r.ks.Navigator.Recent {
		if x.Terminal != dest {
			next = append(next, x)
		}
	}
	if len(next) > 8 {
		next = next[:8]
	}
	r.ks.Navigator.Recent = next
	_ = r.saveKeystore()
}

// parseEventHex reads an event id. There is no id.ParseEventID because an
// EventID is a content hash rather than a key, and nothing else needed to
// take one from a person until now.
func parseEventHex(s string) (id.EventID, error) {
	var out id.EventID
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(out) {
		return out, errors.New("node: not an event id")
	}
	copy(out[:], b)
	return out, nil
}

// ---- HTTP ----

func (a *APIServer) handleShare(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		SourceSpace string   `json:"source_space"`
		Event       string   `json:"event"`
		Targets     []string `json:"targets"`
		Comment     string   `json:"comment"`
		NameAuthor  *bool    `json:"name_author"`
		NameSource  bool     `json:"name_source"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	src, err := id.ParseTerminalID(body.SourceSpace)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("node: which space is it from?"))
		return
	}
	eid, err := parseEventHex(body.Event)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("node: which message?"))
		return
	}
	targets := make([]id.TerminalID, 0, len(body.Targets))
	for _, tHex := range body.Targets {
		tid, err := id.ParseTerminalID(tHex)
		if err != nil {
			httpErr(w, http.StatusBadRequest, errors.New("node: unreadable destination"))
			return
		}
		targets = append(targets, tid)
	}
	nameAuthor := true
	if body.NameAuthor != nil {
		nameAuthor = *body.NameAuthor
	}
	res, err := a.rt.Share(src, eid, targets, ShareOptions{
		Comment: strings.TrimSpace(body.Comment),
		// Naming the author is the point of a quotation; naming the space
		// is not, and stays opt-in for every source.
		NameAuthor: nameAuthor, NameSource: body.NameSource,
	})
	if err != nil {
		// A SOURCE refusal is a 403 and has written nothing anywhere.
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]any{"results": res})
}
