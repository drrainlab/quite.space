package assets

// The streaming reader must be byte-identical to the full assembly at
// every offset — a player's Range requests land anywhere, cross chunk
// boundaries mid-read, and seek backwards while buffering.

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

func TestReaderMatchesRetrieveAtEveryShape(t *testing.T) {
	store := newMemStore()
	// Three chunk-shape regimes: sub-chunk, exact multiple, ragged tail.
	for _, size := range []int{1000, 64 << 10, 3*(64<<10) + 777} {
		payload := make([]byte, size)
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		ref, err := IngestBytes(store, payload, Metadata{MediaType: "video/mp4"})
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyDigest(store, ref); err != nil {
			t.Fatal(err)
		}
		rd, err := Open(store, ref)
		if err != nil {
			t.Fatal(err)
		}
		if rd.Size() != int64(size) {
			t.Fatalf("size %d != %d", rd.Size(), size)
		}
		// Full sequential read.
		got, err := io.ReadAll(rd)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("sequential read diverged at size %d", size)
		}
		// Range shapes a player actually issues: head probe, tail probe,
		// chunk-boundary straddle, backward seek.
		probes := [][2]int64{{0, 512}, {int64(size) - 100, 100},
			{(64 << 10) - 33, 66}, {7, 1}}
		for _, pr := range probes {
			off, n := pr[0], pr[1]
			if off < 0 || off+n > int64(size) {
				continue
			}
			if _, err := rd.Seek(off, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(rd, buf); err != nil {
				t.Fatalf("range %d+%d at size %d: %v", off, n, size, err)
			}
			if !bytes.Equal(buf, payload[off:off+n]) {
				t.Fatalf("range %d+%d diverged at size %d", off, n, size)
			}
		}
	}
}
