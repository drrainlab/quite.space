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
	"github.com/drrainlab/quiet_places/protocol/publication"
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
	// NoReference drops the way back from a POST card (PS-2). The toggle
	// means "attach no path back", never "stop sending a card": a public
	// post always travels as a card, and without a reference it is a
	// readable snapshot with no door.
	NoReference bool
}

// quoteRef names what is being quoted: a chat message OR a publication.
// Exactly one is set. One gate spine runs over both — the checks that must
// never drift (private_history, author resolution, clipping, dest==source)
// are written once, and only the resolution differs.
type quoteRef struct {
	Event    *id.EventID
	Document *[16]byte
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
	return r.shareRef(source, quoteRef{Event: &event}, targets, o)
}

// SharePost forwards one publication into each target, as a card.
func (r *Runtime) SharePost(source id.TerminalID, doc [16]byte,
	targets []id.TerminalID, o ShareOptions) ([]ShareResult, error) {
	return r.shareRef(source, quoteRef{Document: &doc}, targets, o)
}

func (r *Runtime) shareRef(source id.TerminalID, ref quoteRef,
	targets []id.TerminalID, o ShareOptions) ([]ShareResult, error) {

	if len(targets) == 0 {
		return nil, errors.New("node: nowhere to send it")
	}
	if len(targets) > maxShareTargets {
		return nil, fmt.Errorf("node: %d places at once is more than this sends", len(targets))
	}
	origin, quoted, card, err := r.quotationOf(source, ref, o)
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
		eid, err := r.sayQuote(dest, body, origin, card)
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

// quotationOf runs every SOURCE gate and builds the provenance — and, for a
// publication out of a public space, the card. It touches nothing: a refusal
// here writes no event in the source and none in any candidate target.
//
// One gate spine for both kinds of ref; only the resolution differs.
func (r *Runtime) quotationOf(source id.TerminalID, ref quoteRef,
	o ShareOptions) (*schemas.ShareOrigin, string, *schemas.SharedPublication, error) {

	// Read BEFORE the lock: the resolver takes r.mu (as GetSettings did),
	// and composing a reference inside the locked builder was this gate's
	// ready-made deadlock. It is the resolver rather than the raw setting
	// because in automatic mode that setting is empty by design, and a card
	// would have gone out with no way back on a perfectly connected node.
	globalRelay := r.ResolvePersonalRelay()

	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[source]
	if !ok {
		return nil, "", nil, errors.New("node: unknown space")
	}
	// Gate 2, before anything is read: a space that promised the past stays
	// with those who lived it does not get quoted out of. Held for posts by
	// construction — the gate runs before the ref is even looked at.
	if _, ch := st.space.Character(); ch.Memory == "private_history" {
		return nil, "", nil, errors.New(
			"node: the past stays with those who lived it — nothing leaves this space")
	}

	var (
		quoted    string
		truncated bool
		author    id.PrincipalID
		createdAt uint64
		card      *schemas.SharedPublication
	)
	switch {
	case ref.Event != nil:
		// Gate 3: unknown and tombstoned in one check, and a revised message
		// resolves to its CURRENT text, because that is what it says now.
		e, ok := st.space.State.EntryByID(*ref.Event)
		if !ok {
			return nil, "", nil, errors.New("node: that message is not here any more")
		}
		// Gate 1: what may be quoted at all, and what this build can quote
		// yet. Two different sentences, both true.
		schema := entrySchema(e)
		if !share.Shareable(schema) {
			return nil, "", nil, errors.New("node: this kind of message cannot be forwarded")
		}
		if !share.EnabledInBeta(schema) {
			return nil, "", nil, errors.New("node: this kind of message cannot be forwarded yet")
		}
		quoted, truncated = clipQuote(quotableText(e))
		author, createdAt = e.Author, e.CreatedAt

	case ref.Document != nil:
		// A publication resolves through its own projection, never through
		// the schema allowlist — the keep precedent (protocol/keep).
		pub, ok := st.space.State.PublicationByID(*ref.Document)
		if !ok {
			return nil, "", nil, errors.New("node: that post is not here any more")
		}
		// The author withdrew it. Its own sentence — going against that
		// decision and "not found" must not sound alike.
		if pub.Archived {
			return nil, "", nil, errors.New("node: this post was withdrawn by its author")
		}
		quoted, truncated = clipQuote(postQuoteText(pub))
		// The signer, NEVER Document.DisplayAuthors: that field is free
		// text the author edits, and an editable attribution is a forgery
		// tool. Enforced here by not reading it.
		author, createdAt = pub.Author, pub.CreatedAt

		// The card, always, for a post out of a link-readable space. The
		// reference inside it is OPTIONAL: "attach no path back" and "send
		// no card" are different sentences, and the toggle means the first.
		if r.canReferenceByPublicLinkLocked(source) {
			card = &schemas.SharedPublication{
				Title:   clipCardText(pub.Title, schemas.MaxCardTitle),
				Summary: clipCardText(pub.Document.Summary, schemas.MaxCardSummary),
			}
			relayAddr := r.ks.Spaces[source].SourceRelay
			if relayAddr == "" {
				relayAddr = globalRelay
			}
			if !o.NoReference && relayAddr != "" {
				card.Reference = composeShare(relayAddr,
					publicLinkPrefix+source.Hex()+":"+hex.EncodeToString(ref.Document[:]))
			}
		}

	default:
		return nil, "", nil, errors.New("node: nothing to share")
	}

	if quoted == "" {
		return nil, "", nil, errors.New("node: there is nothing here to quote")
	}

	origin := &schemas.ShareOrigin{OriginalAt: createdAt, Truncated: truncated}
	if o.NameAuthor {
		origin.AuthorLabel = clipName(authorNameLocked(r, st, author))
	}
	// A reference implies the source's name: hiding the name while carrying
	// the address is theatre — the recipient learns it in the first click.
	if o.NameSource || (card != nil && card.Reference != "") {
		origin.SourceLabel = clipName(r.ks.Spaces[source].Title)
	}
	// Gate 5, held BY CONSTRUCTION: nothing above reads e.Content.Text.Origin.
	// A share of a share quotes the message you are looking at — whose text
	// already carries the earlier attribution as prose — and attributes it
	// to the person who put it in front of you. That is what stops a claim
	// laundering itself into a fact across three hops.
	return origin, quoted, card, nil
}

// postQuoteText is a publication's quotable face: the title, then the
// summary, falling back to the first text-ish block when there is none.
func postQuoteText(pub reducers.Publication) string {
	body := strings.TrimSpace(pub.Document.Summary)
	if body == "" {
		for _, b := range pub.Document.Blocks {
			switch b.Type {
			case "text", "heading", "quote":
				if tp, err := publication.ParseTextProps(b.RawProps); err == nil && tp.Text != "" {
					body = strings.TrimSpace(tp.Text)
				}
			}
			if body != "" {
				break
			}
		}
	}
	title := strings.TrimSpace(pub.Title)
	switch {
	case title != "" && body != "":
		return title + "\n" + body
	case title != "":
		return title
	}
	return body
}

// clipCardText bounds a card field on a rune boundary, silently — the card
// is a snapshot, and key 1 carries the honest truncation marker.
func clipCardText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut)
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
	origin *schemas.ShareOrigin, card *schemas.SharedPublication) (id.EventID, error) {

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
	// Keys 4 and 6 ride alongside the composed text. A reader that
	// understands them renders from them; one that does not renders key 1
	// and is not misled, because the node built all three from one read.
	payload, err := (&schemas.TextMessage{Text: body, Origin: origin, Card: card}).Encode()
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

// quoteHeadline is the ONE composer of the header line — and, in
// quotedLines, the one matcher. A boolean predicate shared between them
// would still misparse a body line that happens to look like a header and
// would rot silently if the composition ever changed; matching the exact
// string cannot drift, because there is nothing to keep in step.
func quoteHeadline(o *schemas.ShareOrigin) string {
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
	return strings.Join(head, " · ")
}

// composeQuote renders key 1: what a client that has never heard of key 4
// will show. Language-neutral ON PURPOSE — a quote marker, names, an ISO
// date. English scaffolding baked into every payload would be
// unlocalizable forever, and this is a signed field.
func composeQuote(o *schemas.ShareOrigin, quoted string) string {
	var b strings.Builder
	if h := quoteHeadline(o); h != "" {
		b.WriteString("> " + h + "\n")
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
//
// Three fixes found by review (PS-2). The label honors LocalTitle — the
// name THIS device chose — so Recent does not show a space under a
// different name than the sidebar. The cap is maxNavRecent, not a second
// literal that had quietly disagreed with it. And the VERSION BUMPS: this
// is a write to the same document SetNavigator guards with optimistic
// concurrency, and without the bump a client PUT at the version it
// already held would silently clobber the share-derived entry. The
// client's 409 path reloads, so the conflict is visible and resolvable
// rather than a silent lost update.
func (r *Runtime) noteShared(dest id.TerminalID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta := r.ks.Spaces[dest]
	label := meta.LocalTitle
	if label == "" {
		label = meta.Title
	}
	next := []storage.NavRef{{Terminal: dest, Label: label}}
	for _, x := range r.ks.Navigator.Recent {
		if x.Terminal != dest {
			next = append(next, x)
		}
	}
	if len(next) > maxNavRecent {
		next = next[:maxNavRecent]
	}
	r.ks.Navigator.Recent = next
	r.ks.Navigator.Version++
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
		Document    string   `json:"document"`
		Targets     []string `json:"targets"`
		Comment     string   `json:"comment"`
		NameAuthor  *bool    `json:"name_author"`
		NameSource  bool     `json:"name_source"`
		NoReference bool     `json:"no_reference"`
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
	// Exactly one of event / document: a share is of one thing.
	var ref quoteRef
	switch {
	case body.Event != "" && body.Document != "":
		httpErr(w, http.StatusBadRequest, errors.New("node: a message or a post — one of them"))
		return
	case body.Event != "":
		eid, err := parseEventHex(body.Event)
		if err != nil {
			httpErr(w, http.StatusBadRequest, errors.New("node: which message?"))
			return
		}
		ref.Event = &eid
	case body.Document != "":
		b, err := hex.DecodeString(strings.TrimSpace(body.Document))
		if err != nil || len(b) != 16 {
			httpErr(w, http.StatusBadRequest, errors.New("node: which post?"))
			return
		}
		var d [16]byte
		copy(d[:], b)
		ref.Document = &d
	default:
		httpErr(w, http.StatusBadRequest, errors.New("node: nothing to share"))
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
	res, err := a.rt.shareRef(src, ref, targets, ShareOptions{
		Comment: strings.TrimSpace(body.Comment),
		// Naming the author is the point of a quotation; naming the space
		// is not, and stays opt-in for every source — except where a
		// reference makes it implied (see quotationOf).
		NameAuthor: nameAuthor, NameSource: body.NameSource,
		NoReference: body.NoReference,
	})
	if err != nil {
		// A SOURCE refusal is a 403 and has written nothing anywhere.
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]any{"results": res})
}
