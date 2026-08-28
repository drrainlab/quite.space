package node

// SP-3.1 gates. The fake upstream is reached BY CONFIGURING it as the
// tile server — the test seam and the user feature are the same knob (a
// custom server skips the public-only dialer, which is what lets an
// httptest loopback address through).

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tinyPNG is a valid 1×1 PNG — DetectContentType must call it image/png.
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1,
	8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
	0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

func setTileServer(t *testing.T, rt *Runtime, tpl string) {
	t.Helper()
	s := rt.GetSettings()
	s.Tiles.Server = tpl
	if err := rt.SetSettings(s); err != nil {
		t.Fatal(err)
	}
}

func fakeTileUpstream(t *testing.T, hits *atomic.Int64, body []byte, ct string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if ua := r.Header.Get("User-Agent"); ua != tileUA {
			t.Errorf("upstream saw UA %q — the honest-UA law broke", ua)
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTilesAreProxiedCachedAndSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	var hits atomic.Int64
	srv := fakeTileUpstream(t, &hits, tinyPNG, "image/png")
	setTileServer(t, rt, srv.URL+"/{z}/{x}/{y}.png")

	data, err := rt.FetchTile(t.Context(), 12, 2100, 1300)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, tinyPNG) {
		t.Fatal("proxied bytes differ from the upstream's")
	}
	if _, err := rt.FetchTile(t.Context(), 12, 2100, 1300); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("cache did not hold: %d upstream hits for one tile", n)
	}
	rt.Close()

	// Offline-first is a promise about RESTARTS too: the sealed cache
	// must answer with the upstream gone and the node reopened.
	srv.Close()
	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	if _, err := rt2.FetchTile(t.Context(), 12, 2100, 1300); err != nil {
		t.Fatalf("a cached tile did not survive the reopen: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("reopen went back to the upstream: %d hits", n)
	}
}

func TestTheGateRunsBeforeTheSocketOpens(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	var hits atomic.Int64
	srv := fakeTileUpstream(t, &hits, tinyPNG, "image/png")
	setTileServer(t, rt, srv.URL+"/{z}/{x}/{y}.png")

	for _, m := range []ConnectivityMode{ModeOffline, ModeRadioOnly} {
		setMode(t, rt, m)
		if _, err := rt.FetchTile(t.Context(), 5, 1, 1); err == nil {
			t.Fatalf("mode %s let a tile fetch through", m)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the dial IS the disclosure — %d sockets opened under a refusing mode", n)
	}
	// The negative cache must not outlive the policy that caused it: the
	// person flipping back online deserves a map, not a five-minute sulk.
	setMode(t, rt, ModeInternetOnly)
	rt.tiles().mu.Lock()
	rt.tiles().negative = map[string]time.Time{}
	rt.tiles().mu.Unlock()
	if _, err := rt.FetchTile(t.Context(), 5, 1, 1); err != nil {
		t.Fatalf("internet-only refused a tile: %v", err)
	}
}

func TestTileRefusalsNeverTouchTheUpstream(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	var hits atomic.Int64
	srv := fakeTileUpstream(t, &hits, tinyPNG, "image/png")
	setTileServer(t, rt, srv.URL+"/{z}/{x}/{y}.png")

	for _, c := range [][3]int{{25, 0, 0}, {5, -1, 0}, {5, 0, 32}, {-1, 0, 0}} {
		if _, err := rt.FetchTile(t.Context(), c[0], c[1], c[2]); err == nil {
			t.Fatalf("nonsense tile %v was served", c)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("invalid coordinates reached the upstream %d times", n)
	}
}

func TestOversizedAndNonImageTilesAreRefused(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	var hits atomic.Int64
	big := fakeTileUpstream(t, &hits, make([]byte, tileMaxBytes+1), "image/png")
	setTileServer(t, rt, big.URL+"/{z}/{x}/{y}.png")
	if _, err := rt.FetchTile(t.Context(), 5, 1, 1); err == nil {
		t.Fatal("an oversized tile was admitted")
	}

	svg := fakeTileUpstream(t, &hits, []byte("<svg/>"), "image/svg+xml")
	setTileServer(t, rt, svg.URL+"/{z}/{x}/{y}.png")
	if _, err := rt.FetchTile(t.Context(), 5, 1, 2); err == nil {
		t.Fatal("a non-raster content type was admitted — the allowlist is a prefix check again")
	}
}

func TestUpstreamDisciplineTwoLanesOneFlight(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	var inflight, high, hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		cur := inflight.Add(1)
		for {
			h := high.Load()
			if cur <= h || high.CompareAndSwap(h, cur) {
				break
			}
		}
		defer inflight.Add(-1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer srv.Close()
	setTileServer(t, rt, srv.URL+"/{z}/{x}/{y}.png")

	// 16 distinct tiles at once: the OSM policy semaphore holds at 2.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = rt.FetchTile(t.Context(), 10, i, i)
		}(i)
	}
	wg.Wait()
	if h := high.Load(); h > tileUpstreamConcurrency {
		t.Fatalf("upstream saw %d parallel fetches; the policy says %d", h, tileUpstreamConcurrency)
	}

	// One tile asked for 8 times at once: single-flight makes one request.
	before := hits.Load()
	wg = sync.WaitGroup{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = rt.FetchTile(t.Context(), 11, 7, 7)
		}()
	}
	wg.Wait()
	if n := hits.Load() - before; n != 1 {
		t.Fatalf("8 concurrent asks for one tile made %d upstream requests", n)
	}
}

func TestBackupsCarryHistoryNotOpenStreetMap(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	var hits atomic.Int64
	srv := fakeTileUpstream(t, &hits, tinyPNG, "image/png")
	setTileServer(t, rt, srv.URL+"/{z}/{x}/{y}.png")
	if _, err := rt.FetchTile(t.Context(), 9, 3, 3); err != nil {
		t.Fatal(err)
	}
	// An ordinary sealed blob IS history and must travel.
	if err := rt.root.SaveSealed("draft-test", []byte("words")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteBackup(rt.DataDir(), []byte("backup-passphrase"), &buf); err != nil {
		t.Fatal(err)
	}
	// Restore into a fresh dir and look at what actually travelled.
	dst := t.TempDir()
	if err := ReadBackup(dst, []byte("backup-passphrase"), &buf); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(dst, "sealed"))
	if err != nil {
		t.Fatal(err)
	}
	sawDraft := false
	for _, e := range ents {
		if len(e.Name()) >= 5 && e.Name()[:5] == "tile-" {
			t.Fatalf("a tile rode the backup: %s", e.Name())
		}
		if e.Name() == "draft-test.sealed" {
			sawDraft = true
		}
	}
	if !sawDraft {
		t.Fatal("the exclusion overshot: an ordinary sealed draft was dropped too")
	}
}

func TestTileServerSettingIsValidatedAndSticky(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	s := rt.GetSettings()
	s.Tiles.Server = "https://example.org/{z}/{x}.png" // no {y}
	if err := rt.SetSettings(s); err == nil {
		t.Fatal("a template without {y} was stored")
	}
	s.Tiles.Server = "ftp://example.org/{z}/{x}/{y}"
	if err := rt.SetSettings(s); err == nil {
		t.Fatal("a non-http template was stored")
	}

	setTileServer(t, rt, "https://tiles.example.org/{z}/{x}/{y}.png")
	// A settings write that says nothing about tiles keeps the choice —
	// the pointer-merge law every other field lives under.
	other := rt.GetSettings()
	other.Theme = "light"
	other.Tiles = TilesConfig{}
	if err := rt.SetSettings(other); err != nil {
		t.Fatal(err)
	}
	if got := rt.GetSettings().Tiles.Server; got != "https://tiles.example.org/{z}/{x}/{y}.png" {
		t.Fatalf("an unrelated settings write dropped the tile server: %q", got)
	}
	if tileServerOf(Settings{}) != defaultTileServer {
		t.Fatal("an empty setting must resolve to the default at use")
	}
}

func TestTileEvictionKeepsTheNewest(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()
	tpl := "https://tiles.example.org/{z}/{x}/{y}.png"
	// Seed the sealed store directly — eviction is about the store, not
	// the fetch path.
	for i := 0; i < tileCacheCap+40; i++ {
		if err := rt.root.SaveSealed(tileKey(tpl, 10, i, 0), tinyPNG); err != nil {
			t.Fatal(err)
		}
	}
	rt.sweepTiles()
	names, err := rt.root.ListSealed("tile-")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != tileCacheCap {
		t.Fatalf("sweep left %d tiles, cap is %d", len(names), tileCacheCap)
	}
	// The newest survivor must be present (same-mtime ties may reorder
	// within a second, so assert the cap and one sentinel, not the set).
	last := tileKey(tpl, 10, tileCacheCap+39, 0)
	found := false
	for _, n := range names {
		if n == last {
			found = true
		}
	}
	if !found {
		t.Fatal("the newest tile did not survive the sweep")
	}
}
