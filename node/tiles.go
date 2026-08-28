package node

// SP-3.1 basemap tiles (ADR-032: "the basemap is a courtesy; claims stay
// sovereign"). The node is the ONLY way tiles reach the UI — the page's
// CSP (img-src 'self') forbids talking to a tile server directly, and
// that constraint is kept on purpose: it makes this file the single
// place where the three laws live.
//
//  1. The dial is the disclosure. A tile request tells a third party
//     which square of the world this person is looking at — the most
//     location-revealing outbound traffic in the product. So the
//     connectivity gate runs BEFORE any socket opens, and offline/radio
//     modes serve cache or nothing.
//  2. The cache is a location diary. What somebody has looked at is as
//     private as what they drafted — tiles are stored through the sealed
//     store (encrypted at rest) and excluded from backups (a backup is a
//     person's history, not a mirror of OpenStreetMap).
//  3. Tiles never ride the protocol: not in events, bundles, radio
//     frames, or backups. They are refetchable world, not history.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"crypto/sha256"
	"encoding/hex"
)

// defaultTileServer is resolved at USE, never persisted: an empty
// Settings.Tiles.Server means "no preference expressed" (the relay-mode
// lesson), and the default must be free to change in a later build.
const defaultTileServer = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"

const (
	// tileUA follows the honest-UA law (see unfurlUA): name the app so a
	// host that dislikes the traffic can say so. The OSM tile usage
	// policy REQUIRES a distinctive User-Agent; a generic or
	// browser-spoofed one is grounds for a ban.
	tileUA = "QuietSpaces/0.1 (+https://github.com/drrainlab/quiet_places; basemap tiles, cached locally)"
	// tileMaxZ bounds the tile pyramid; OSM serves up to 19.
	tileMaxZ = 19
	// tileMaxBytes bounds one tile (largest real OSM tiles are ~100 KiB;
	// this also keeps every tile under the sealed store's 1 MiB cap).
	tileMaxBytes = 512 << 10
	// tileFetchTimeout bounds one upstream request.
	tileFetchTimeout = 10 * time.Second
	// tileUpstreamConcurrency is an OSM tile usage policy number: at most
	// two download threads. A semaphore, not a suggestion.
	tileUpstreamConcurrency = 2
	// tileNegativeTTL is how long a failed fetch is remembered so an
	// offline pan does not hammer the upstream (or the airwaves the
	// failure came from).
	tileNegativeTTL = 5 * time.Minute
	// tileCacheCap bounds the disk cache (~250-350 MB worst case). "No
	// eviction" would be disk-first, not offline-first.
	tileCacheCap = 8192
	// tileSweepEvery: the eviction sweep walks the sealed dir (a full
	// ReadDir), so it runs once per this many saves, not per save.
	tileSweepEvery = 256
)

// ErrBadTileServer is a settings mistake, not a server fault (400).
type ErrBadTileServer struct{ Why string }

func (e ErrBadTileServer) Error() string { return "node: tile server template: " + e.Why }

// tileServerOf resolves the template in effect.
func tileServerOf(s Settings) string {
	if s.Tiles.Server != "" {
		return s.Tiles.Server
	}
	return defaultTileServer
}

// validateTileServer refuses an unusable template at the settings door.
// "" is valid — it means the default.
func validateTileServer(tpl string) error {
	if tpl == "" {
		return nil
	}
	for _, ph := range []string{"{z}", "{x}", "{y}"} {
		if !strings.Contains(tpl, ph) {
			return ErrBadTileServer{Why: "missing " + ph}
		}
	}
	sample := tileURL(tpl, 1, 0, 0)
	u, err := url.Parse(sample)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ErrBadTileServer{Why: "not an http(s) URL"}
	}
	return nil
}

func tileURL(tpl string, z, x, y int) string {
	r := strings.NewReplacer("{z}", fmt.Sprint(z), "{x}", fmt.Sprint(x), "{y}", fmt.Sprint(y))
	return r.Replace(tpl)
}

// tileKey is the sealed-store name: flat (the store's namespace has no
// slashes) and prefixed with a hash of the server template, so switching
// servers can never serve another provider's stale pixels.
func tileKey(tpl string, z, x, y int) string {
	h := sha256.Sum256([]byte(tpl))
	return fmt.Sprintf("tile-%s-%d-%d-%d", hex.EncodeToString(h[:4]), z, x, y)
}

// tileState is the runtime's tile machinery, created lazily on first use.
type tileState struct {
	mu       sync.Mutex
	client   *http.Client
	negative map[string]time.Time      // key → retry-after
	inflight map[string][]chan tileRes // single-flight followers
	sem      chan struct{}             // upstream concurrency, cap 2
	saves    int                       // saves since the last eviction sweep
}

type tileRes struct {
	data []byte
	err  error
}

// internetGate refuses an internet-bound side request before any socket
// opens. TransportRelay is the existing name for "the internet as a way
// out of this device": offline and radio-only refuse it, auto and
// internet-only permit it — exactly the truth table tiles need. (unfurl
// predates this gate and does not consult it yet; ADR-032 names that as
// the place its fix belongs.)
func (r *Runtime) internetGate() error {
	if r.anySpaceAllows(TransportRelay) {
		return nil
	}
	return ErrTransportBlocked{Transport: TransportRelay, Mode: r.connectivity().Mode}
}

// errTileOffline says "the policy or the network said no, and the cache
// has nothing" — the UI's cue to fall back to paper, never a spinner.
var errTileOffline = errors.New("node: tile not cached and not fetchable")

func (r *Runtime) tiles() *tileState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tileState == nil {
		r.tileState = &tileState{
			negative: map[string]time.Time{},
			inflight: map[string][]chan tileRes{},
			sem:      make(chan struct{}, tileUpstreamConcurrency),
			client: &http.Client{
				// Keep-alives ON — tiles arrive in bursts of dozens from
				// one host, the opposite of unfurl's one-shot threat
				// model. Still no cookie jar: a basemap must never carry
				// a session anywhere.
				Timeout: tileFetchTimeout,
				Jar:     nil,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if len(via) >= 2 {
						return errors.New("node: tile redirect chain too long")
					}
					if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
						return fmt.Errorf("node: tile redirect to %q refused", req.URL.Scheme)
					}
					return nil
				},
			},
		}
	}
	return r.tileState
}

// FetchTile returns one basemap tile: sealed cache first, then — gate
// permitting — the configured upstream, cached on the way through.
func (r *Runtime) FetchTile(ctx context.Context, z, x, y int) ([]byte, error) {
	if z < 0 || z > tileMaxZ || x < 0 || y < 0 || x >= 1<<uint(z) || y >= 1<<uint(z) {
		return nil, fmt.Errorf("node: no such tile %d/%d/%d", z, x, y)
	}
	s := r.GetSettings()
	tpl := tileServerOf(s)
	key := tileKey(tpl, z, x, y)

	// Cache first — offline-first means the disk answers before any
	// policy question is even asked. A Load also bumps mtime (best
	// effort) so the eviction sweep is genuinely LRU.
	if data, err := r.root.LoadSealed(key); err == nil {
		now := time.Now()
		_ = os.Chtimes(r.sealedFilePath(key), now, now)
		return data, nil
	}

	ts := r.tiles()
	ts.mu.Lock()
	if until, ok := ts.negative[key]; ok {
		if time.Now().Before(until) {
			ts.mu.Unlock()
			return nil, errTileOffline
		}
		delete(ts.negative, key)
	}
	// Single-flight: the map view asks for the same tile from several
	// draws at once; one upstream request feeds them all.
	if _, running := ts.inflight[key]; running {
		ch := make(chan tileRes, 1)
		ts.inflight[key] = append(ts.inflight[key], ch)
		ts.mu.Unlock()
		select {
		case res := <-ch:
			return res.data, res.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ts.inflight[key] = []chan tileRes{}
	ts.mu.Unlock()

	data, err := r.fetchTileUpstream(ctx, ts, tpl, key, z, x, y)

	ts.mu.Lock()
	followers := ts.inflight[key]
	delete(ts.inflight, key)
	if err != nil {
		// Remember the failure so an offline pan does not retry per frame.
		if len(ts.negative) < 4096 {
			ts.negative[key] = time.Now().Add(tileNegativeTTL)
		}
	}
	ts.mu.Unlock()
	for _, ch := range followers {
		ch <- tileRes{data: data, err: err}
	}
	return data, err
}

func (r *Runtime) fetchTileUpstream(ctx context.Context, ts *tileState, tpl, key string, z, x, y int) ([]byte, error) {
	// LOOK AGAIN BEFORE SPENDING A REQUEST. Between this caller's cache
	// miss and the moment it claimed the tile, another one may have
	// finished the very same fetch and written it: the leader saves and
	// THEN drops its in-flight entry, so a straggler that missed the cache
	// a moment earlier finds no leader to follow and becomes a second one.
	//
	// The window is narrow on fast iron and wide under the race detector,
	// which is where CI caught it. It is not a correctness bug — the
	// second request returns the same pixels — but it is a request to
	// somebody's donated capacity that nobody needed, and the OSM policy
	// we chose to honour is made of exactly such requests. Panning a map
	// asks for the same tile from two draws all the time.
	if data, err := r.root.LoadSealed(key); err == nil {
		return data, nil
	}
	// THE GATE RUNS BEFORE THE SOCKET OPENS — the dial itself is the
	// disclosure (connectivity doctrine). Offline/radio-only: cache or
	// nothing, no exception for "just one tile".
	if err := r.internetGate(); err != nil {
		return nil, errTileOffline
	}
	select {
	case ts.sem <- struct{}{}:
		defer func() { <-ts.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tileURL(tpl, z, x, y), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", tileUA)
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp")

	client := ts.client
	if tpl == defaultTileServer {
		// The default upstream gets the public-only dialer (SSRF hygiene,
		// reused from unfurl). A CUSTOM server is the owner's own
		// configuration — the LLM.BaseURL precedent — and may
		// legitimately point at localhost or the LAN, so it dials plain.
		client = r.tilePublicClient(ts)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errTileOffline
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("node: tile upstream said %d", resp.StatusCode)
	}
	// Allowlist, not prefix: image/svg+xml through a prefix check is the
	// exact mistake the asset headers already teach against.
	ct := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch strings.TrimSpace(ct) {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return nil, fmt.Errorf("node: tile upstream sent %q, not a raster image", ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, tileMaxBytes+1))
	if err != nil {
		return nil, errTileOffline
	}
	if len(data) > tileMaxBytes {
		return nil, errors.New("node: tile larger than the cache admits")
	}
	if err := r.root.SaveSealed(key, data); err != nil {
		// A full disk must not break the map that is on screen: the tile
		// is still served, only uncached.
		return data, nil
	}
	r.maybeSweepTiles(ts)
	return data, nil
}

// tilePublicClient wraps the shared client with the public-only dialer.
func (r *Runtime) tilePublicClient(ts *tileState) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: publicOnly}
	c := *ts.client
	c.Transport = &http.Transport{DialContext: d.DialContext, ForceAttemptHTTP2: true}
	return &c
}

// sealedFilePath mirrors the sealed store's layout for mtime bumps and
// the eviction sweep. dataDir IS the storage root's directory — the two
// are opened on the same path — so this stays a plain join.
func (r *Runtime) sealedFilePath(name string) string {
	return filepath.Join(r.dataDir, "sealed", name+".sealed")
}

// maybeSweepTiles evicts oldest-mtime tiles beyond the cap, once per
// tileSweepEvery saves — the sweep is a full ReadDir, so it earns its
// keep by running rarely. On filesystems that refuse Chtimes the order
// degrades to FIFO, which is acceptable for a refetchable cache.
func (r *Runtime) maybeSweepTiles(ts *tileState) {
	ts.mu.Lock()
	ts.saves++
	due := ts.saves >= tileSweepEvery
	if due {
		ts.saves = 0
	}
	ts.mu.Unlock()
	if !due {
		return
	}
	go r.sweepTiles()
}

func (r *Runtime) sweepTiles() {
	names, err := r.root.ListSealed("tile-")
	if err != nil || len(names) <= tileCacheCap {
		return
	}
	type aged struct {
		name string
		at   time.Time
	}
	byAge := make([]aged, 0, len(names))
	for _, n := range names {
		info, err := os.Stat(r.sealedFilePath(n))
		if err != nil {
			continue
		}
		byAge = append(byAge, aged{name: n, at: info.ModTime()})
	}
	sort.Slice(byAge, func(i, j int) bool { return byAge[i].at.Before(byAge[j].at) })
	for i := 0; i < len(byAge)-tileCacheCap; i++ {
		_ = r.root.DeleteSealed(byAge[i].name)
	}
}
