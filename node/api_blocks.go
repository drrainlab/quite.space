// Block/asset/reaction API (Gate C). Multipart uploads are strictly
// streamed (r.MultipartReader, never ParseMultipartForm) behind
// MaxBytesReader; metadata is validated before the file part is read; asset
// downloads carry validated Content-Type + nosniff and stream only after
// full integrity verification (RetrieveBytes).
package node

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	Kind        string `json:"kind"` // visual|voice|audio|file|link|live_signal
	Size        int64  `json:"size"` // declared file size (streaming ingest)
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
		payload, err = (&schemas.VisualBlock{Caption: meta.Caption, Alt: meta.Alt,
			ThumbMIME: previewMIME, Thumb: preview, Original: ref}).Encode()
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
	eid, err := a.rt.EmitBlock(tid, schema, payload)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
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
	data, ref, err := a.rt.RetrieveAsset(tid, aid)
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
	// Content-Type strictly from the validated AssetRef; never sniffed.
	ct := ref.MediaType
	if _, _, err := mime.ParseMediaType(ct); err != nil {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Media plays inline (video/audio elements need Range + inline);
	// everything else downloads as an attachment.
	disp := "attachment"
	if strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") {
		disp = "inline"
	}
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType(disp, map[string]string{"filename": schemas.NormalizeFilename("asset-" + aid[:min(12, len(aid))])}))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	// ServeContent adds Range/206 support — video seeking works.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
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
	Clock      uint64 `json:"clock"`
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
	Kept      bool `json:"kept,omitempty"`
	KeepCount int  `json:"keep_count,omitempty"`

	Text string `json:"text,omitempty"`
	// ReplyTo is a genuine reply edge (message.text.v1 only — the revision
	// schema reuses the same wire field for a different meaning).
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
		me := a.rt.Principal.ID
		// Resolve author principals to human display names (self-declared
		// claims from member manifests) — the honest projection that keeps
		// principal:hex out of the human path.
		names := map[id.PrincipalID]string{me: a.rt.displayNameLocked()}
		for _, c := range st.space.MemberCards(0) {
			if c.Name != "" {
				names[c.Principal] = c.Name
			}
		}
		entries := st.space.State.Entries()
		out = make([]entryResp, 0, len(entries))
		for i := range entries {
			resp := a.projectEntry(tid, st.space, &entries[i], me, names)
			resp.Kept, _ = st.space.State.KeepState(entries[i].ID, me)
			resp.KeepCount = st.space.State.KeepCount(entries[i].ID)
			out = append(out, resp)
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, out)
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
		// Only when something other than a person signed it: a model name
		// beside a human's words would be a claim about them.
		if e.ProducedBy != signal.AuthorshipHuman {
			resp.Model = e.Content.Text.Model
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
