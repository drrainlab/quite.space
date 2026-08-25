// Block/asset/reaction API (Gate C). Multipart uploads are strictly
// streamed (r.MultipartReader, never ParseMultipartForm) behind
// MaxBytesReader; metadata is validated before the file part is read; asset
// downloads carry validated Content-Type + nosniff and stream only after
// full integrity verification (RetrieveBytes).
package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
)

// MaxMultipartBody bounds an upload request before any reading happens.
const MaxMultipartBody = assets.MaxAssetSize + (1 << 20)

type blockMeta struct {
	Kind string `json:"kind"` // visual|voice|audio|file|link|live_signal
	Size int64  `json:"size"` // declared file size (streaming ingest)
	// ReplyTo (hex event id) makes a picture an answer. Visual only for now
	// — the one kind a live reply bar was measured sending.
	ReplyTo     string `json:"reply_to"`
	MediaType   string `json:"media_type"`
	Caption     string `json:"caption"`
	Alt         string `json:"alt"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	BPM         uint64 `json:"bpm"`
	Loop        bool   `json:"loop"`
	DurationMS  uint64 `json:"duration_ms"`
	Language    string `json:"language"`
	Transcript  string `json:"transcript"`
	Filename    string `json:"filename"`
	URL         string `json:"url"`
	Descr       string `json:"description"`
	Waveform    string `json:"waveform"` // base64, ≤64 buckets
	PreviewMIME string `json:"preview_mime"`
	Width       uint64 `json:"width"`
	Height      uint64 `json:"height"`
	ChunkKiB    int    `json:"chunk_kib"` // 4|16|64, 0=default
	// live signal fields
	Preset   string            `json:"preset"`
	Seed     uint64            `json:"seed"`
	Params   map[string]uint64 `json:"params"`
	Fallback string            `json:"fallback"`
}

func (a *APIServer) handlePostBlock(w http.ResponseWriter, r *http.Request) {
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

	// Part 1: metadata — validated before any file bytes are read.
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "metadata" {
		httpErr(w, http.StatusBadRequest, errors.New("first part must be metadata"))
		return
	}
	var meta blockMeta
	if err := json.NewDecoder(part).Decode(&meta); err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("metadata does not parse"))
		return
	}
	var waveform []byte
	if meta.Waveform != "" {
		waveform, err = base64.StdEncoding.DecodeString(meta.Waveform)
		if err != nil || len(waveform) > schemas.MaxWaveformBuckets {
			httpErr(w, http.StatusBadRequest, errors.New("bad waveform"))
			return
		}
	}

	// Kinds without assets finish here-ish; asset kinds validate early.
	needsFile := false
	switch meta.Kind {
	case "visual", "video", "voice", "audio", "file":
		needsFile = true
		if meta.Size <= 0 || meta.Size > assets.MaxAssetSize {
			httpErr(w, http.StatusBadRequest, errors.New("declared size missing or over limit"))
			return
		}
		if (meta.Kind == "visual" || meta.Kind == "video") && meta.Alt == "" {
			httpErr(w, http.StatusBadRequest, errors.New("visual blocks require alt text"))
			return
		}
	case "link", "live_signal":
	default:
		httpErr(w, http.StatusBadRequest, fmt.Errorf("unknown block kind %q", meta.Kind))
		return
	}

	// Part 2 (optional): preview.
	var preview []byte
	var previewMIME string
	part, err = mr.NextPart()
	for err == nil && part.FormName() == "preview" {
		previewMIME = meta.PreviewMIME
		if !schemas.AllowedPreviewMIME(previewMIME) {
			httpErr(w, http.StatusBadRequest, errors.New("preview mime not allowed"))
			return
		}
		buf := make([]byte, schemas.MaxInlinePreview+1)
		n, _ := readFull(part, buf)
		if n > schemas.MaxInlinePreview {
			httpErr(w, http.StatusBadRequest, errors.New("preview too large"))
			return
		}
		preview = buf[:n]
		part, err = mr.NextPart()
	}

	// Part 3: the file, streamed straight into the encrypted store.
	var ref *schemas.AssetRef
	if needsFile {
		if err != nil || part.FormName() != "file" {
			httpErr(w, http.StatusBadRequest, errors.New("file part required"))
			return
		}
		ref, err = a.rt.IngestAsset(part, meta.Size, assets.Metadata{
			MediaType: meta.MediaType, Role: "original",
			Width: meta.Width, Height: meta.Height, DurationMS: meta.DurationMS,
			ChunkSize: meta.ChunkKiB * 1024,
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
	}

	// Build and emit the block. On emit failure the ingested blobs stay
	// orphaned (unindexed; future GC) — never a DeleteBlob.
	var schema string
	var payload []byte
	switch meta.Kind {
	case "visual":
		schema = schemas.BlockVisual
		vb := &schemas.VisualBlock{Caption: meta.Caption, Alt: meta.Alt,
			ThumbMIME: previewMIME, Thumb: preview, Original: ref}
		if meta.ReplyTo != "" {
			raw, derr := hex.DecodeString(meta.ReplyTo)
			var eid id.EventID
			if derr != nil || len(raw) != len(eid) {
				httpErr(w, http.StatusBadRequest, errors.New("bad reply_to"))
				return
			}
			copy(eid[:], raw)
			vb.ReplyTo = &eid
		}
		payload, err = vb.Encode()
	case "video":
		schema = schemas.BlockVideo
		payload, err = (&schemas.VideoBlock{Caption: meta.Caption, Alt: meta.Alt,
			ThumbMIME: previewMIME, Thumb: preview, DurationMS: meta.DurationMS,
			Width: meta.Width, Height: meta.Height, Original: ref}).Encode()
	case "voice":
		schema = schemas.BlockVoice
		payload, err = (&schemas.VoiceBlock{DurationMS: meta.DurationMS, Waveform: waveform,
			Transcript: meta.Transcript, Language: meta.Language, Original: ref}).Encode()
	case "audio":
		schema = schemas.BlockAudio
		payload, err = (&schemas.AudioBlock{Title: meta.Title, Artist: meta.Artist,
			BPM: meta.BPM, Loop: meta.Loop, DurationMS: meta.DurationMS,
			Waveform: waveform, Original: ref}).Encode()
	case "file":
		schema = schemas.BlockFile
		payload, err = (&schemas.FileBlock{Filename: meta.Filename,
			MediaType: meta.MediaType, Size: uint64(meta.Size), Original: ref}).Encode()
	case "link":
		schema = schemas.BlockLink
		payload, err = (&schemas.LinkBlock{URL: meta.URL, Title: meta.Title,
			Description: meta.Descr, ThumbMIME: previewMIME, Thumb: preview}).Encode()
	case "live_signal":
		schema = schemas.BlockLiveSignal
		var params []schemas.SignalParam
		for name, v := range meta.Params {
			params = append(params, schemas.SignalParam{Name: name, Value: v})
		}
		sortParams(params)
		payload, err = (&schemas.LiveSignalBlock{FallbackText: meta.Fallback,
			Engine: schemas.LiveSignalEngineV1, Preset: meta.Preset,
			Seed: meta.Seed, Params: params}).Encode()
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	// Armed BEFORE the emit so the push that carries the new frame finds
	// the bytes already waiting — armed after, a tick landing in between
	// would ship the frame alone and the chunks would never ride.
	a.rt.RideAhead(tid, ref)
	eid, err := a.rt.EmitBlock(tid, schema, payload)
	if err != nil {
		a.rt.DisarmRideAhead(tid, ref)
		httpErr(w, http.StatusForbidden, err)
		return
	}
	a.rt.kickRelaySync()
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

func sortParams(ps []schemas.SignalParam) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j-1].Name > ps[j].Name; j-- {
			ps[j-1], ps[j] = ps[j], ps[j-1]
		}
	}
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ---- Asset download / fetch ----

// assetID returns the hex asset id from the path, accepting both the legacy
// 16-byte handle and the 32-byte V2 content digest.
func (a *APIServer) assetID(r *http.Request) (string, error) {
	s := r.PathValue("asset")
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 16 && len(b) != 32) {
		return "", errors.New("bad asset id")
	}
	return s, nil
}

func (a *APIServer) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	aid, err := a.assetID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	content, ref, err := a.rt.OpenAsset(tid, aid)
	if err != nil {
		if ref == nil {
			httpErr(w, http.StatusNotFound, err)
			return
		}
		st, _ := a.rt.AssetStatus(tid, aid)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{
			"state": st.State, "reason": st.Reason,
			"missing": st.Missing, "total": st.Total, "size": st.SizeBytes,
		})
		return
	}
	serveAssetContent(w, r, ref, aid, content)
}

// serveAssetBytes writes verified asset bytes with the headers every asset
// route shares: Content-Type strictly from the validated AssetRef (never
// sniffed), nosniff, inline for media, Range/206 via ServeContent. The
// space route and the preview route (PS-3) serve identically because they
// serve HERE.
func serveAssetBytes(w http.ResponseWriter, r *http.Request, ref *schemas.AssetRef, aid string, data []byte) {
	serveAssetContent(w, r, ref, aid, bytes.NewReader(data))
}

// serveAssetContent is the streaming form: the space route hands it a lazy
// chunk reader so a Range request decrypts only the chunks it touches —
// a player buffering a 380 MB video used to cost one full-file assembly
// PER RANGE REQUEST, which played as "downloaded but freezes".
func serveAssetContent(w http.ResponseWriter, r *http.Request, ref *schemas.AssetRef, aid string, content io.ReadSeeker) {
	// The declared type is parsed rather than trusted whole: parameters
	// ("image/png; charset=…") must not smuggle a second opinion past the
	// allowlist below, so the BASE type is what both the header and the
	// disposition decision are taken from.
	ct, _, err := mime.ParseMediaType(ref.MediaType)
	if err != nil {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Asset bytes are somebody else's data, and the UI opens them as
	// top-level documents (image zoom is a window.open on this URL, with
	// the session token in the query string). A document opened here must
	// therefore be inert: `sandbox` without allow-scripts blocks script
	// execution outright, which is what stops an asset declared
	// image/svg+xml — an SVG is an active document, unlike every other
	// image format — from running script in this origin and reading that
	// token out of location.search. allow-downloads keeps file attachments
	// working; nothing else is granted, so the document has no origin, no
	// storage and no scripting. nosniff alone never covered this: it stops
	// type GUESSING, and the type here was declared, not guessed.
	//
	// Defence in depth, not the fix: schemas.AllowedInlineMIME below is what
	// keeps the declaration honest in the first place. This header is what
	// holds if that allowlist is ever widened by somebody who has not read
	// this comment.
	w.Header().Set("Content-Security-Policy",
		"sandbox allow-downloads; default-src 'none'; script-src 'none'; object-src 'none'")
	// Rendering in place is EARNED by an allowlist, never granted by a
	// prefix. "image/" used to be enough, and image/svg+xml matches it —
	// which handed an active document the inline disposition. Anything not
	// on the list is still served in full, as a download.
	disp := "attachment"
	if schemas.AllowedInlineMIME(ct) {
		disp = "inline"
	}
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType(disp, map[string]string{"filename": schemas.NormalizeFilename("asset-" + aid[:min(12, len(aid))])}))
	// Content-addressed bytes are immutable — unless the caller already
	// declared a stricter policy (the preview route sets no-store BEFORE
	// serving: transient media must not enter a durable browser layer, and
	// clobbering that header here silently re-cached it for a year).
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, "", time.Time{}, content)
}

func (a *APIServer) handleFetchAsset(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	aid, err := a.assetID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.RequestAsset(tid, aid); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	st, _ := a.rt.AssetStatus(tid, aid)
	// The reason travels with the state: without it the client can only
	// spin, and a spinner that never resolves is the interface lying by
	// omission.
	writeJSON(w, map[string]any{"state": st.State, "missing": st.Missing,
		"total": st.Total, "reason": st.Reason})
}

// ---- Entries feed ----

type assetResp struct {
	ID         string `json:"id"`
	MediaType  string `json:"media_type"`
	Size       uint64 `json:"size"`
	DurationMS uint64 `json:"duration_ms,omitempty"`
	Width      uint64 `json:"width,omitempty"`
	Height     uint64 `json:"height,omitempty"`
	State      string `json:"state"`
	Missing    int    `json:"missing"`
	Total      int    `json:"total"`
	Reason     string `json:"reason,omitempty"`
}

// sharedResp is a quotation's provenance, all of it self-declared.
type sharedResp struct {
	Author     string `json:"author,omitempty"`
	Source     string `json:"source,omitempty"`
	OriginalAt uint64 `json:"original_at,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Quote      string `json:"quote"`
	// Card is the post card when this share forwards a publication (PS).
	// The client renders it from THESE fields, never by parsing the
	// composed key-1 prose back apart.
	Card *postCardResp `json:"card,omitempty"`
}

// postCardResp mirrors schemas.SharedPublication for the client.
// (cardResp was already taken — it is a member card.)
type postCardResp struct {
	Title     string `json:"title,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// quotedLines pulls the quotation back out of the composed text: the lines
// the node prefixed with "> ", minus what the node ALSO wrote around them.
// Both removals match the composer exactly rather than trusting position:
// the header line is dropped only when it equals what quoteHeadline would
// have produced (it used to be "line 0", which silently ate the first line
// of any quote whose header happened to be empty), and the trailing "…"
// marker is dropped when the origin says truncated — the client appends
// its own, and collecting this one rendered a double ellipsis.
func quotedLines(o *schemas.ShareOrigin, text string) string {
	head := quoteHeadline(o)
	lines := strings.Split(text, "\n")
	var out []string
	for i, line := range lines {
		if !strings.HasPrefix(line, "> ") {
			continue
		}
		body := strings.TrimPrefix(line, "> ")
		if i == 0 && head != "" && body == head {
			continue
		}
		if o.Truncated && i == len(lines)-1 && body == "…" {
			continue
		}
		out = append(out, body)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

type entryResp struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	// AuthorFull is the full principal hex — needed to address this person
	// back (a reply stages them as a mention). The short Author form stays
	// for display and Protocol view.
	AuthorFull string `json:"author_full"`
	AuthorName string `json:"author_name"`
	Mine       bool   `json:"mine"`
	ProducedBy string `json:"produced_by"`
	// Model is the signer's claim about what produced a machine-authored
	// message. Shown ONLY alongside a non-human authorship mark, so it can
	// never read as a fact about a person's own words.
	Model string `json:"model,omitempty"`
	// Shared is the quotation's provenance. Every field is a CLAIM by the
	// sender — the original's signature does not travel — and the renderer
	// says so beside it (SHARE-1).
	Shared *sharedResp `json:"shared,omitempty"`
	// External is who the GATEWAY says a letter came from (TR-0, key 7).
	// Set only under imported authorship: the payload can carry this
	// structure from anybody, the signature cannot, so trusting the
	// payload alone would hand every member a stencil for forging mail.
	External *externalResp `json:"external,omitempty"`
	Clock    uint64        `json:"clock"`
	// CreatedAt is the AUTHOR's wall clock from the signed envelope —
	// advisory, it can lie. Display only; never trust it for local policy.
	CreatedAt uint64 `json:"created_at,omitempty"`
	Kind      string `json:"kind"`
	Fallback  string `json:"fallback"`
	// Resonance: the reaction aggregate (own/mine are viewer-relative,
	// resolved at this layer only).
	Resonance *resonanceResp `json:"resonance,omitempty"`
	// Kept: viewer-relative "kept by me" affordance (API layer only);
	// KeepCount is how many members keep this entry.
	Kept bool `json:"kept,omitempty"`

	// WaitingFor says this event has NOT gone out on the path available, and
	// why. Empty is the ordinary case and means nothing is known to be wrong
	// — never "it arrived", which is a claim only the delivery ladder makes.
	//
	// The sizes ride along so the sentence can be concrete: "this is 2.4 KB
	// and the radio carries 2.5" tells somebody what to do, where "too large"
	// only tells them to be annoyed.
	WaitingFor     string `json:"waiting_for,omitempty"`
	WaitingSize    int    `json:"waiting_size,omitempty"`
	WaitingCeiling int    `json:"waiting_ceiling,omitempty"`
	KeepCount      int    `json:"keep_count,omitempty"`

	Text string `json:"text,omitempty"`
	// ReplyTo is a genuine reply edge (message.text.v1 and block.visual.v1 —
	// the revision schema reuses the same wire field for a different meaning).
	ReplyTo string `json:"reply_to,omitempty"`
	// Mentions is the author's signed claim of who is addressed (full hex),
	// with names resolved here so the client renders without a lookup.
	// MentionsMe is viewer-relative, resolved at this layer only — the same
	// pattern as Mine and Kept.
	Mentions     []string `json:"mentions,omitempty"`
	MentionNames []string `json:"mention_names,omitempty"`
	MentionsMe   bool     `json:"mentions_me,omitempty"`
	Revised      bool     `json:"revised,omitempty"`
	Caption      string   `json:"caption,omitempty"`
	Alt          string   `json:"alt,omitempty"`
	ThumbB64     string   `json:"thumb_b64,omitempty"`
	ThumbMIME    string   `json:"thumb_mime,omitempty"`

	Title      string `json:"title,omitempty"`
	Artist     string `json:"artist,omitempty"`
	BPM        uint64 `json:"bpm,omitempty"`
	Loop       bool   `json:"loop,omitempty"`
	DurationMS uint64 `json:"duration_ms,omitempty"`
	WaveB64    string `json:"waveform_b64,omitempty"`
	Transcript string `json:"transcript,omitempty"`

	Filename string `json:"filename,omitempty"`
	URL      string `json:"url,omitempty"`
	Descr    string `json:"description,omitempty"`

	Preset string            `json:"preset,omitempty"`
	Seed   uint64            `json:"seed,omitempty"`
	Params map[string]uint64 `json:"params,omitempty"`
	Schema string            `json:"schema,omitempty"` // for unknown kinds

	Asset *assetResp `json:"asset,omitempty"`
}

func (a *APIServer) handleEntries(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var out []entryResp
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		me := a.rt.PrincipalID
		// Resolve author principals to human display names (self-declared
		// claims from member manifests) — the honest projection that keeps
		// principal:hex out of the human path.
		// TWO maps, because a principal is no longer one voice. The local
		// assistant is an Agent Terminal controlled by YOUR principal
		// (AI-0), so a single map keyed by principal let its card rename
		// you — a message you forwarded read "Quite AI says this came
		// from alice". Which name is right depends on who WROTE the event,
		// not only on whose principal signs for it.
		names := map[id.PrincipalID]string{me: a.rt.displayNameLocked()}
		machineNames := map[id.PrincipalID]string{}
		for _, c := range st.space.MemberCards(0) {
			if c.Name == "" {
				continue
			}
			if c.Agency == "ai_agent" || c.Kind == "agent" || c.Kind == "bot" {
				machineNames[c.Principal] = c.Name
				continue
			}
			names[c.Principal] = c.Name
		}
		entries := st.space.State.Entries()
		out = make([]entryResp, 0, len(entries))
		for i := range entries {
			who := names
			if entries[i].ProducedBy != signal.AuthorshipHuman {
				if n, ok := machineNames[entries[i].Author]; ok {
					who = map[id.PrincipalID]string{entries[i].Author: n}
				}
			}
			resp := a.projectEntry(tid, st.space, &entries[i], me, who)
			resp.Kept, _ = st.space.State.KeepState(entries[i].ID, me)
			resp.KeepCount = st.space.State.KeepCount(entries[i].ID)
			out = append(out, resp)
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	// NOT MODIFIED IS AN ANSWER — and on this endpoint it is the usual
	// one. The interface polls every two seconds and this handler used to
	// ship the WHOLE room each time, every inline preview re-encoded to
	// base64: a network-panel screenshot measured 24 MB over one sitting
	// for a 244-event room, all of it through the webview bridge a phone
	// has to parse. The tag is a hash of the exact bytes we would send —
	// not of the log head, because an asset can finish fetching without
	// the log growing, and a length-based tag would freeze the screen at
	// "fetching…" forever. Serialization still happens each poll; the
	// transfer and the client-side parse — the part that hurts — do not.
	body, err := json.Marshal(out)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	sum := sha256.Sum256(body)
	tag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", tag)
	// no-store BESIDE the ETag, deliberately: the validator saves the
	// transfer for our own hand-rolled conditional request, no-store
	// forbids the HTTP cache from ever answering in the node's place —
	// a heuristically-cached feed is frozen while every poll "succeeds".
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// projectEntry renders one entry. It reads the replica and the asset index,
// so it MUST be called with r.mu held — i.e. from inside a withSpace scope,
// which is where sp legitimately comes from.
func (a *APIServer) projectEntry(tid id.TerminalID, sp *terminals.Space,
	e *reducers.Entry, me id.PrincipalID, names map[id.PrincipalID]string) entryResp {
	resp := entryResp{
		ID: e.ID.Hex(), Author: e.Author.String(), AuthorFull: e.Author.Hex(),
		AuthorName: names[e.Author],
		// "mine" now asks TWO things, because the assistant is controlled by
		// this principal but is not this person: my principal controls the
		// signer, AND a human wrote it. Those were never the same statement
		// — they only looked identical while every terminal was a person's
		// own (AI-0).
		Mine:       e.Author == me && e.ProducedBy == signal.AuthorshipHuman,
		ProducedBy: e.ProducedBy.String(),
		Clock:      e.Clock, CreatedAt: e.CreatedAt, Kind: string(e.Kind),
	}
	// Only for one's OWN events, because a waiting state is a statement
	// about this device's outbound. Telling somebody that SOMEBODY ELSE's
	// message is stuck would be a claim about a machine we cannot see.
	//
	// Lock order: r.mu (already held here) → r.tooLarge.mu, never the
	// reverse — the same rule the pass registry declares, written down for
	// the same reason.
	if resp.Mine {
		if w, ok := a.rt.WaitingForAWiderPath(e.ID); ok {
			resp.WaitingFor = "wider_path"
			resp.WaitingSize, resp.WaitingCeiling = w.Size, w.Ceiling
		}
	}
	if sp != nil {
		resp.Resonance = a.projectResonance(sp, e.ID, me, names)
	}

	attachAsset := func(ref *schemas.AssetRef) {
		// assetStatusLocked, not AssetStatus: every caller of projectEntry
		// already holds r.mu (see projectEntry's doc comment), and the
		// exported form would take it a second time and self-deadlock.
		st, err := a.rt.assetStatusLocked(AssetKey{Space: tid, Asset: ref.PublicIDHex()})
		ar := &assetResp{
			ID: ref.PublicIDHex(), MediaType: ref.MediaType,
			Size: ref.Size, DurationMS: ref.DurationMS,
			Width: ref.Width, Height: ref.Height,
		}
		if err == nil {
			ar.State, ar.Missing, ar.Total, ar.Reason =
				string(st.State), st.Missing, st.Total, string(st.Reason)
		}
		resp.Asset = ar
	}
	switch {
	case e.Content.Text != nil:
		resp.Text = e.Content.Text.Text
		if o := e.Content.Text.Origin; o != nil {
			resp.Shared = &sharedResp{
				Author: o.AuthorLabel, Source: o.SourceLabel,
				OriginalAt: o.OriginalAt, Truncated: o.Truncated,
				// The quoted lines are carried in the composed text with a
				// "> " marker, so a client that ignores this object still
				// shows the quotation. This one renders from HERE.
				Quote: quotedLines(o, e.Content.Text.Text),
			}
			if c := e.Content.Text.Card; c != nil {
				resp.Shared.Card = &postCardResp{
					Title: c.Title, Summary: c.Summary, Reference: c.Reference,
				}
			}
		}
		// Only when something other than a person signed it: a model name
		// beside a human's words would be a claim about them.
		if e.ProducedBy != signal.AuthorshipHuman {
			resp.Model = e.Content.Text.Model
		}
		// Same rule, one wave later: foreign provenance rides the envelope's
		// authorship, never the payload's word for itself.
		if x := e.Content.Text.External; x != nil && e.ProducedBy == signal.AuthorshipImported {
			resp.External = &externalResp{
				Connector: x.ConnectorKind, Address: x.Address,
				LossFlags: x.LossFlags,
			}
		}
		resp.Revised = e.Content.Text.Revised
		resp.Fallback = e.Content.Text.Text
		if rt := e.Content.Text.ReplyTo; rt != nil {
			resp.ReplyTo = rt.Hex()
		}
		for _, m := range e.Content.Text.Mentions {
			resp.Mentions = append(resp.Mentions, m.Hex())
			resp.MentionNames = append(resp.MentionNames, names[m])
			if m == me {
				resp.MentionsMe = true
			}
		}
	case e.Content.Visual != nil:
		v := e.Content.Visual
		resp.Caption, resp.Alt, resp.Fallback = v.Caption, v.Alt, v.Fallback()
		if rt := v.ReplyTo; rt != nil {
			resp.ReplyTo = rt.Hex()
		}
		if len(v.Thumb) > 0 {
			resp.ThumbB64 = base64.StdEncoding.EncodeToString(v.Thumb)
			resp.ThumbMIME = v.ThumbMIME
		}
		attachAsset(v.Original)
	case e.Content.Video != nil:
		v := e.Content.Video
		resp.Caption, resp.Alt, resp.Fallback = v.Caption, v.Alt, v.Fallback()
		resp.DurationMS = v.DurationMS
		if len(v.Thumb) > 0 {
			resp.ThumbB64 = base64.StdEncoding.EncodeToString(v.Thumb)
			resp.ThumbMIME = v.ThumbMIME
		}
		attachAsset(v.Original)
	case e.Content.Voice != nil:
		v := e.Content.Voice
		resp.DurationMS, resp.Transcript, resp.Fallback = v.DurationMS, v.Transcript, v.Fallback()
		if len(v.Waveform) > 0 {
			resp.WaveB64 = base64.StdEncoding.EncodeToString(v.Waveform)
		}
		attachAsset(v.Original)
	case e.Content.Audio != nil:
		v := e.Content.Audio
		resp.Title, resp.Artist, resp.BPM, resp.Loop = v.Title, v.Artist, v.BPM, v.Loop
		resp.DurationMS, resp.Fallback = v.DurationMS, v.Fallback()
		if len(v.Waveform) > 0 {
			resp.WaveB64 = base64.StdEncoding.EncodeToString(v.Waveform)
		}
		attachAsset(v.Original)
	case e.Content.File != nil:
		v := e.Content.File
		resp.Filename, resp.Fallback = v.Filename, v.Fallback()
		attachAsset(v.Original)
	case e.Content.Link != nil:
		v := e.Content.Link
		resp.URL, resp.Title, resp.Descr, resp.Fallback = v.URL, v.Title, v.Description, v.Fallback()
		if len(v.Thumb) > 0 {
			resp.ThumbB64 = base64.StdEncoding.EncodeToString(v.Thumb)
			resp.ThumbMIME = v.ThumbMIME
		}
	case e.Content.Signal != nil:
		v := e.Content.Signal
		resp.Preset, resp.Seed, resp.Fallback = v.Preset, v.Seed, v.FallbackText
		resp.Params = map[string]uint64{}
		for _, p := range v.Params {
			resp.Params[p.Name] = p.Value
		}
	case e.Content.Unknown != nil:
		resp.Schema = e.Content.Unknown.Schema
		resp.Fallback = e.Content.Unknown.Fallback
	}
	return resp
}

var _ = time.Now
