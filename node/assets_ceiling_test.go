// What one message may carry, checked against what actually survives the
// path rather than against the constant that declares it.
//
// The ceiling was 64 MiB and a phone met it the first time somebody tried to
// send an ordinary thirteen-second video (79.8 MB). Raising a number is easy;
// what this pins is that the number is REACHABLE — that an asset of exactly
// that size chunks, manifests and reads back, and that the chunk-count bound
// next door has not quietly become the real limit.
package node

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/assets"
)

func TestTheDeclaredCeilingIsActuallyReachable(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "big")
	defer rt.Close()

	size := int64(assets.MaxAssetSize)
	ref, err := assets.Ingest(rt.root, &countingReader{n: size}, size, assets.Metadata{
		Role: "original", MediaType: "video/mp4",
	})
	if err != nil {
		t.Fatalf("an asset of exactly MaxAssetSize cannot be ingested: %v", err)
	}
	chunks := (ref.Size + ref.ChunkSize - 1) / ref.ChunkSize
	t.Logf("%d bytes → %d chunks of %d", ref.Size, chunks, ref.ChunkSize)
	if chunks > assets.MaxChunksPerAsset {
		t.Fatalf("%d chunks is past the %d manifest bound — the chunk count is the "+
			"real ceiling, not MaxAssetSize", chunks, assets.MaxChunksPerAsset)
	}

	res, err := assets.Missing(rt.root, ref)
	if err != nil {
		t.Fatal(err)
	}
	if res.ManifestMissing || len(res.MissingChunks) != 0 {
		t.Fatalf("a freshly ingested asset reports missing pieces: manifest=%v chunks=%d",
			res.ManifestMissing, len(res.MissingChunks))
	}

	// And one byte over is still refused, by the size rule rather than by
	// running out of something else on the way.
	if _, err := assets.Ingest(rt.root, &countingReader{n: size + 1}, size+1,
		assets.Metadata{Role: "original", MediaType: "video/mp4"}); !errors.Is(err, assets.ErrAssetTooLarge) {
		t.Errorf("one byte over the ceiling gave %v, want ErrAssetTooLarge", err)
	}
}

// countingReader yields n bytes without holding them. Ingest streams — memory
// is one chunk whatever the asset size — and a test that allocated half a
// gigabyte to prove it would be measuring the wrong thing.
type countingReader struct{ n int64 }

func (r *countingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("exhausted") // ingest reads exactly size; never reached
	}
	n := int64(len(p))
	if n > r.n {
		n = r.n
	}
	for i := range n {
		p[i] = byte(i)
	}
	r.n -= n
	return int(n), nil
}
