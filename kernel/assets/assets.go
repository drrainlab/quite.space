// Package assets implements the encrypted asset pipeline (media wave):
// streaming ingest of a plaintext into encrypted 4/16/64-KiB chunks stored
// by wire id (hash of ciphertext — plaintext hashes are never published),
// an encrypted manifest for larger assets, and streaming retrieval with
// mandatory integrity verification.
//
// Blob format v1: [1 byte format version][24 bytes XChaCha nonce][ct+tag].
// A fresh random nonce per blob (24 bytes makes random safe); AADs are
// canonical-CBOR structures with domain separation, so a chunk cannot be
// replayed into another asset, position, or role.
package assets

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/drrainlab/quiet_places/kernel/storage"
	"github.com/drrainlab/quiet_places/protocol/codec"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"golang.org/x/crypto/chacha20poly1305"
)

// Limits (plan: resource limits).
const (
	MaxAssetSize      = 64 << 20
	DefaultChunkSize  = 64 << 10
	MaxChunksPerAsset = 16384 // 64 MiB at 4 KiB chunks
	MaxManifestBytes  = 1 << 20
	blobFormatV1      = 1
)

// Domain separation strings (plan §13).
const (
	domainChunk    = "qs.asset.chunk.v1"
	domainManifest = "qs.asset.manifest.v1"
)

var (
	ErrAssetTooLarge     = errors.New("assets: asset exceeds MaxAssetSize")
	ErrAssetEmpty        = errors.New("assets: empty assets are not allowed")
	ErrAssetSizeMismatch = errors.New("assets: reader length does not match declared size")
	ErrIntegrity         = errors.New("assets: plaintext digest mismatch — content rejected")
)

// Store is the blob surface the pipeline needs (implemented by
// storage.Root; small fakes in tests).
type Store interface {
	PutBlob(data []byte) (id.Hash, error)
	GetBlob(h id.Hash) ([]byte, error)
	HasBlob(h id.Hash) bool
}

var _ Store = (*storage.Root)(nil)

// Metadata describes the ingested content.
type Metadata struct {
	MediaType  string
	Role       string
	Width      uint64
	Height     uint64
	DurationMS uint64
	ChunkSize  int // 4/16/64 KiB; 0 = default (mesh profiles pick smaller)
}

// ---- AAD construction (fixed byte-for-byte via canonical CBOR) ----

func chunkAAD(assetID [16]byte, index, total uint32, plaintextLen uint32, manifestVer uint16) []byte {
	buf := codec.AppendMap(nil, 6)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, domainChunk)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendBytes(buf, assetID[:])
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendUint(buf, uint64(index))
	buf = codec.AppendUint(buf, 4)
	buf = codec.AppendUint(buf, uint64(total))
	buf = codec.AppendUint(buf, 5)
	buf = codec.AppendUint(buf, uint64(plaintextLen))
	buf = codec.AppendUint(buf, 6)
	buf = codec.AppendUint(buf, uint64(manifestVer))
	return buf
}

func manifestAAD(assetID [16]byte, manifestVer uint16) []byte {
	buf := codec.AppendMap(nil, 3)
	buf = codec.AppendUint(buf, 1)
	buf = codec.AppendText(buf, domainManifest)
	buf = codec.AppendUint(buf, 2)
	buf = codec.AppendBytes(buf, assetID[:])
	buf = codec.AppendUint(buf, 3)
	buf = codec.AppendUint(buf, uint64(manifestVer))
	return buf
}

// ---- Blob seal/open ----

func sealBlob(key [32]byte, aad, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, blobFormatV1)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

func openBlob(key [32]byte, aad, blob []byte) ([]byte, error) {
	if len(blob) < 1+chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, errors.New("assets: blob too short")
	}
	if blob[0] != blobFormatV1 {
		return nil, fmt.Errorf("assets: unsupported blob format %d", blob[0])
	}
	nonce := blob[1 : 1+chacha20poly1305.NonceSizeX]
	ct := blob[1+chacha20poly1305.NonceSizeX:]
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, errors.New("assets: blob decryption failed (wrong key, position, or tampered)")
	}
	return pt, nil
}

// ---- Manifest (encrypted, external for larger assets) ----

// manifest wire key table v1.
const (
	manKeyVersion   = 1
	manKeyCipher    = 2
	manKeyChunkSize = 3
	manKeyTotalSize = 4
	manKeyChunks    = 5
)

// Manifest is the decrypted chunk list of a larger asset.
type Manifest struct {
	Version   uint64
	Cipher    string
	ChunkSize uint64
	TotalSize uint64
	Chunks    []id.Hash
}

func encodeManifest(m *Manifest) []byte {
	buf := codec.AppendMap(nil, 5)
	buf = codec.AppendUint(buf, manKeyVersion)
	buf = codec.AppendUint(buf, m.Version)
	buf = codec.AppendUint(buf, manKeyCipher)
	buf = codec.AppendText(buf, m.Cipher)
	buf = codec.AppendUint(buf, manKeyChunkSize)
	buf = codec.AppendUint(buf, m.ChunkSize)
	buf = codec.AppendUint(buf, manKeyTotalSize)
	buf = codec.AppendUint(buf, m.TotalSize)
	buf = codec.AppendUint(buf, manKeyChunks)
	buf = codec.AppendArray(buf, len(m.Chunks))
	for _, c := range m.Chunks {
		buf = codec.AppendBytes(buf, c[:])
	}
	return buf
}

func decodeManifest(data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, errors.New("assets: manifest too large")
	}
	d := codec.NewDecoder(data)
	mr, err := d.ReadMapHeader()
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	for {
		k, ok, er := mr.Next()
		if er != nil {
			return nil, er
		}
		if !ok {
			break
		}
		switch k {
		case manKeyVersion:
			m.Version, er = d.ReadUint()
		case manKeyCipher:
			m.Cipher, er = d.ReadText()
		case manKeyChunkSize:
			m.ChunkSize, er = d.ReadUint()
		case manKeyTotalSize:
			m.TotalSize, er = d.ReadUint()
		case manKeyChunks:
			var cnt int
			cnt, er = d.ReadArray()
			if er != nil {
				return nil, er
			}
			if cnt > MaxChunksPerAsset {
				return nil, errors.New("assets: manifest chunk count over limit")
			}
			for range cnt {
				b, e := d.ReadBytes()
				if e != nil {
					return nil, e
				}
				if len(b) != id.Size {
					return nil, errors.New("assets: bad chunk id in manifest")
				}
				var h id.Hash
				copy(h[:], b)
				m.Chunks = append(m.Chunks, h)
			}
		default:
			er = d.SkipItem()
		}
		if er != nil {
			return nil, er
		}
	}
	if err := d.Done(); err != nil {
		return nil, err
	}
	if m.Version == 0 || m.ChunkSize == 0 || m.TotalSize == 0 || len(m.Chunks) == 0 {
		return nil, errors.New("assets: manifest missing required fields")
	}
	return m, nil
}

// LoadManifest fetches and decrypts an asset's external manifest, and
// cross-checks it against the ref (size, chunk size, chunk count).
func LoadManifest(store Store, ref *schemas.AssetRef) (*Manifest, error) {
	if ref.ManifestWireID == nil {
		return nil, errors.New("assets: ref has no external manifest")
	}
	blob, err := store.GetBlob(*ref.ManifestWireID)
	if err != nil {
		return nil, err
	}
	pt, err := openBlob(ref.Key, manifestAAD(ref.AssetID, uint16(ref.ManifestVer)), blob)
	if err != nil {
		return nil, err
	}
	m, err := decodeManifest(pt)
	if err != nil {
		return nil, err
	}
	if m.TotalSize != ref.Size || m.ChunkSize != ref.ChunkSize {
		return nil, errors.New("assets: manifest does not match asset ref")
	}
	want := int((ref.Size + ref.ChunkSize - 1) / ref.ChunkSize)
	if len(m.Chunks) != want {
		return nil, errors.New("assets: manifest chunk count does not match size")
	}
	return m, nil
}

// ---- Ingest ----

// Ingest reads exactly size bytes from src, chunks, encrypts, and stores
// them, returning the AssetRef to embed in a block. Streaming: memory use
// is one chunk regardless of asset size.
func Ingest(store Store, src io.Reader, size int64, meta Metadata) (*schemas.AssetRef, error) {
	if size <= 0 {
		return nil, ErrAssetEmpty
	}
	if size > MaxAssetSize {
		return nil, ErrAssetTooLarge
	}
	chunkSize := meta.ChunkSize
	if chunkSize == 0 {
		chunkSize = DefaultChunkSize
	}
	okSize := false
	for _, s := range schemas.AllowedChunkSizes {
		if uint64(chunkSize) == s {
			okSize = true
		}
	}
	if !okSize {
		return nil, fmt.Errorf("assets: chunk size %d not allowed", chunkSize)
	}
	total := int((size + int64(chunkSize) - 1) / int64(chunkSize))
	if total > MaxChunksPerAsset {
		return nil, ErrAssetTooLarge
	}

	ref := &schemas.AssetRef{
		Role: meta.Role, MediaType: meta.MediaType, Size: uint64(size),
		Width: meta.Width, Height: meta.Height, DurationMS: meta.DurationMS,
		ChunkSize: uint64(chunkSize),
	}
	if _, err := rand.Read(ref.AssetID[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(ref.Key[:]); err != nil {
		return nil, err
	}
	manifestVer := uint16(1)

	digest := sha256.New()
	buf := make([]byte, chunkSize)
	var chunkIDs []id.Hash
	var read int64
	for idx := 0; idx < total; idx++ {
		want := int64(chunkSize)
		if remaining := size - read; remaining < want {
			want = remaining
		}
		n, err := io.ReadFull(src, buf[:want])
		if err != nil {
			return nil, fmt.Errorf("assets: %w (short read at chunk %d)", io.ErrUnexpectedEOF, idx)
		}
		read += int64(n)
		digest.Write(buf[:n])
		aad := chunkAAD(ref.AssetID, uint32(idx), uint32(total), uint32(n), manifestVer)
		blob, err := sealBlob(ref.Key, aad, buf[:n])
		if err != nil {
			return nil, err
		}
		wireID, err := store.PutBlob(blob)
		if err != nil {
			return nil, err
		}
		chunkIDs = append(chunkIDs, wireID)
	}
	// The reader must be exactly consumed: one extra byte means the caller
	// lied about size.
	var probe [1]byte
	if n, _ := src.Read(probe[:]); n != 0 {
		return nil, ErrAssetSizeMismatch
	}
	digest.Sum(ref.PlaintextDigest[:0])

	if total <= schemas.InlineChunkMax {
		ref.InlineChunks = chunkIDs
	} else {
		man := &Manifest{Version: uint64(manifestVer), Cipher: "xchacha20poly1305",
			ChunkSize: uint64(chunkSize), TotalSize: uint64(size), Chunks: chunkIDs}
		sealed, err := sealBlob(ref.Key, manifestAAD(ref.AssetID, manifestVer), encodeManifest(man))
		if err != nil {
			return nil, err
		}
		manID, err := store.PutBlob(sealed)
		if err != nil {
			return nil, err
		}
		ref.ManifestWireID = &manID
		ref.ManifestVer = uint64(manifestVer)
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return ref, nil
}

// IngestBytes is the convenience form for tests and small data.
func IngestBytes(store Store, data []byte, meta Metadata) (*schemas.AssetRef, error) {
	return Ingest(store, bytes.NewReader(data), int64(len(data)), meta)
}

// ---- Retrieve ----

// chunkList resolves the asset's chunk ids (inline or via manifest).
func chunkList(store Store, ref *schemas.AssetRef) ([]id.Hash, error) {
	if len(ref.InlineChunks) > 0 {
		return ref.InlineChunks, nil
	}
	m, err := LoadManifest(store, ref)
	if err != nil {
		return nil, err
	}
	return m.Chunks, nil
}

// RetrieveTo streams the decrypted asset into dst, verifying every chunk's
// AAD position and, at the end, the whole-plaintext digest. On digest
// mismatch the already-written output must be discarded by the caller —
// RetrieveTo returns ErrIntegrity and callers that need atomicity use
// RetrieveBytes.
func RetrieveTo(store Store, ref *schemas.AssetRef, dst io.Writer) error {
	chunks, err := chunkList(store, ref)
	if err != nil {
		return err
	}
	manifestVer := uint16(1)
	if ref.ManifestWireID != nil {
		manifestVer = uint16(ref.ManifestVer)
	}
	digest := sha256.New()
	var written uint64
	for idx, wireID := range chunks {
		blob, err := store.GetBlob(wireID)
		if err != nil {
			return fmt.Errorf("assets: chunk %d not local: %w", idx, err)
		}
		want := ref.ChunkSize
		if remaining := ref.Size - written; remaining < want {
			want = remaining
		}
		aad := chunkAAD(ref.AssetID, uint32(idx), uint32(len(chunks)), uint32(want), manifestVer)
		pt, err := openBlob(ref.Key, aad, blob)
		if err != nil {
			return err
		}
		if uint64(len(pt)) != want {
			return errors.New("assets: chunk plaintext size mismatch")
		}
		digest.Write(pt)
		if _, err := dst.Write(pt); err != nil {
			return err
		}
		written += uint64(len(pt))
	}
	if written != ref.Size {
		return ErrIntegrity
	}
	var sum id.Hash
	digest.Sum(sum[:0])
	if sum != ref.PlaintextDigest {
		return ErrIntegrity
	}
	return nil
}

// RetrieveBytes buffers and verifies before returning anything.
func RetrieveBytes(store Store, ref *schemas.AssetRef) ([]byte, error) {
	var buf bytes.Buffer
	if err := RetrieveTo(store, ref, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- State machine (plan §10) ----

// State is the two-phase asset lifecycle.
type State string

const (
	StatePreviewOnly     State = "preview_only"
	StateManifestMissing State = "manifest_missing"
	StateChunksMissing   State = "chunks_missing"
	StateFetching        State = "fetching"
	StateVerifying       State = "verifying"
	StateComplete        State = "complete"
	StateFailed          State = "failed"
)

// MissingResult reports what still has to be fetched.
type MissingResult struct {
	ManifestMissing bool
	MissingChunks   []id.Hash
	AvailableChunks int
	TotalChunks     int
}

// Missing inspects local availability. When the external manifest is not
// local, chunk ids are simply unknown yet — that is the manifest_missing
// phase, not an error.
func Missing(store Store, ref *schemas.AssetRef) (MissingResult, error) {
	res := MissingResult{}
	if ref.ManifestWireID != nil && !store.HasBlob(*ref.ManifestWireID) {
		res.ManifestMissing = true
		res.TotalChunks = int((ref.Size + ref.ChunkSize - 1) / ref.ChunkSize)
		return res, nil
	}
	chunks, err := chunkList(store, ref)
	if err != nil {
		return res, err
	}
	res.TotalChunks = len(chunks)
	for _, c := range chunks {
		if store.HasBlob(c) {
			res.AvailableChunks++
		} else {
			res.MissingChunks = append(res.MissingChunks, c)
		}
	}
	return res, nil
}

// StateOf derives the passive state (fetching/verifying are transitions the
// fetch coordinator owns).
func StateOf(store Store, ref *schemas.AssetRef) (State, MissingResult) {
	res, err := Missing(store, ref)
	if err != nil {
		return StateFailed, res
	}
	switch {
	case res.ManifestMissing:
		return StateManifestMissing, res
	case len(res.MissingChunks) > 0:
		return StateChunksMissing, res
	default:
		return StateComplete, res
	}
}
