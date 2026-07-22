package assets

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// memStore is an in-memory blob store for tests.
type memStore struct{ blobs map[id.Hash][]byte }

func newMemStore() *memStore { return &memStore{blobs: map[id.Hash][]byte{}} }

func (m *memStore) PutBlob(data []byte) (id.Hash, error) {
	h := id.HashOf(data)
	m.blobs[h] = append([]byte(nil), data...)
	return h, nil
}
func (m *memStore) GetBlob(h id.Hash) ([]byte, error) {
	b, ok := m.blobs[h]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}
func (m *memStore) HasBlob(h id.Hash) bool { _, ok := m.blobs[h]; return ok }

func randomData(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIngestRetrieveInline(t *testing.T) {
	store := newMemStore()
	data := randomData(t, 10_000) // 3 chunks at 4KiB → inline
	ref, err := IngestBytes(store, data, Metadata{MediaType: "image/webp", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if len(ref.InlineChunks) != 3 || ref.ManifestWireID != nil {
		t.Fatalf("expected 3 inline chunks: %+v", ref)
	}
	got, err := RetrieveBytes(store, ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("retrieve: %v", err)
	}
	// Wire ids are hashes of CIPHERTEXT: none may equal the plaintext hash.
	plainHash := id.HashOf(data)
	for _, c := range ref.InlineChunks {
		if c == plainHash {
			t.Fatal("wire id equals plaintext hash — content guessable")
		}
	}
	if st, _ := StateOf(store, ref); st != StateComplete {
		t.Fatalf("state = %v", st)
	}
}

func TestIngestRetrieveManifest(t *testing.T) {
	store := newMemStore()
	data := randomData(t, 600_000) // 147 chunks at 4KiB → external manifest
	ref, err := IngestBytes(store, data, Metadata{MediaType: "audio/webm", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if ref.ManifestWireID == nil || len(ref.InlineChunks) != 0 {
		t.Fatalf("expected external manifest: %+v", ref)
	}
	man, err := LoadManifest(store, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Chunks) != 147 || man.TotalSize != 600_000 {
		t.Fatalf("manifest wrong: %d chunks, %d bytes", len(man.Chunks), man.TotalSize)
	}
	got, err := RetrieveBytes(store, ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("retrieve: %v", err)
	}
}

func TestChunkPositionSwapDetected(t *testing.T) {
	store := newMemStore()
	data := randomData(t, 12_288) // exactly 3 chunks of 4KiB
	ref, err := IngestBytes(store, data, Metadata{MediaType: "application/octet-stream", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	// Swap chunks 0 and 1 in the ref: AAD binds position, so decryption of
	// the swapped chunk must fail — not produce reordered plaintext.
	ref.InlineChunks[0], ref.InlineChunks[1] = ref.InlineChunks[1], ref.InlineChunks[0]
	if _, err := RetrieveBytes(store, ref); err == nil {
		t.Fatal("chunk position swap not detected")
	}
}

func TestTamperedChunkRejected(t *testing.T) {
	store := newMemStore()
	data := randomData(t, 5000)
	ref, err := IngestBytes(store, data, Metadata{MediaType: "x/y", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	// Flip one byte in a stored blob, keep its index consistent (an
	// attacker controlling storage): AEAD must reject.
	target := ref.InlineChunks[0]
	blob := store.blobs[target]
	blob[len(blob)-1] ^= 1
	if _, err := RetrieveBytes(store, ref); err == nil {
		t.Fatal("tampered chunk accepted")
	}
}

func TestSizeContracts(t *testing.T) {
	store := newMemStore()
	// Declared size larger than reader → ErrUnexpectedEOF wrapped.
	if _, err := Ingest(store, bytes.NewReader(make([]byte, 100)), 200,
		Metadata{MediaType: "x/y", ChunkSize: 4096}); err == nil {
		t.Fatal("short reader accepted")
	}
	// Declared size smaller than reader → ErrAssetSizeMismatch.
	if _, err := Ingest(store, bytes.NewReader(make([]byte, 300)), 200,
		Metadata{MediaType: "x/y", ChunkSize: 4096}); !errors.Is(err, ErrAssetSizeMismatch) {
		t.Fatalf("long reader: %v", err)
	}
	// Empty assets are explicitly forbidden.
	if _, err := Ingest(store, bytes.NewReader(nil), 0,
		Metadata{MediaType: "x/y"}); !errors.Is(err, ErrAssetEmpty) {
		t.Fatalf("empty: %v", err)
	}
	// Over the cap → refused before reading.
	if _, err := Ingest(store, bytes.NewReader(nil), MaxAssetSize+1,
		Metadata{MediaType: "x/y"}); !errors.Is(err, ErrAssetTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	// Disallowed chunk size.
	if _, err := Ingest(store, bytes.NewReader(make([]byte, 10)), 10,
		Metadata{MediaType: "x/y", ChunkSize: 1234}); err == nil {
		t.Fatal("bad chunk size accepted")
	}
}

func TestNoncesAreRandomPerBlob(t *testing.T) {
	// The same plaintext ingested twice must produce different ciphertexts
	// and wire ids (fresh key AND fresh nonce per blob) — this is what
	// makes wire ids unlinkable to known content.
	store := newMemStore()
	data := randomData(t, 4096)
	ref1, err := IngestBytes(store, data, Metadata{MediaType: "x/y", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := IngestBytes(store, data, Metadata{MediaType: "x/y", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if ref1.InlineChunks[0] == ref2.InlineChunks[0] {
		t.Fatal("identical wire ids for two ingests of the same content")
	}
	if ref1.PlaintextDigest != ref2.PlaintextDigest {
		t.Fatal("plaintext digests should match (same content)")
	}
}

func TestMissingTwoPhase(t *testing.T) {
	store := newMemStore()
	data := randomData(t, 600_000)
	ref, err := IngestBytes(store, data, Metadata{MediaType: "x/y", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	// A fresh replica with no blobs at all: phase 1 — manifest missing,
	// chunk ids honestly unknown.
	empty := newMemStore()
	res, err := Missing(empty, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ManifestMissing || res.TotalChunks != 147 || len(res.MissingChunks) != 0 {
		t.Fatalf("phase 1 wrong: %+v", res)
	}
	if st, _ := StateOf(empty, ref); st != StateManifestMissing {
		t.Fatalf("state = %v", st)
	}
	// Deliver the manifest only: phase 2 — chunks known and missing.
	manBlob, _ := store.GetBlob(*ref.ManifestWireID)
	empty.PutBlob(manBlob)
	res, err = Missing(empty, ref)
	if err != nil {
		t.Fatal(err)
	}
	if res.ManifestMissing || len(res.MissingChunks) != 147 {
		t.Fatalf("phase 2 wrong: %+v", res)
	}
	if st, _ := StateOf(empty, ref); st != StateChunksMissing {
		t.Fatalf("state = %v", st)
	}
	// Deliver half the chunks: counts stay honest.
	man, _ := LoadManifest(store, ref)
	for _, c := range man.Chunks[:70] {
		b, _ := store.GetBlob(c)
		empty.PutBlob(b)
	}
	res, _ = Missing(empty, ref)
	if res.AvailableChunks != 70 || len(res.MissingChunks) != 77 {
		t.Fatalf("partial wrong: %d/%d", res.AvailableChunks, len(res.MissingChunks))
	}
}

func TestDigestMismatchRejected(t *testing.T) {
	store := newMemStore()
	data := randomData(t, 8000)
	ref, err := IngestBytes(store, data, Metadata{MediaType: "x/y", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	ref.PlaintextDigest[0] ^= 1 // ref claims different content
	if _, err := RetrieveBytes(store, ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("digest mismatch not rejected: %v", err)
	}
}

// V2 content identity (ADR-013): Ingest mints a domain-separated content id,
// stable for identical bytes (reference dedup) and distinct from the raw
// plaintext digest.
func TestIngestContentIDV2(t *testing.T) {
	store := newMemStore()
	data := randomData(t, 9_000)

	ref, err := IngestBytes(store, data, Metadata{MediaType: "image/webp", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Version != schemas.AssetRefVersion {
		t.Fatalf("expected V%d ref, got V%d", schemas.AssetRefVersion, ref.Version)
	}
	// Domain-separated: SHA256("qs.asset.v1" ‖ plaintext), not SHA256(plaintext).
	h := sha256.New()
	h.Write([]byte("qs.asset.v1"))
	h.Write(data)
	var want id.Hash
	h.Sum(want[:0])
	if ref.ContentID != want {
		t.Fatal("content id is not the domain-separated digest")
	}
	if ref.ContentID == ref.PlaintextDigest {
		t.Fatal("content id must differ from the raw plaintext digest")
	}
	// Identical bytes → identical content id (reference-level dedup), even
	// though the ciphertext blobs differ (random per-asset key).
	ref2, err := IngestBytes(store, data, Metadata{MediaType: "image/webp", Role: "original", ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if ref2.ContentID != ref.ContentID {
		t.Fatal("identical content produced different content ids")
	}
	if ref2.AssetID == ref.AssetID {
		t.Fatal("crypto binding handle should be random per ingest")
	}
}
