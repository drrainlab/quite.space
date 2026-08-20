// The seven v1 block types. Every Encode writes fallback_text at key 1
// (universal contract) and gates the result on actually-encoded CBOR size.
package schemas

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
)

// ---- VisualBlock (block.visual.v1) ----
// keys: 1 fallback, 2 caption, 3 alt, 4 thumb_mime, 5 thumb, 6 original,
// 7 reply_to (event id; appended 2026-08 — a picture can answer someone.
// Old decoders skip unknown keys, so they show the image without the quote)

type VisualBlock struct {
	Caption   string
	Alt       string // required: honesty for non-visual terminals
	ThumbMIME string
	Thumb     []byte // inline preview, jpeg/png/webp only
	Original  *AssetRef
	// ReplyTo is a genuine reply edge, the same meaning as the text
	// message's — a photo sent as an answer, not beside one.
	ReplyTo *id.EventID
}

func (b *VisualBlock) Fallback() string {
	if b.Alt != "" {
		return b.Alt
	}
	return "image"
}

func (b *VisualBlock) Encode() ([]byte, error) {
	if b.Alt == "" || len(b.Alt) > MaxAltLen {
		return nil, errors.New("schemas: visual block requires alt text (≤500)")
	}
	if len(b.Caption) > MaxCaptionLen {
		return nil, errors.New("schemas: caption too long")
	}
	if err := validatePreview(b.ThumbMIME, b.Thumb); err != nil {
		return nil, err
	}
	if b.Original == nil {
		return nil, errors.New("schemas: visual block requires original asset")
	}
	orig, err := b.Original.encode()
	if err != nil {
		return nil, err
	}
	n := 3 // fallback, alt, original
	if b.Caption != "" {
		n++
	}
	if len(b.Thumb) > 0 {
		n += 2
	}
	if b.ReplyTo != nil {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, clip(b.Fallback(), MaxFallbackLen))
	if b.Caption != "" {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendText(buf, b.Caption)
	}
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendText(buf, b.Alt)
	if len(b.Thumb) > 0 {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendText(buf, b.ThumbMIME)
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendBytes(buf, b.Thumb)
	}
	buf = codec.AppendUint(buf, 6)
	buf = append(buf, orig...)
	if b.ReplyTo != nil {
		buf = codec.AppendUint(buf, 7)
		buf = codec.AppendBytes(buf, b.ReplyTo[:])
	}
	return finishBlock(buf)
}

func DecodeVisualBlock(p []byte) (*VisualBlock, error) {
	b := &VisualBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.Caption, er = d.ReadText()
		case 3:
			b.Alt, er = d.ReadText()
		case 4:
			b.ThumbMIME, er = d.ReadText()
		case 5:
			var v []byte
			v, er = d.ReadBytes()
			b.Thumb = append([]byte(nil), v...)
		case 6:
			b.Original, er = decodeAssetRef(d)
		case 7:
			var v []byte
			if v, er = d.ReadBytes(); er == nil {
				var e id.EventID
				if len(v) != len(e) {
					return errors.New("schemas: reply_to must be 32 bytes")
				}
				copy(e[:], v)
				b.ReplyTo = &e
			}
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if b.Alt == "" || b.Original == nil {
		return nil, errors.New("schemas: visual block missing alt or original")
	}
	if err := validatePreview(b.ThumbMIME, b.Thumb); err != nil {
		return nil, err
	}
	return b, nil
}

// ---- VoiceBlock (block.voice.v1) ----
// keys: 1 fallback, 2 duration_ms, 3 waveform, 4 transcript, 5 language, 6 original

type VoiceBlock struct {
	DurationMS uint64
	Waveform   []byte // ≤64 buckets, each 0..255
	Transcript string
	Language   string
	Original   *AssetRef
}

func (b *VoiceBlock) Fallback() string {
	if b.Transcript != "" {
		return clip(b.Transcript, MaxFallbackLen)
	}
	return fmt.Sprintf("Voice message · %s", fmtDuration(b.DurationMS))
}

func (b *VoiceBlock) Encode() ([]byte, error) {
	if b.DurationMS == 0 {
		return nil, errors.New("schemas: voice block requires duration")
	}
	if len(b.Waveform) > MaxWaveformBuckets {
		return nil, errors.New("schemas: waveform too long")
	}
	if len(b.Transcript) > MaxTranscriptLen {
		return nil, errors.New("schemas: transcript too long")
	}
	if b.Original == nil {
		return nil, errors.New("schemas: voice block requires original asset")
	}
	orig, err := b.Original.encode()
	if err != nil {
		return nil, err
	}
	n := 3 // fallback, duration, original
	if len(b.Waveform) > 0 {
		n++
	}
	if b.Transcript != "" {
		n++
	}
	if b.Language != "" {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.Fallback())
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendUint(buf, b.DurationMS)
	if len(b.Waveform) > 0 {
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendBytes(buf, b.Waveform)
	}
	if b.Transcript != "" {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendText(buf, b.Transcript)
	}
	if b.Language != "" {
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendText(buf, b.Language)
	}
	buf = codec.AppendUint(buf, 6)
	buf = append(buf, orig...)
	return finishBlock(buf)
}

func DecodeVoiceBlock(p []byte) (*VoiceBlock, error) {
	b := &VoiceBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.DurationMS, er = d.ReadUint()
		case 3:
			var v []byte
			v, er = d.ReadBytes()
			if er == nil && len(v) > MaxWaveformBuckets {
				return errors.New("schemas: waveform too long")
			}
			b.Waveform = append([]byte(nil), v...)
		case 4:
			b.Transcript, er = d.ReadText()
		case 5:
			b.Language, er = d.ReadText()
		case 6:
			b.Original, er = decodeAssetRef(d)
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if b.DurationMS == 0 || b.Original == nil {
		return nil, errors.New("schemas: voice block missing duration or original")
	}
	return b, nil
}

// ---- AudioBlock (block.audio.v1) ----
// keys: 1 fallback, 2 title, 3 artist, 4 bpm, 5 loop, 6 duration_ms,
//       7 waveform, 8 original

type AudioBlock struct {
	Title      string
	Artist     string
	BPM        uint64
	Loop       bool
	DurationMS uint64
	Waveform   []byte
	Original   *AssetRef
}

func (b *AudioBlock) Fallback() string {
	t := b.Title
	if t == "" {
		t = "audio"
	}
	return clip(fmt.Sprintf("%s · %s", t, fmtDuration(b.DurationMS)), MaxFallbackLen)
}

func (b *AudioBlock) Encode() ([]byte, error) {
	if b.Title == "" || len(b.Title) > MaxTitleLen {
		return nil, errors.New("schemas: audio block requires title (≤300)")
	}
	if b.DurationMS == 0 {
		return nil, errors.New("schemas: audio block requires duration")
	}
	if len(b.Waveform) > MaxWaveformBuckets {
		return nil, errors.New("schemas: waveform too long")
	}
	if b.Original == nil {
		return nil, errors.New("schemas: audio block requires original asset")
	}
	orig, err := b.Original.encode()
	if err != nil {
		return nil, err
	}
	n := 4 // fallback, title, duration, original
	if b.Artist != "" {
		n++
	}
	if b.BPM != 0 {
		n++
	}
	if b.Loop {
		n++
	}
	if len(b.Waveform) > 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.Fallback())
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, b.Title)
	if b.Artist != "" {
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendText(buf, b.Artist)
	}
	if b.BPM != 0 {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendUint(buf, b.BPM)
	}
	if b.Loop {
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendBool(buf, true)
	}
	buf = codec.AppendUint(buf, 6)
	buf = codec.AppendUint(buf, b.DurationMS)
	if len(b.Waveform) > 0 {
		buf = codec.AppendUint(buf, 7)
		buf = codec.AppendBytes(buf, b.Waveform)
	}
	buf = codec.AppendUint(buf, 8)
	buf = append(buf, orig...)
	return finishBlock(buf)
}

func DecodeAudioBlock(p []byte) (*AudioBlock, error) {
	b := &AudioBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.Title, er = d.ReadText()
		case 3:
			b.Artist, er = d.ReadText()
		case 4:
			b.BPM, er = d.ReadUint()
		case 5:
			b.Loop, er = d.ReadBool()
		case 6:
			b.DurationMS, er = d.ReadUint()
		case 7:
			var v []byte
			v, er = d.ReadBytes()
			b.Waveform = append([]byte(nil), v...)
		case 8:
			b.Original, er = decodeAssetRef(d)
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if b.Title == "" || b.Original == nil {
		return nil, errors.New("schemas: audio block missing title or original")
	}
	return b, nil
}

// ---- FileBlock (block.file.v1) ----
// keys: 1 fallback, 2 filename, 3 media_type, 4 size, 5 original

type FileBlock struct {
	Filename  string
	MediaType string
	Size      uint64
	Original  *AssetRef
}

func (b *FileBlock) Fallback() string {
	return clip(fmt.Sprintf("%s · %s", b.Filename, fmtSize(b.Size)), MaxFallbackLen)
}

func (b *FileBlock) Encode() ([]byte, error) {
	b.Filename = NormalizeFilename(b.Filename)
	if b.Size == 0 || b.Original == nil {
		return nil, errors.New("schemas: file block requires size and original")
	}
	orig, err := b.Original.encode()
	if err != nil {
		return nil, err
	}
	buf := codec.AppendMap(nil, 5)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.Fallback())
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, b.Filename)
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendText(buf, b.MediaType)
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendUint(buf, b.Size)
	buf = codec.AppendUint(buf, 5)
	buf = append(buf, orig...)
	return finishBlock(buf)
}

func DecodeFileBlock(p []byte) (*FileBlock, error) {
	b := &FileBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.Filename, er = d.ReadText()
		case 3:
			b.MediaType, er = d.ReadText()
		case 4:
			b.Size, er = d.ReadUint()
		case 5:
			b.Original, er = decodeAssetRef(d)
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if b.Filename == "" || b.Original == nil {
		return nil, errors.New("schemas: file block missing filename or original")
	}
	b.Filename = NormalizeFilename(b.Filename)
	return b, nil
}

// ---- LinkBlock (block.link.v1) ----
// keys: 1 fallback, 2 url, 3 title, 4 description, 5 thumb_mime, 6 thumb
// The preview is attached by the SENDER — no central unfurl service exists.

type LinkBlock struct {
	URL         string
	Title       string
	Description string
	ThumbMIME   string
	Thumb       []byte
}

func (b *LinkBlock) Fallback() string {
	if b.Title != "" {
		return clip(b.Title, MaxFallbackLen)
	}
	return clip(b.URL, MaxFallbackLen)
}

func (b *LinkBlock) Encode() ([]byte, error) {
	if err := ValidateHTTPURL(b.URL); err != nil {
		return nil, err
	}
	if len(b.Title) > MaxTitleLen || len(b.Description) > MaxCaptionLen {
		return nil, errors.New("schemas: link title or description too long")
	}
	if err := validatePreview(b.ThumbMIME, b.Thumb); err != nil {
		return nil, err
	}
	n := 2
	if b.Title != "" {
		n++
	}
	if b.Description != "" {
		n++
	}
	if len(b.Thumb) > 0 {
		n += 2
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.Fallback())
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, b.URL)
	if b.Title != "" {
		buf = codec.AppendUint(buf, 3)
		buf = codec.AppendText(buf, b.Title)
	}
	if b.Description != "" {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendText(buf, b.Description)
	}
	if len(b.Thumb) > 0 {
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendText(buf, b.ThumbMIME)
		buf = codec.AppendUint(buf, 6)
		buf = codec.AppendBytes(buf, b.Thumb)
	}
	return finishBlock(buf)
}

func DecodeLinkBlock(p []byte) (*LinkBlock, error) {
	b := &LinkBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.URL, er = d.ReadText()
		case 3:
			b.Title, er = d.ReadText()
		case 4:
			b.Description, er = d.ReadText()
		case 5:
			b.ThumbMIME, er = d.ReadText()
		case 6:
			var v []byte
			v, er = d.ReadBytes()
			b.Thumb = append([]byte(nil), v...)
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if err := ValidateHTTPURL(b.URL); err != nil {
		return nil, err
	}
	if err := validatePreview(b.ThumbMIME, b.Thumb); err != nil {
		return nil, err
	}
	return b, nil
}

// ---- ReactionBlock (block.reaction.v1) ----
// keys: 1 fallback, 2 target, 3 emoji, 4 active
// State-based, not toggle: the event carries the DESIRED state, and the
// reducer resolves concurrency by (clock, event id) — two offline devices
// both saying active=true converge to one visible reaction.

type ReactionBlock struct {
	Target id.EventID
	Emoji  string
	Active bool
}

func (b *ReactionBlock) Fallback() string { return b.Emoji }

func (b *ReactionBlock) Encode() ([]byte, error) {
	emoji, err := NormalizeEmoji(b.Emoji)
	if err != nil {
		return nil, err
	}
	b.Emoji = emoji
	n := 3
	if b.Active {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.Emoji)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendBytes(buf, b.Target[:])
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendText(buf, b.Emoji)
	if b.Active {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendBool(buf, true)
	}
	return finishBlock(buf)
}

func DecodeReactionBlock(p []byte) (*ReactionBlock, error) {
	b := &ReactionBlock{}
	seenTarget := false
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			var v []byte
			v, er = d.ReadBytes()
			if er == nil {
				if len(v) != id.Size {
					return errors.New("schemas: reaction target must be 32 bytes")
				}
				copy(b.Target[:], v)
				seenTarget = true
			}
		case 3:
			b.Emoji, er = d.ReadText()
		case 4:
			b.Active, er = d.ReadBool()
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if !seenTarget {
		return nil, errors.New("schemas: reaction missing target")
	}
	b.Emoji, err = NormalizeEmoji(b.Emoji)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ---- LiveSignalBlock (block.live_signal.v1) ----
// keys: 1 fallback, 2 engine, 3 preset, 4 seed, 5 params (map name→permille)
//
// Validation is deliberately three-layered (plan §12): this protocol
// validator checks ONLY universal constraints and knows no preset catalog —
// producer UIs are strict, renderers are defensive, the network is tolerant.

// LiveSignalEngineV1 is the engine id our renderer implements.
const LiveSignalEngineV1 = "qs.live_signal.v1"

var presetRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}@[1-9][0-9]{0,3}$`)
var paramNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type SignalParam struct {
	Name  string
	Value uint64 // permille 0..1000 (no floats in signed structures, ADR-003)
}

type LiveSignalBlock struct {
	FallbackText string // required: what a text terminal shows
	Engine       string
	Preset       string // "name@version", version immutable once published
	Seed         uint64
	Params       []SignalParam
}

func (b *LiveSignalBlock) Encode() ([]byte, error) {
	if b.FallbackText == "" || len(b.FallbackText) > MaxFallbackLen {
		return nil, errors.New("schemas: live signal requires fallback text (≤200)")
	}
	if b.Engine == "" || len(b.Engine) > 64 {
		return nil, errors.New("schemas: live signal requires engine id")
	}
	if !presetRe.MatchString(b.Preset) {
		return nil, errors.New("schemas: preset must be name@version")
	}
	if len(b.Params) > MaxSignalParams {
		return nil, errors.New("schemas: too many signal params")
	}
	for _, p := range b.Params {
		if !paramNameRe.MatchString(p.Name) {
			return nil, fmt.Errorf("schemas: bad param name %q", p.Name)
		}
		if p.Value > 1000 {
			return nil, fmt.Errorf("schemas: param %s out of permille range", p.Name)
		}
	}
	n := 4
	if len(b.Params) > 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.FallbackText)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, b.Engine)
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendText(buf, b.Preset)
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendUint(buf, b.Seed)
	if len(b.Params) > 0 {
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendArray(buf, len(b.Params))
		for _, p := range b.Params {
			buf = codec.AppendArray(buf, 2)
			buf = codec.AppendText(buf, p.Name)
			buf = codec.AppendUint(buf, p.Value)
		}
	}
	return finishBlock(buf)
}

func DecodeLiveSignalBlock(p []byte) (*LiveSignalBlock, error) {
	b := &LiveSignalBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.Engine, er = d.ReadText()
		case 3:
			b.Preset, er = d.ReadText()
		case 4:
			b.Seed, er = d.ReadUint()
		case 5:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return er
			}
			if cnt > MaxSignalParams {
				return errors.New("schemas: too many signal params")
			}
			for range cnt {
				if _, er = d.ReadArray(); er != nil {
					return er
				}
				var sp SignalParam
				if sp.Name, er = d.ReadText(); er != nil {
					return er
				}
				if sp.Value, er = d.ReadUint(); er != nil {
					return er
				}
				if !paramNameRe.MatchString(sp.Name) || sp.Value > 1000 {
					return errors.New("schemas: bad signal param")
				}
				b.Params = append(b.Params, sp)
			}
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	fb, err := DecodeBlockFallback(p)
	if err != nil {
		return nil, err
	}
	b.FallbackText = fb
	if b.Engine == "" || !presetRe.MatchString(b.Preset) {
		return nil, errors.New("schemas: live signal missing engine or preset")
	}
	return b, nil
}

// ---- helpers ----

// walkBlock iterates a block payload map, delegating unknown keys to skip.
func walkBlock(p []byte, fn func(k uint64, d *codec.Decoder) error) error {
	d := codec.NewDecoder(p)
	m, err := d.ReadMapHeader()
	if err != nil {
		return err
	}
	seenFallback := false
	for {
		k, ok, er := m.Next()
		if er != nil {
			return er
		}
		if !ok {
			break
		}
		if k == 1 {
			fb, er := d.ReadText()
			if er != nil {
				return er
			}
			seenFallback = fb != ""
			continue
		}
		if er := fn(k, d); er != nil {
			return er
		}
	}
	if err := d.Done(); err != nil {
		return err
	}
	if !seenFallback {
		return errors.New("schemas: block missing fallback text (key 1)")
	}
	return nil
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	// Do not split a UTF-8 sequence.
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func fmtDuration(ms uint64) string {
	s := ms / 1000
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func fmtSize(b uint64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func init() {
	Register(BlockVisual, func(p []byte) error { _, err := DecodeVisualBlock(p); return err })
	Register(BlockVoice, func(p []byte) error { _, err := DecodeVoiceBlock(p); return err })
	Register(BlockAudio, func(p []byte) error { _, err := DecodeAudioBlock(p); return err })
	Register(BlockFile, func(p []byte) error { _, err := DecodeFileBlock(p); return err })
	Register(BlockLink, func(p []byte) error { _, err := DecodeLinkBlock(p); return err })
	Register(BlockReaction, func(p []byte) error { _, err := DecodeReactionBlock(p); return err })
	Register(BlockLiveSignal, func(p []byte) error { _, err := DecodeLiveSignalBlock(p); return err })
	Register(BlockAttached, func(p []byte) error { _, err := DecodeAttachedBlock(p); return err })
	Register(BlockVideo, func(p []byte) error { _, err := DecodeVideoBlock(p); return err })
}

// ---- AttachedBlock (block.attached.v1) ----
//
// An asset CARRIER: it puts an AssetRef into the log so every replica can
// index, authorize and decrypt the asset — without creating a feed entry.
// Publications reference the asset by its public id (composer uploads).

type AttachedBlock struct {
	Filename  string
	MediaType string
	Original  *AssetRef
}

func (b *AttachedBlock) Fallback() string {
	return "attached: " + b.Filename
}

func (b *AttachedBlock) Encode() ([]byte, error) {
	b.Filename = NormalizeFilename(b.Filename)
	if b.Original == nil {
		return nil, errors.New("schemas: attached block requires an asset")
	}
	orig, err := b.Original.encode()
	if err != nil {
		return nil, err
	}
	buf := codec.AppendMap(nil, 4)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, b.Fallback())
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendText(buf, b.Filename)
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendText(buf, b.MediaType)
	buf = codec.AppendUint(buf, 4)
	buf = append(buf, orig...)
	return finishBlock(buf)
}

func DecodeAttachedBlock(p []byte) (*AttachedBlock, error) {
	b := &AttachedBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.Filename, er = d.ReadText()
		case 3:
			b.MediaType, er = d.ReadText()
		case 4:
			b.Original, er = decodeAssetRef(d)
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if b.Original == nil {
		return nil, errors.New("schemas: attached block missing asset")
	}
	b.Filename = NormalizeFilename(b.Filename)
	return b, nil
}

// ---- VideoBlock (block.video.v1) ----
//
// Same pipeline as visual: an inline poster thumb for instant preview, the
// original as an encrypted asset. Alt is required — the honest path for
// text terminals ("what is in this video").

type VideoBlock struct {
	Caption    string
	Alt        string
	ThumbMIME  string
	Thumb      []byte // poster frame, jpeg/png/webp only
	DurationMS uint64
	Width      uint64
	Height     uint64
	Original   *AssetRef
}

func (b *VideoBlock) Fallback() string {
	if b.Alt != "" {
		return "video: " + b.Alt
	}
	return "video"
}

func (b *VideoBlock) Encode() ([]byte, error) {
	if b.Alt == "" || len(b.Alt) > MaxAltLen {
		return nil, errors.New("schemas: video block requires alt text (≤500)")
	}
	if len(b.Caption) > MaxCaptionLen {
		return nil, errors.New("schemas: caption too long")
	}
	if err := validatePreview(b.ThumbMIME, b.Thumb); err != nil {
		return nil, err
	}
	if b.Original == nil {
		return nil, errors.New("schemas: video block requires original asset")
	}
	orig, err := b.Original.encode()
	if err != nil {
		return nil, err
	}
	n := 3 // fallback, alt, original
	if b.Caption != "" {
		n++
	}
	if len(b.Thumb) > 0 {
		n += 2
	}
	if b.DurationMS != 0 {
		n++
	}
	if b.Width != 0 {
		n++
	}
	if b.Height != 0 {
		n++
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, clip(b.Fallback(), MaxFallbackLen))
	if b.Caption != "" {
		buf = codec.AppendUint(buf, 2)
		buf = codec.AppendText(buf, b.Caption)
	}
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendText(buf, b.Alt)
	if len(b.Thumb) > 0 {
		buf = codec.AppendUint(buf, 4)
		buf = codec.AppendText(buf, b.ThumbMIME)
		buf = codec.AppendUint(buf, 5)
		buf = codec.AppendBytes(buf, b.Thumb)
	}
	if b.DurationMS != 0 {
		buf = codec.AppendUint(buf, 6)
		buf = codec.AppendUint(buf, b.DurationMS)
	}
	if b.Width != 0 {
		buf = codec.AppendUint(buf, 7)
		buf = codec.AppendUint(buf, b.Width)
	}
	if b.Height != 0 {
		buf = codec.AppendUint(buf, 8)
		buf = codec.AppendUint(buf, b.Height)
	}
	buf = codec.AppendUint(buf, 9)
	buf = append(buf, orig...)
	return finishBlock(buf)
}

func DecodeVideoBlock(p []byte) (*VideoBlock, error) {
	b := &VideoBlock{}
	err := walkBlock(p, func(k uint64, d *codec.Decoder) (er error) {
		switch k {
		case 2:
			b.Caption, er = d.ReadText()
		case 3:
			b.Alt, er = d.ReadText()
		case 4:
			b.ThumbMIME, er = d.ReadText()
		case 5:
			var v []byte
			v, er = d.ReadBytes()
			b.Thumb = append([]byte(nil), v...)
		case 6:
			b.DurationMS, er = d.ReadUint()
		case 7:
			b.Width, er = d.ReadUint()
		case 8:
			b.Height, er = d.ReadUint()
		case 9:
			b.Original, er = decodeAssetRef(d)
		default:
			er = d.SkipItem()
		}
		return er
	})
	if err != nil {
		return nil, err
	}
	if b.Alt == "" || b.Original == nil {
		return nil, errors.New("schemas: video block missing alt or original")
	}
	if err := validatePreview(b.ThumbMIME, b.Thumb); err != nil {
		return nil, err
	}
	return b, nil
}
