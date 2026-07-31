// Publications (ADR-014, PUB-0): drafts stay local and sealed; publishing
// validates the document and emits a signed revision event with optimistic
// concurrency — a stale base returns a conflict and the draft survives.
package node

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/publication"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// ---- JSON <-> Document (the API/composer model) ----

type blockJSON struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Props    *propsJSON  `json:"props,omitempty"`
	RawB64   string      `json:"props_raw_b64,omitempty"` // opaque foreign props
	Children []blockJSON `json:"children,omitempty"`
}

type propsJSON struct {
	Text    string   `json:"text,omitempty"`
	Extra   string   `json:"extra,omitempty"`
	More    string   `json:"more,omitempty"`
	Asset   string   `json:"asset,omitempty"`
	Caption string   `json:"caption,omitempty"`
	Items   []string `json:"items,omitempty"`
}

type documentJSON struct {
	DocumentID string      `json:"document_id,omitempty"`
	Kind       string      `json:"kind"`
	Title      string      `json:"title"`
	Summary    string      `json:"summary,omitempty"`
	Cover      string      `json:"cover,omitempty"`
	Authors    []string    `json:"authors,omitempty"`
	Tags       []string    `json:"tags,omitempty"`
	Layout     string      `json:"layout,omitempty"`
	Discussion string      `json:"discussion,omitempty"`
	Visibility string      `json:"visibility_intent"`
	Blocks     []blockJSON `json:"blocks"`
	// A POINTER, so a post without one produces no key at all rather than an
	// empty object. Every publication written before AM-1 must keep exactly
	// the JSON shape it had, and the reader treats the field's presence as
	// the question "does this post have another layer?" — an empty object
	// would answer yes and then render a region with no scene and no words.
	Atmosphere *atmosphereJSON `json:"atmosphere,omitempty"`
}

// The atmosphere as it crosses to the client. Field for field with
// protocol/publication/atmosphere.go and nothing else: the Document it comes
// from has already been validated against every bound in the contract, so
// anything this layer renamed, defaulted or re-derived would be a second,
// unvalidated opinion about the same recipe. Permille stays permille and hex
// stays hex; the client divides once, in one place.
type atmosphereJSON struct {
	Visual   atmoVisualJSON    `json:"visual"`
	Audio    *atmoAudioJSON    `json:"audio,omitempty"`
	Reactive *atmoReactiveJSON `json:"reactive,omitempty"`
	Fallback atmoFallbackJSON  `json:"fallback"`
	// Lineage travels NOW, though only AM-5's "Use this atmosphere" reads it.
	// The alternative is widening the wire shape twice for one feature, and a
	// wire shape is the most expensive thing here to change twice.
	DerivedFrom *atmoDerivedJSON `json:"derived_from,omitempty"`
}

type atmoVisualJSON struct {
	Scene   string           `json:"scene"`
	Seed    uint64           `json:"seed"`
	Params  []atmoParamJSON  `json:"params,omitempty"`
	Palette []atmoColourJSON `json:"palette,omitempty"`
}

type atmoParamJSON struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"` // permille, 0..1000
}

type atmoColourJSON struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

type atmoAudioJSON struct {
	Asset     string `json:"asset"`
	Mode      string `json:"mode"` // loop | once
	Gain      uint64 `json:"gain"` // permille
	FadeInMs  uint64 `json:"fade_in_ms,omitempty"`
	FadeOutMs uint64 `json:"fade_out_ms,omitempty"`
}

type atmoReactiveJSON struct {
	Audio   bool `json:"audio"`
	Pointer bool `json:"pointer"`
}

type atmoFallbackJSON struct {
	Text   string `json:"text"`
	Poster string `json:"poster,omitempty"`
}

type atmoDerivedJSON struct {
	RecipeHash    string `json:"recipe_hash,omitempty"` // hex of the 32-byte digest
	PublicationID string `json:"publication_id,omitempty"`
	RevisionHash  string `json:"revision_hash,omitempty"`
}

func blockFromJSON(bj blockJSON) (publication.Block, error) {
	b := publication.Block{ID: bj.ID, Type: bj.Type}
	if bj.RawB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(bj.RawB64)
		if err != nil {
			return b, errors.New("node: bad opaque props encoding")
		}
		b.RawProps = raw
	} else if p := bj.Props; p != nil {
		switch {
		case len(p.Items) > 0 || bj.Type == "gallery" || bj.Type == "credits":
			b.RawProps = publication.EncodeListProps(publication.ListProps{Items: p.Items, Text: p.Text})
		case p.Asset != "" || bj.Type == "image" || bj.Type == "audio" || bj.Type == "file" ||
			bj.Type == "video-link" || bj.Type == "app":
			b.RawProps = publication.EncodeAssetProps(publication.AssetProps{Asset: p.Asset, Text: p.Text, Caption: p.Caption})
		default:
			b.RawProps = publication.EncodeTextProps(publication.TextProps{Text: p.Text, Extra: p.Extra, More: p.More})
		}
	}
	for _, cj := range bj.Children {
		cb, err := blockFromJSON(cj)
		if err != nil {
			return b, err
		}
		b.Children = append(b.Children, cb)
	}
	return b, nil
}

func blockToJSON(b publication.Block) blockJSON {
	bj := blockJSON{ID: b.ID, Type: b.Type}
	if len(b.RawProps) > 0 {
		if publication.KnownBlockType(b.Type) {
			switch {
			case b.Type == "gallery" || b.Type == "credits":
				if p, err := publication.ParseListProps(b.RawProps); err == nil {
					bj.Props = &propsJSON{Items: p.Items, Text: p.Text}
				}
			case b.Type == "image" || b.Type == "audio" || b.Type == "file" ||
				b.Type == "video-link" || b.Type == "app":
				if p, err := publication.ParseAssetProps(b.RawProps); err == nil {
					bj.Props = &propsJSON{Asset: p.Asset, Text: p.Text, Caption: p.Caption}
				}
			default:
				if p, err := publication.ParseTextProps(b.RawProps); err == nil {
					bj.Props = &propsJSON{Text: p.Text, Extra: p.Extra, More: p.More}
				}
			}
		}
		if bj.Props == nil { // unknown/opaque — keep raw bytes visible
			bj.RawB64 = base64.StdEncoding.EncodeToString(b.RawProps)
		}
	}
	for _, c := range b.Children {
		bj.Children = append(bj.Children, blockToJSON(c))
	}
	return bj
}

func documentFromJSON(dj documentJSON) (*publication.Document, error) {
	doc := &publication.Document{
		Kind: dj.Kind, Title: strings.TrimSpace(dj.Title), Summary: dj.Summary,
		Cover: dj.Cover, DisplayAuthors: dj.Authors, Tags: dj.Tags,
		Layout: dj.Layout, Discussion: dj.Discussion, Visibility: dj.Visibility,
	}
	if doc.Visibility == "" {
		doc.Visibility = "space"
	}
	if doc.Kind == "" {
		doc.Kind = "article"
	}
	if dj.DocumentID != "" {
		b, err := hex.DecodeString(dj.DocumentID)
		if err != nil || len(b) != 16 {
			return nil, errors.New("node: bad document id")
		}
		copy(doc.DocumentID[:], b)
	} else if _, err := rand.Read(doc.DocumentID[:]); err != nil {
		return nil, err
	}
	for _, bj := range dj.Blocks {
		b, err := blockFromJSON(bj)
		if err != nil {
			return nil, err
		}
		doc.Blocks = append(doc.Blocks, b)
	}
	atmo, err := atmosphereFromJSON(dj.Atmosphere)
	if err != nil {
		return nil, err
	}
	doc.Atmosphere = atmo
	return doc, nil
}

func documentToJSON(doc *publication.Document) documentJSON {
	dj := documentJSON{
		DocumentID: hex.EncodeToString(doc.DocumentID[:]),
		Kind:       doc.Kind, Title: doc.Title, Summary: doc.Summary,
		Cover: doc.Cover, Authors: doc.DisplayAuthors, Tags: doc.Tags,
		Layout: doc.Layout, Discussion: doc.Discussion, Visibility: doc.Visibility,
	}
	for _, b := range doc.Blocks {
		dj.Blocks = append(dj.Blocks, blockToJSON(b))
	}
	dj.Atmosphere = atmosphereToJSON(doc.Atmosphere)
	return dj
}

// atmosphereToJSON projects the recipe, or nil when there is not one.
func atmosphereToJSON(a *publication.Atmosphere) *atmosphereJSON {
	if a == nil {
		return nil
	}
	aj := &atmosphereJSON{
		Visual:   atmoVisualJSON{Scene: a.Visual.Scene, Seed: a.Visual.Seed},
		Fallback: atmoFallbackJSON{Text: a.Fall.Text, Poster: a.Fall.Poster},
	}
	for _, p := range a.Visual.Params {
		aj.Visual.Params = append(aj.Visual.Params, atmoParamJSON{Name: p.Name, Value: p.Value})
	}
	for _, c := range a.Visual.Palette {
		aj.Visual.Palette = append(aj.Visual.Palette, atmoColourJSON{Name: c.Name, Hex: c.Hex})
	}
	if a.Audio != nil {
		aj.Audio = &atmoAudioJSON{
			Asset: a.Audio.Asset, Mode: a.Audio.Mode, Gain: a.Audio.Gain,
			FadeInMs: a.Audio.FadeInMs, FadeOutMs: a.Audio.FadeOutMs,
		}
	}
	if a.React != nil {
		aj.Reactive = &atmoReactiveJSON{Audio: a.React.Audio, Pointer: a.React.Pointer}
	}
	if a.Derived != nil {
		aj.DerivedFrom = &atmoDerivedJSON{
			RecipeHash:    hex.EncodeToString(a.Derived.RecipeHash),
			PublicationID: a.Derived.PublicationID,
			RevisionHash:  a.Derived.RevisionHash,
		}
	}
	return aj
}

// atmosphereFromJSON is the authoring direction, and it exists so that editing
// a post does not silently strip its atmosphere: the composer round-trips a
// document through this layer, and a field the round-trip drops is a field the
// next save deletes. Bounds are NOT enforced here — publication.Validate is
// the one place that decides what is legal, and a second opinion in this
// layer is how the two drift apart.
func atmosphereFromJSON(aj *atmosphereJSON) (*publication.Atmosphere, error) {
	if aj == nil {
		return nil, nil
	}
	a := &publication.Atmosphere{
		Visual: publication.Visual{Scene: aj.Visual.Scene, Seed: aj.Visual.Seed},
		Fall:   publication.Fallback{Text: aj.Fallback.Text, Poster: aj.Fallback.Poster},
	}
	for _, p := range aj.Visual.Params {
		a.Visual.Params = append(a.Visual.Params, publication.Param{Name: p.Name, Value: p.Value})
	}
	for _, c := range aj.Visual.Palette {
		a.Visual.Palette = append(a.Visual.Palette, publication.PaletteToken{Name: c.Name, Hex: c.Hex})
	}
	if aj.Audio != nil {
		a.Audio = &publication.Audio{
			Asset: aj.Audio.Asset, Mode: aj.Audio.Mode, Gain: aj.Audio.Gain,
			FadeInMs: aj.Audio.FadeInMs, FadeOutMs: aj.Audio.FadeOutMs,
		}
	}
	if aj.Reactive != nil {
		a.React = &publication.Reactive{Audio: aj.Reactive.Audio, Pointer: aj.Reactive.Pointer}
	}
	if aj.DerivedFrom != nil {
		d := &publication.Derived{
			PublicationID: aj.DerivedFrom.PublicationID,
			RevisionHash:  aj.DerivedFrom.RevisionHash,
		}
		if aj.DerivedFrom.RecipeHash != "" {
			h, err := hex.DecodeString(aj.DerivedFrom.RecipeHash)
			if err != nil {
				return nil, errors.New("node: bad atmosphere recipe hash")
			}
			d.RecipeHash = h
		}
		a.Derived = d
	}
	return a, nil
}

// ---- Runtime operations ----

// spaceAssetOK returns the validator's asset check for one space.
func (r *Runtime) spaceAssetOK(tid id.TerminalID) func(string) bool {
	return func(hexID string) bool {
		_, ok := r.assetIdx.refs[AssetKey{Space: tid, Asset: hexID}]
		return ok
	}
}

// ErrNoSuchSource is returned when a draft claims to derive from a
// publication this space does not hold, or from one carrying no atmosphere.
// Refusing is the point: an unresolvable pointer published as-is would be a
// lineage claim nobody could ever check.
var ErrNoSuchSource = errors.New(
	"node: that atmosphere's source is not in this space — nothing to derive from")

// resolveLineageLocked fills derived_from from the source the draft names.
// Caller holds r.mu.
//
// Anonymous inheritance — a digest with no pointer — is left exactly as it
// arrived. It claims only "this recipe equals some other recipe", which is
// either true of the bytes or not, and it names nobody, so there is nothing
// here to verify on the author's behalf.
func resolveLineageLocked(st *spaceState, doc *publication.Document) error {
	if doc == nil || doc.Atmosphere == nil || doc.Atmosphere.Derived == nil {
		return nil
	}
	d := doc.Atmosphere.Derived
	if d.PublicationID == "" {
		return nil
	}
	raw, err := hex.DecodeString(d.PublicationID)
	if err != nil || len(raw) != 16 {
		return ErrNoSuchSource
	}
	var srcID [16]byte
	copy(srcID[:], raw)
	src, ok := st.space.State.PublicationByID(srcID)
	if !ok || src.Document == nil || src.Document.Atmosphere == nil {
		return ErrNoSuchSource
	}
	// Both fields come off the source, not off the request.
	d.RecipeHash = src.Document.Atmosphere.RecipeHash()
	d.RevisionHash = src.RevisionEventID.Hex()
	return nil
}

// ErrPublishConflict marks a stale base revision (optimistic concurrency).
var ErrPublishConflict = errors.New("node: the post changed elsewhere — republish from the latest revision")

// PublishDocument validates and emits a signed revision. baseRevision is the
// revision the author edited (nil for a brand-new document); on a stale base
// the publish is refused and nothing is emitted.
func (r *Runtime) PublishDocument(tid id.TerminalID, doc *publication.Document, baseRevision *id.EventID) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	// Lineage is COMPUTED HERE, never accepted from the caller — and this is
	// the whole difference between a verifiable digest and a free-text claim.
	// If a client could hand us the hash alongside the pointer, "derived from
	// that post" would be a sentence anyone can write about any post. So when
	// a draft names a source, the node looks that source up in this space and
	// takes the digest off the recipe it actually holds, overwriting whatever
	// arrived. A name that resolves to nothing is refused rather than
	// published as an unverifiable claim.
	//
	// It runs BEFORE Validate so the bytes that get checked are the bytes that
	// get signed; filling this in afterwards would sign a field nothing had
	// looked at.
	if err := resolveLineageLocked(st, doc); err != nil {
		return id.EventID{}, err
	}
	if err := publication.Validate(doc, r.spaceAssetOK(tid)); err != nil {
		return id.EventID{}, err
	}

	cur, exists := st.space.State.PublicationByID(doc.DocumentID)
	schema := publication.SchemaPublished
	var prev *id.EventID
	if exists {
		schema = publication.SchemaRevised
		tip := cur.RevisionEventID
		prev = &tip
		if baseRevision == nil || *baseRevision != tip {
			return id.EventID{}, ErrPublishConflict
		}
	}
	payload := (&publication.RevisionPayload{
		Fallback: doc.Title, Document: doc.Encode(),
		BaseRevision: baseRevision, PrevRevision: prev,
	}).Encode()
	a, err := r.emitLocked(st, schema, payload)
	if err != nil {
		return id.EventID{}, err
	}
	// A fresh post in an OWNED public space nudges the projection push
	// instead of waiting for the sync loop's next pass: "Read post" on a
	// just-forwarded card otherwise answered "not in it yet" for the gap —
	// an honest sentence about a staleness we created ourselves. Async and
	// best-effort; the loop remains the guarantee.
	if r.ks.Spaces[tid].Owned && st.space.Policy().IsPublic() {
		// Settings are read from the raw blob HERE because r.mu is held for
		// this whole function and GetSettings takes it — the same deadlock
		// the share builder already stepped around once (PS-2).
		var cfg Settings
		if len(r.ks.Settings) > 0 {
			_ = json.Unmarshal(r.ks.Settings, &cfg)
		}
		if cfg.Relay != "" {
			go func() { _ = r.publishPublicProjection(cfg.Relay, tid) }()
		}
	}
	return a, nil
}

// ArchiveDocument emits the archive event and returns its id — a restore
// must reference exactly this event (ADR-014 invariant 6).
func (r *Runtime) ArchiveDocument(tid id.TerminalID, docID [16]byte) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	pub, ok := st.space.State.PublicationByID(docID)
	if !ok {
		return id.EventID{}, errors.New("node: unknown document")
	}
	payload := (&publication.LifecyclePayload{Fallback: pub.Title, DocumentID: docID}).Encode()
	return r.emitLocked(st, publication.SchemaArchived, payload)
}

func (r *Runtime) RestoreDocument(tid id.TerminalID, docID [16]byte, archiveEvent id.EventID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return errors.New("node: unknown space")
	}
	payload := (&publication.LifecyclePayload{
		Fallback: "restored", DocumentID: docID, ArchivedRevision: &archiveEvent,
	}).Encode()
	_, err := r.emitLocked(st, publication.SchemaRestored, payload)
	return err
}

// CommentOnDocument emits a threaded comment.
func (r *Runtime) CommentOnDocument(tid id.TerminalID, docID [16]byte, parent *[16]byte, text string) (id.EventID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.spaces[tid]
	if !ok {
		return id.EventID{}, errors.New("node: unknown space")
	}
	p := &publication.CommentPayload{Text: strings.TrimSpace(text), DocumentID: docID, ParentID: parent}
	if _, err := rand.Read(p.CommentID[:]); err != nil {
		return id.EventID{}, err
	}
	return r.emitLocked(st, publication.SchemaComment, p.Encode())
}

// emitLocked emits with r.mu already held (EmitBlock re-locks; this doesn't).
func (r *Runtime) emitLocked(st *spaceState, schema string, payload []byte) (id.EventID, error) {
	a, err := r.Self.Emit(st.space, schema, payload,
		r.Self.DefaultAuthorship(), nowUnix())
	if err != nil {
		return id.EventID{}, err
	}
	return a.ID, nil
}

// ---- Drafts (local, sealed — never in the log) ----

func draftName(tid id.TerminalID, docID string) string {
	return "draft-" + tid.Hex()[:16] + "-" + docID
}

// SaveDraft seals the JSON document as-is (opaque to the node).
func (r *Runtime) SaveDraft(tid id.TerminalID, docID string, body []byte) error {
	if len(docID) != 32 { // hex of 16 bytes
		return errors.New("node: draft needs a document id")
	}
	return r.root.SaveSealed(draftName(tid, docID), body)
}

func (r *Runtime) LoadDraft(tid id.TerminalID, docID string) ([]byte, error) {
	return r.root.LoadSealed(draftName(tid, docID))
}

func (r *Runtime) DeleteDraft(tid id.TerminalID, docID string) error {
	return r.root.DeleteSealed(draftName(tid, docID))
}

func (r *Runtime) ListDrafts(tid id.TerminalID) ([]string, error) {
	names, err := r.root.ListSealed("draft-" + tid.Hex()[:16] + "-")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strings.TrimPrefix(n, "draft-"+tid.Hex()[:16]+"-"))
	}
	return out, nil
}

// ---- API ----

func (a *APIServer) publicationJSON(tid id.TerminalID, docID [16]byte) (map[string]any, bool) {
	var out map[string]any
	// The whole projection — publication, member names, comments, resonance —
	// reads the replica, so it all belongs inside one locked scope.
	err := a.rt.withSpace(tid, func(st *spaceState) error {
		sp := st.space
		pub, ok := sp.State.PublicationByID(docID)
		if !ok {
			return errors.New("unknown publication")
		}
		me := a.rt.Principal.ID
		names := map[id.PrincipalID]string{me: a.rt.displayNameLocked()}
		for _, c := range sp.MemberCards(0) {
			if c.Name != "" {
				names[c.Principal] = c.Name
			}
		}
		comments := make([]map[string]any, 0, len(pub.Comments))
		for _, c := range pub.Comments {
			cm := map[string]any{
				"comment_id":  hex.EncodeToString(c.CommentID[:]),
				"text":        c.Text,
				"author":      c.Author.String(),
				"author_name": names[c.Author],
				"mine":        c.Author == me,
				"clock":       c.Clock,
				"created_at":  c.CreatedAt,
			}
			if c.ParentID != nil {
				cm["parent_comment_id"] = hex.EncodeToString(c.ParentID[:])
			}
			comments = append(comments, cm)
		}
		target := reducers.PubReactionTarget(docID)
		out = map[string]any{
			"document":          documentToJSON(pub.Document),
			"author":            pub.Author.String(),
			"revision_event_id": pub.RevisionEventID.Hex(),
			"clock":             pub.Clock,
			"archived":          pub.Archived,
			"comments":          comments,
			"reaction_target":   target.Hex(),
		}
		if res := a.projectResonance(sp, target, me, names); res != nil {
			out["resonance"] = res
		}
		return nil
	})
	// Unknown space and unknown publication are both "not here", as before.
	return out, err == nil
}

func (a *APIServer) handleListPublications(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	out := []map[string]any{}
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		for _, p := range st.space.State.Publications() {
			out = append(out, map[string]any{
				"document_id":       hex.EncodeToString(p.DocumentID[:]),
				"title":             p.Title,
				"summary":           p.Document.Summary,
				"kind":              p.Document.Kind,
				"cover":             p.Document.Cover,
				"tags":              p.Document.Tags,
				"author":            p.Author.String(),
				"clock":             p.Clock,
				"revision_event_id": p.RevisionEventID.Hex(),
				"comment_count":     len(p.Comments),
				// The feed marker needs the recipe to name the scene and to
				// say whether there is sound. It gets the whole thing rather
				// than a summary flag: a projection that invented its own
				// smaller shape here would be a second wire format to keep in
				// step, and the client already knows how to read this one.
				"atmosphere": atmosphereToJSON(p.Document.Atmosphere),
			})
			// A space-card's whole point is its target, and the list has no
			// blocks — so a catalog level would need one extra fetch per row
			// just to learn where each row goes. Carry it here instead
			// (CAT-0a). Absent for every other kind, so nothing else grows.
			if p.Document.Kind == "space" {
				out[len(out)-1]["link"] = spaceCardTarget(p.Document)
			}
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"publications": out})
}

// spaceCardTarget reads a space-card's share link: the first link block's
// text. One definition, so the list and the article cannot disagree.
func spaceCardTarget(doc *publication.Document) string {
	if doc == nil {
		return ""
	}
	for _, b := range doc.Blocks {
		if b.Type != "link" {
			continue
		}
		if tp, err := publication.ParseTextProps(b.RawProps); err == nil && tp.Text != "" {
			return tp.Text
		}
	}
	return ""
}

func (a *APIServer) docIDParam(r *http.Request) ([16]byte, error) {
	var out [16]byte
	b, err := hex.DecodeString(r.PathValue("doc"))
	if err != nil || len(b) != 16 {
		return out, errors.New("bad document id")
	}
	copy(out[:], b)
	return out, nil
}

func (a *APIServer) handleGetPublication(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	docID, err := a.docIDParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	j, ok := a.publicationJSON(tid, docID)
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("unknown document"))
		return
	}
	writeJSON(w, j)
}

func (a *APIServer) handlePublish(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Document documentJSON `json:"document"`
		Base     string       `json:"base_revision_event_id"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	doc, err := documentFromJSON(body.Document)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var base *id.EventID
	if body.Base != "" {
		b, err := hex.DecodeString(body.Base)
		if err != nil || len(b) != 32 {
			httpErr(w, http.StatusBadRequest, errors.New("bad base revision id"))
			return
		}
		var e id.EventID
		copy(e[:], b)
		base = &e
	}
	eid, err := a.rt.PublishDocument(tid, doc, base)
	if errors.Is(err, ErrPublishConflict) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "conflict": true})
		return
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// A successful publish consumes the draft.
	_ = a.rt.DeleteDraft(tid, hex.EncodeToString(doc.DocumentID[:]))
	writeJSON(w, map[string]any{
		"document_id":       hex.EncodeToString(doc.DocumentID[:]),
		"revision_event_id": eid.Hex(),
	})
}

func (a *APIServer) handleArchivePublication(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	docID, err := a.docIDParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	archEvent, err := a.rt.ArchiveDocument(tid, docID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// The archive event id is what a future restore must reference.
	writeJSON(w, map[string]string{"status": "archived", "archive_event_id": archEvent.Hex()})
}

func (a *APIServer) handleComment(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	docID, err := a.docIDParam(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Text   string `json:"text"`
		Parent string `json:"parent_comment_id"`
	}](r)
	if err != nil || strings.TrimSpace(body.Text) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("text required"))
		return
	}
	var parent *[16]byte
	if body.Parent != "" {
		b, err := hex.DecodeString(body.Parent)
		if err != nil || len(b) != 16 {
			httpErr(w, http.StatusBadRequest, errors.New("bad parent id"))
			return
		}
		var p [16]byte
		copy(p[:], b)
		parent = &p
	}
	eid, err := a.rt.CommentOnDocument(tid, docID, parent, body.Text)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

func (a *APIServer) handleDrafts(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ids, err := a.rt.ListDrafts(tid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
		out := []map[string]any{}
		for _, docID := range ids {
			if body, err := a.rt.LoadDraft(tid, docID); err == nil {
				var dj documentJSON
				title := ""
				if json.Unmarshal(body, &dj) == nil {
					title = dj.Title
				}
				out = append(out, map[string]any{"document_id": docID, "title": title})
			}
		}
		writeJSON(w, map[string]any{"drafts": out})
	case http.MethodPost:
		body, err := readBody[struct {
			DocumentID string          `json:"document_id"`
			Document   json.RawMessage `json:"document"`
		}](r)
		if err != nil || body.DocumentID == "" {
			httpErr(w, http.StatusBadRequest, errors.New("document_id and document required"))
			return
		}
		if err := a.rt.SaveDraft(tid, body.DocumentID, body.Document); err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]string{"status": "saved"})
	default:
		httpErr(w, http.StatusMethodNotAllowed, errors.New("unsupported method"))
	}
}

func (a *APIServer) handleGetDraft(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := a.rt.LoadDraft(tid, r.PathValue("doc"))
	if err != nil {
		httpErr(w, http.StatusNotFound, errors.New("no such draft"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// handleUploadAsset ingests a file from the composer and emits a
// block.attached.v1 carrier so every replica can index and decrypt the asset
// — WITHOUT a chat feed entry. Returns the public asset id for the document.
func (a *APIServer) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxMultipartBody)
	mr, err := r.MultipartReader()
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("multipart body required"))
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "metadata" {
		httpErr(w, http.StatusBadRequest, errors.New("first part must be metadata"))
		return
	}
	var meta struct {
		Filename  string `json:"filename"`
		MediaType string `json:"media_type"`
		Size      int64  `json:"size"`
	}
	if err := json.NewDecoder(part).Decode(&meta); err != nil || meta.Size <= 0 {
		httpErr(w, http.StatusBadRequest, errors.New("metadata with a positive size required"))
		return
	}
	part, err = mr.NextPart()
	if err != nil || part.FormName() != "file" {
		httpErr(w, http.StatusBadRequest, errors.New("second part must be the file"))
		return
	}
	role := "original"
	ref, err := a.rt.IngestAsset(part, meta.Size, assets.Metadata{MediaType: meta.MediaType, Role: role})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	block := &schemas.AttachedBlock{
		Filename: meta.Filename, MediaType: meta.MediaType, Original: ref,
	}
	payload, err := block.Encode()
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := a.rt.EmitBlock(tid, schemas.BlockAttached, payload); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{
		"asset_id":   ref.PublicIDHex(),
		"media_type": ref.MediaType,
		"size":       ref.Size,
		"filename":   meta.Filename,
	})
}

func (a *APIServer) handleDeleteDraft(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.DeleteDraft(tid, r.PathValue("doc")); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

func nowUnix() uint64 { return uint64(time.Now().Unix()) }
