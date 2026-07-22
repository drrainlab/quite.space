// Block Events (media wave): a message is not text with attachments — it is
// a signed typed block whose payload carries meaning inline and references
// heavy content as encrypted assets.
//
// Universal fallback contract: in EVERY block.* payload, CBOR key 1 is
// always fallback_text; keys ≥2 belong to the specific schema. A node that
// does not know a block type can still show the fallback and must never
// drop the event (forward compatibility by format, not by client goodwill).
package schemas

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"golang.org/x/text/unicode/norm"
)

// Block schema ids, v1.
const (
	BlockVisual     = "block.visual.v1"
	BlockVoice      = "block.voice.v1"
	BlockAudio      = "block.audio.v1"
	BlockFile       = "block.file.v1"
	BlockLink       = "block.link.v1"
	BlockReaction   = "block.reaction.v1"
	BlockLiveSignal = "block.live_signal.v1"
)

// IsBlockSchema reports whether a schema id belongs to the block family.
func IsBlockSchema(schema string) bool { return strings.HasPrefix(schema, "block.") }

// Size limits (plan: protective constraints). The final gate is always the
// actually-encoded CBOR length, not field arithmetic.
const (
	MaxBlockPayloadBytes = 48 << 10
	MaxFallbackLen       = 200
	MaxAltLen            = 500
	MaxCaptionLen        = 1000
	MaxTranscriptLen     = 10 << 10
	MaxFilenameLen       = 255
	MaxURLLen            = 2048
	MaxTitleLen          = 300
	MaxWaveformBuckets   = 64
	MaxInlinePreview     = 40 << 10 // hard cap; UI aims for ≤8KiB
	MaxSignalParams      = 16
	MaxParamNameLen      = 32
	MaxEmojiBytes        = 32
)

// ErrBlockTooLarge is returned when the encoded payload exceeds the limit.
var ErrBlockTooLarge = errors.New("schemas: encoded block exceeds payload limit")

func finishBlock(buf []byte) ([]byte, error) {
	if len(buf) > MaxBlockPayloadBytes {
		return nil, ErrBlockTooLarge
	}
	return buf, nil
}

// DecodeBlockFallback extracts the universal fallback (key 1) from any
// block.* payload, skipping everything it does not understand. This is the
// path an old node takes for a block type from the future.
func DecodeBlockFallback(payload []byte) (string, error) {
	d := codec.NewDecoder(payload)
	m, err := d.ReadMapHeader()
	if err != nil {
		return "", err
	}
	fallback := ""
	for {
		k, ok, er := m.Next()
		if er != nil {
			return "", er
		}
		if !ok {
			break
		}
		if k == 1 {
			fallback, er = d.ReadText()
		} else {
			er = d.SkipItem()
		}
		if er != nil {
			return "", er
		}
	}
	if err := d.Done(); err != nil {
		return "", err
	}
	if fallback == "" {
		return "", errors.New("schemas: block has no fallback text")
	}
	return fallback, nil
}

// validateBlockFallbackOnly is the validator for unknown-but-block schemas
// and the baseline every block validator inherits.
func validateBlockFallbackOnly(p []byte) error {
	_, err := DecodeBlockFallback(p)
	return err
}

var _ = validateBlockFallbackOnly // referenced by future block registrations

// ExtractAssetRefs pulls every asset reference out of a known block payload
// (asset indexing). Unknown schemas yield nothing — their assets become
// fetchable only once the node learns the schema, which is the honest
// reading of "cannot parse".
func ExtractAssetRefs(schema string, payload []byte) []*AssetRef {
	switch schema {
	case BlockVisual:
		if b, err := DecodeVisualBlock(payload); err == nil {
			return []*AssetRef{b.Original}
		}
	case BlockVoice:
		if b, err := DecodeVoiceBlock(payload); err == nil {
			return []*AssetRef{b.Original}
		}
	case BlockAudio:
		if b, err := DecodeAudioBlock(payload); err == nil {
			return []*AssetRef{b.Original}
		}
	case BlockFile:
		if b, err := DecodeFileBlock(payload); err == nil {
			return []*AssetRef{b.Original}
		}
	}
	return nil
}

// ---- AssetRef ----

// InlineChunkMax is the cutoff between inline chunk lists and an external
// encrypted manifest (plan §1: two-level asset model).
const InlineChunkMax = 8

// AllowedChunkSizes for ingest; chunk_size travels in the ref/manifest.
var AllowedChunkSizes = []uint64{4 << 10, 16 << 10, 64 << 10}

// AssetRef points from a block to encrypted content. The key lives here —
// inside the epoch-encrypted block payload — so whoever can read the block
// can read the asset, and nobody else.
type AssetRef struct {
	// AssetID is the crypto binding handle: a random 16-byte value known at
	// ingest start and mixed into every chunk's AAD. It is NOT the public
	// identity in V2 (the content digest can't be known before streaming).
	AssetID         [16]byte
	Role            string // original | preview | transcript
	MediaType       string
	Size            uint64
	Width           uint64 // optional
	Height          uint64 // optional
	DurationMS      uint64 // optional
	Key             [32]byte
	PlaintextDigest id.Hash
	ChunkSize       uint64
	// Exactly one of the following is set:
	InlineChunks   []id.Hash // small assets: wire ids inline (≤ InlineChunkMax)
	ManifestWireID *id.Hash  // larger assets: encrypted manifest blob
	ManifestVer    uint64    // manifest AAD version (1 when manifest present)
	// Versioned identity (ADR-013): Version 0/1 = legacy (public id is the
	// 16-byte AssetID handle); Version 2 = ContentID is the public id, a
	// domain-separated content digest SHA256("qs.asset.v1"‖plaintext). New
	// events emit V2 only; the decoder still reads V1 (dual-read).
	Version   uint64
	ContentID id.Hash
}

// AssetRefVersion is the version new assets are minted at.
const AssetRefVersion = 2

// AssetRef wire key table (append-only). Keys 14/15 add the V2 identity.
const (
	arKeyAssetID    = 1
	arKeyRole       = 2
	arKeyMediaType  = 3
	arKeySize       = 4
	arKeyWidth      = 5
	arKeyHeight     = 6
	arKeyDurationMS = 7
	arKeyKey        = 8
	arKeyDigest     = 9
	arKeyChunkSize  = 10
	arKeyInline     = 11
	arKeyManifest   = 12
	arKeyManVer     = 13
	arKeyVersion    = 14
	arKeyContentID  = 15
)

// PublicID is the addressing/dedup identity used by the composition contract,
// the asset index, and the API: the content digest for V2, else the legacy
// 16-byte handle. Two identical files share a V2 PublicID (reference-level
// dedup); the underlying encrypted blobs still differ (random per-asset keys).
func (a *AssetRef) PublicID() []byte {
	if a.Version >= 2 {
		return append([]byte(nil), a.ContentID[:]...)
	}
	return append([]byte(nil), a.AssetID[:]...)
}

// PublicIDHex is PublicID as lowercase hex (a comparable map/URL key).
func (a *AssetRef) PublicIDHex() string { return hex.EncodeToString(a.PublicID()) }

// Validate enforces structural rules.
func (a *AssetRef) Validate() error {
	if a.MediaType == "" || len(a.MediaType) > 128 {
		return errors.New("schemas: asset media_type missing or too long")
	}
	if a.Size == 0 {
		return errors.New("schemas: empty assets are not allowed")
	}
	okSize := false
	for _, s := range AllowedChunkSizes {
		if a.ChunkSize == s {
			okSize = true
		}
	}
	if !okSize {
		return fmt.Errorf("schemas: chunk size %d not in allowed set", a.ChunkSize)
	}
	hasInline := len(a.InlineChunks) > 0
	hasManifest := a.ManifestWireID != nil
	if hasInline == hasManifest {
		return errors.New("schemas: asset needs exactly one of inline chunks or manifest")
	}
	if hasInline && len(a.InlineChunks) > InlineChunkMax {
		return fmt.Errorf("schemas: more than %d inline chunks — use a manifest", InlineChunkMax)
	}
	if hasManifest && a.ManifestVer == 0 {
		return errors.New("schemas: manifest reference without manifest version")
	}
	// Versioned identity: unknown versions are rejected (not length-guessed).
	// V2 requires a non-zero content id; legacy (0/1) must not carry one.
	switch a.Version {
	case 0, 1:
		if a.ContentID != (id.Hash{}) {
			return errors.New("schemas: legacy asset ref must not carry a content id")
		}
	case 2:
		if a.ContentID == (id.Hash{}) {
			return errors.New("schemas: v2 asset ref requires a content id")
		}
	default:
		return fmt.Errorf("schemas: unknown asset ref version %d", a.Version)
	}
	return nil
}

func (a *AssetRef) encode() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	n := 0
	n++ // asset_id
	if a.Role != "" {
		n++
	}
	n += 2 // media_type, size
	if a.Width != 0 {
		n++
	}
	if a.Height != 0 {
		n++
	}
	if a.DurationMS != 0 {
		n++
	}
	n += 3 // key, digest, chunk_size
	if len(a.InlineChunks) > 0 {
		n++
	}
	if a.ManifestWireID != nil {
		n += 2 // manifest id + version
	}
	if a.Version != 0 {
		n += 2 // version + content_id
	}
	buf := codec.AppendMap(nil, n)
	buf = codec.AppendUint(buf, arKeyAssetID)
	buf = codec.AppendBytes(buf, a.AssetID[:])
	if a.Role != "" {
		buf = codec.AppendUint(buf, arKeyRole)
		buf = codec.AppendText(buf, a.Role)
	}
	buf = codec.AppendUint(buf, arKeyMediaType)
	buf = codec.AppendText(buf, a.MediaType)
	buf = codec.AppendUint(buf, arKeySize)
	buf = codec.AppendUint(buf, a.Size)
	if a.Width != 0 {
		buf = codec.AppendUint(buf, arKeyWidth)
		buf = codec.AppendUint(buf, a.Width)
	}
	if a.Height != 0 {
		buf = codec.AppendUint(buf, arKeyHeight)
		buf = codec.AppendUint(buf, a.Height)
	}
	if a.DurationMS != 0 {
		buf = codec.AppendUint(buf, arKeyDurationMS)
		buf = codec.AppendUint(buf, a.DurationMS)
	}
	buf = codec.AppendUint(buf, arKeyKey)
	buf = codec.AppendBytes(buf, a.Key[:])
	buf = codec.AppendUint(buf, arKeyDigest)
	buf = codec.AppendBytes(buf, a.PlaintextDigest[:])
	buf = codec.AppendUint(buf, arKeyChunkSize)
	buf = codec.AppendUint(buf, a.ChunkSize)
	if len(a.InlineChunks) > 0 {
		buf = codec.AppendUint(buf, arKeyInline)
		buf = codec.AppendArray(buf, len(a.InlineChunks))
		for _, c := range a.InlineChunks {
			buf = codec.AppendBytes(buf, c[:])
		}
	}
	if a.ManifestWireID != nil {
		buf = codec.AppendUint(buf, arKeyManifest)
		buf = codec.AppendBytes(buf, a.ManifestWireID[:])
		buf = codec.AppendUint(buf, arKeyManVer)
		buf = codec.AppendUint(buf, a.ManifestVer)
	}
	if a.Version != 0 {
		buf = codec.AppendUint(buf, arKeyVersion)
		buf = codec.AppendUint(buf, a.Version)
		buf = codec.AppendUint(buf, arKeyContentID)
		buf = codec.AppendBytes(buf, a.ContentID[:])
	}
	return buf, nil
}

func decodeAssetRef(d *codec.Decoder) (*AssetRef, error) {
	m, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	a := &AssetRef{}
	readN := func(dst []byte, want int) error {
		b, err := d.ReadBytes()
		if err != nil {
			return err
		}
		if len(b) != want {
			return fmt.Errorf("schemas: asset field must be %d bytes", want)
		}
		copy(dst, b)
		return nil
	}
	for {
		k, ok, er := m.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case arKeyAssetID:
			er = readN(a.AssetID[:], 16)
		case arKeyRole:
			a.Role, er = d.ReadText()
		case arKeyMediaType:
			a.MediaType, er = d.ReadText()
		case arKeySize:
			a.Size, er = d.ReadUint()
		case arKeyWidth:
			a.Width, er = d.ReadUint()
		case arKeyHeight:
			a.Height, er = d.ReadUint()
		case arKeyDurationMS:
			a.DurationMS, er = d.ReadUint()
		case arKeyKey:
			er = readN(a.Key[:], 32)
		case arKeyDigest:
			er = readN(a.PlaintextDigest[:], 32)
		case arKeyChunkSize:
			a.ChunkSize, er = d.ReadUint()
		case arKeyInline:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			if cnt > InlineChunkMax {
				return nil, errors.New("schemas: too many inline chunks")
			}
			for range cnt {
				var h id.Hash
				if er = readN(h[:], 32); er != nil {
					return nil, er
				}
				a.InlineChunks = append(a.InlineChunks, h)
			}
		case arKeyManifest:
			var h id.Hash
			er = readN(h[:], 32)
			a.ManifestWireID = &h
		case arKeyManVer:
			a.ManifestVer, er = d.ReadUint()
		case arKeyVersion:
			a.Version, er = d.ReadUint()
		case arKeyContentID:
			er = readN(a.ContentID[:], 32)
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// WireIDs returns every wire id this ref makes legally requestable in its
// space: inline chunks, or the manifest (chunk ids come after decryption).
func (a *AssetRef) WireIDs() []id.Hash {
	if a.ManifestWireID != nil {
		return []id.Hash{*a.ManifestWireID}
	}
	return append([]id.Hash(nil), a.InlineChunks...)
}

// ---- Shared helpers ----

var filenameStrip = regexp.MustCompile(`[\x00-\x1f/\\]+`)

// NormalizeFilename removes path separators and control characters.
func NormalizeFilename(name string) string {
	name = filenameStrip.ReplaceAllString(name, "_")
	name = strings.TrimSpace(name)
	if len(name) > MaxFilenameLen {
		name = name[:MaxFilenameLen]
	}
	if name == "" {
		name = "file"
	}
	return name
}

// ValidateHTTPURL enforces http/https only (no javascript:, data:, …).
func ValidateHTTPURL(raw string) error {
	if raw == "" || len(raw) > MaxURLLen {
		return errors.New("schemas: url missing or too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("schemas: url does not parse")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("schemas: url scheme %q not allowed", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("schemas: url has no host")
	}
	return nil
}

// AllowedPreviewMIME whitelists inline preview formats. SVG and anything
// document-like is rejected: a preview is pixels, never an active document.
func AllowedPreviewMIME(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/webp":
		return true
	}
	return false
}

func validatePreview(mime string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if !AllowedPreviewMIME(mime) {
		return fmt.Errorf("schemas: preview mime %q not allowed", mime)
	}
	if len(data) > MaxInlinePreview {
		return errors.New("schemas: inline preview too large")
	}
	return nil
}

// NormalizeEmoji canonicalizes a reaction emoji: NFC, no control or space
// characters, bounded size (~one grapheme cluster).
func NormalizeEmoji(s string) (string, error) {
	s = norm.NFC.String(strings.TrimSpace(s))
	if s == "" || len(s) > MaxEmojiBytes {
		return "", errors.New("schemas: emoji missing or too long")
	}
	runes := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", errors.New("schemas: emoji contains control or space characters")
		}
		runes++
	}
	if runes > 8 { // generous bound for ZWJ sequences, still ~one cluster
		return "", errors.New("schemas: emoji is more than one symbol")
	}
	return s, nil
}
