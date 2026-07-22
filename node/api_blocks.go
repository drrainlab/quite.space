// Block/asset/reaction API (Gate C). Multipart uploads are strictly
// streamed (r.MultipartReader, never ParseMultipartForm) behind
// MaxBytesReader; metadata is validated before the file part is read; asset
// downloads carry validated Content-Type + nosniff and stream only after
// full integrity verification (RetrieveBytes).
package node

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/kernel/reducers"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
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
	case "visual", "voice", "audio", "file":
		needsFile = true
		if meta.Size <= 0 || meta.Size > assets.MaxAssetSize {
			httpErr(w, http.StatusBadRequest, errors.New("declared size missing or over limit"))
			return
		}
		if meta.Kind == "visual" && meta.Alt == "" {
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

func (a *APIServer) assetID(r *http.Request) ([16]byte, error) {
	var out [16]byte
	b, err := hex.DecodeString(r.PathValue("asset"))
	if err != nil || len(b) != 16 {
		return out, errors.New("bad asset id")
	}
	copy(out[:], b)
	return out, nil
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
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": schemas.NormalizeFilename("asset-" + hex.EncodeToString(aid[:6]))}))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(data)
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
	writeJSON(w, map[string]any{"state": st.State, "missing": st.Missing, "total": st.Total})
}

func (a *APIServer) handleReaction(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Target string `json:"target"`
		Emoji  string `json:"emoji"`
		Active bool   `json:"active"`
	}](r)
	if err != nil || body.Target == "" {
		httpErr(w, http.StatusBadRequest, errors.New("target and emoji required"))
		return
	}
	tb, err := hex.DecodeString(body.Target)
	if err != nil || len(tb) != id.Size {
		httpErr(w, http.StatusBadRequest, errors.New("bad target id"))
		return
	}
	var target id.EventID
	copy(target[:], tb)
	if err := a.rt.ReactionSet(tid, target, body.Emoji, body.Active); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
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

type reactionResp struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine"`
}

type entryResp struct {
	ID         string         `json:"id"`
	Author     string         `json:"author"`
	AuthorName string         `json:"author_name"`
	Mine       bool           `json:"mine"`
	ProducedBy string         `json:"produced_by"`
	Clock      uint64         `json:"clock"`
	Kind       string         `json:"kind"`
	Fallback   string         `json:"fallback"`
	Reactions  []reactionResp `json:"reactions,omitempty"`

	Text      string `json:"text,omitempty"`
	Revised   bool   `json:"revised,omitempty"`
	Caption   string `json:"caption,omitempty"`
	Alt       string `json:"alt,omitempty"`
	ThumbB64  string `json:"thumb_b64,omitempty"`
	ThumbMIME string `json:"thumb_mime,omitempty"`

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
	sp, ok := a.rt.Space(tid)
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("unknown space"))
		return
	}
	me := a.rt.Principal.ID
	// Resolve author principals to human display names (self-declared
	// claims from member manifests) — the honest projection that keeps
	// principal:hex out of the human path.
	names := map[id.PrincipalID]string{me: a.rt.DisplayName()}
	for _, c := range sp.MemberCards(0) {
		if c.Name != "" {
			names[c.Principal] = c.Name
		}
	}
	entries := sp.State.Entries()
	out := make([]entryResp, 0, len(entries))
	for i := range entries {
		out = append(out, a.projectEntry(tid, &entries[i], me, names))
	}
	writeJSON(w, out)
}

func (a *APIServer) projectEntry(tid id.TerminalID, e *reducers.Entry, me id.PrincipalID,
	names map[id.PrincipalID]string) entryResp {
	resp := entryResp{
		ID: e.ID.Hex(), Author: e.Author.String(), AuthorName: names[e.Author],
		Mine: e.Author == me, ProducedBy: e.ProducedBy.String(),
		Clock: e.Clock, Kind: string(e.Kind),
	}
	for emoji, ps := range e.Reactions {
		rr := reactionResp{Emoji: emoji, Count: len(ps)}
		for _, p := range ps {
			if p == me {
				rr.Mine = true // viewer-relative projection, API-layer only
			}
		}
		resp.Reactions = append(resp.Reactions, rr)
	}
	sortReactions(resp.Reactions)

	attachAsset := func(ref *schemas.AssetRef) {
		st, err := a.rt.AssetStatus(tid, ref.AssetID)
		ar := &assetResp{
			ID: hex.EncodeToString(ref.AssetID[:]), MediaType: ref.MediaType,
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
		resp.Revised = e.Content.Text.Revised
		resp.Fallback = e.Content.Text.Text
	case e.Content.Visual != nil:
		v := e.Content.Visual
		resp.Caption, resp.Alt, resp.Fallback = v.Caption, v.Alt, v.Fallback()
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

func sortReactions(rs []reactionResp) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].Emoji > rs[j].Emoji; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}

var _ = time.Now
