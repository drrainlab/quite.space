package relayserver

// The Relay Status API (docs/RELAY_STATUS_API.md): a read-only, aggregate,
// snapshot-cached view of one relay for a website or an operator
// dashboard. Everything here is either a number the probe already
// answers, a monotonic counter bumped in the message switch, or the one
// thing nothing else can tell an operator — how many devices are parked.
//
// THE CENSUS AND WHY IT IS SHAPED LIKE THIS. The owner asked "how many
// people use this?" and the relay is blind by design: it sees rotating
// hints and ciphertext, never a device, never a person. What it CAN
// count honestly is parks: a device that listens parks its hint set on
// one connection, once per six-hour bucket (the hints rotate with the
// bucket, so a device re-parks then). So:
//
//   parked           connections holding at least one hint, right now
//   distinct_6h      distinct hint SETS parked since the current bucket
//                    began — a device, give or take a device whose set
//                    changed mid-bucket
//   distinct_24h_max the largest distinct_6h of the last four buckets
//
// distinct_6h is kept as a set of SALTED hashes of the sorted hint set.
// The salt is random, lives in memory only, and is thrown away with the
// set when the bucket rotates — so the relay stores nothing that could
// later be matched against a hint, and two relays cannot compare notes.
// Nothing here is per-connection-address, per-hint, or timestamped.
//
// Every value the endpoint serves is a snapshot recomputed at most once
// a minute (privacy rule 2): a public counter on a quiet relay polled at
// 1 Hz would otherwise be an activity oracle.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// stats are the monotonic-since-start counters (RR-7 + status spec).
type stats struct {
	puts, replaces, fetches, collects, probes, rateLimited atomic.Uint64
	bytesStored, bytesServed                               atomic.Uint64
}

// censusWindow is the relay's own six-hour bucket, the same one hints
// rotate on: a device re-parks at the boundary, so a window that lines
// up with it counts each device about once.
const censusWindow = 6 * 3600

type census struct {
	mu     sync.Mutex
	window int64
	salt   [16]byte
	seen   map[[16]byte]struct{}
	// ring holds the distinct counts of the last windows, newest last.
	ring [4]int
}

func (c *census) rotate(now int64) {
	w := now / censusWindow
	if w == c.window && c.seen != nil {
		return
	}
	if c.seen != nil {
		copy(c.ring[:], c.ring[1:])
		c.ring[3] = len(c.seen)
	}
	c.window = w
	c.seen = map[[16]byte]struct{}{}
	_, _ = rand.Read(c.salt[:])
}

// note records one park. The key is a salted digest of the SORTED hint
// set: the same device parking the same set twice in a window counts
// once, and nothing recoverable about the hints survives the window.
func (c *census) note(hints [][]byte, now int64) {
	if len(hints) == 0 {
		return
	}
	sorted := make([]string, len(hints))
	for i, h := range hints {
		sorted[i] = string(h)
	}
	sort.Strings(sorted)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rotate(now)
	d := sha256.New()
	d.Write(c.salt[:])
	for _, h := range sorted {
		d.Write([]byte(h))
	}
	var key [16]byte
	copy(key[:], d.Sum(nil))
	c.seen[key] = struct{}{}
}

func (c *census) snapshot(now int64) (distinct, max24h int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rotate(now)
	distinct = len(c.seen)
	max24h = distinct
	for _, n := range c.ring {
		if n > max24h {
			max24h = n
		}
	}
	return
}

// Status is the response shape, pinned: a new field is a deliberate act
// recorded in docs/RELAY_STATUS_API.md, and the shape test holds it.
type Status struct {
	Relay         string       `json:"relay"`
	Label         string       `json:"label"`
	Protocol      StatusProto  `json:"protocol"`
	Accepting     bool         `json:"accepting"`
	Load          string       `json:"load"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	NowMs         int64        `json:"now_ms"`
	Connections   int          `json:"connections"`
	Listeners     StatusParked `json:"listeners"`
	Store         StatusStore  `json:"store"`
	Traffic       StatusTraf   `json:"traffic"`
}

type StatusProto struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type StatusParked struct {
	Parked        int `json:"parked"`
	Distinct6h    int `json:"distinct_6h"`
	Distinct24hMx int `json:"distinct_24h_max"`
}

type StatusStore struct {
	Items int     `json:"items"`
	KiB   int64   `json:"kib"`
	Fill  float64 `json:"fill"`
}

type StatusTraf struct {
	WindowSeconds    int    `json:"window_seconds"`
	PutsTotal        uint64 `json:"puts_total"`
	ReplacesTotal    uint64 `json:"replaces_total"`
	FetchesTotal     uint64 `json:"fetches_total"`
	CollectsTotal    uint64 `json:"collects_total"`
	ProbesTotal      uint64 `json:"probes_total"`
	RateLimitedTotal uint64 `json:"rate_limited_total"`
	KiBStoredTotal   uint64 `json:"kib_stored_total"`
	KiBServedTotal   uint64 `json:"kib_served_total"`
}

// parkedCount is how many connections hold at least one hint right now —
// derived from the registry, so it cannot drift from it.
func (s *Server) parkedCount() int {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	seen := map[*connState]struct{}{}
	for _, set := range s.listeners {
		for cs := range set {
			seen[cs] = struct{}{}
		}
	}
	return len(seen)
}

// StatusSnapshot computes the status NOW. The HTTP handler is what caches
// it; callers that want a fresh value (the metrics line, tests) get one.
func (s *Server) StatusSnapshot(name, label string) Status {
	now := time.Now()
	d6, d24 := s.census.snapshot(now.Unix())
	fill := s.store.FillRatio()
	return Status{
		Relay: name, Label: label,
		Protocol:      StatusProto{Min: relay.RelayProtocolMin, Max: relay.RelayProtocolVersion},
		Accepting:     true,
		Load:          s.loadClass(),
		UptimeSeconds: int64(now.Sub(s.started) / time.Second),
		NowMs:         now.UnixMilli(),
		Connections:   s.Conns(),
		Listeners:     StatusParked{Parked: s.parkedCount(), Distinct6h: d6, Distinct24hMx: d24},
		Store: StatusStore{
			Items: s.Pending(),
			KiB:   s.PendingBytes() / 1024,
			Fill:  float64(int(fill*100+0.5)) / 100,
		},
		Traffic: StatusTraf{
			WindowSeconds:    60,
			PutsTotal:        s.st.puts.Load(),
			ReplacesTotal:    s.st.replaces.Load(),
			FetchesTotal:     s.st.fetches.Load(),
			CollectsTotal:    s.st.collects.Load(),
			ProbesTotal:      s.st.probes.Load(),
			RateLimitedTotal: s.st.rateLimited.Load(),
			KiBStoredTotal:   s.st.bytesStored.Load() / 1024,
			KiBServedTotal:   s.st.bytesServed.Load() / 1024,
		},
	}
}

// StatusHandler serves GET /status and GET /healthz over plain HTTP with
// the spec's headers, computing at most one snapshot per `hold`.
func (s *Server) StatusHandler(name, label string, hold time.Duration) http.Handler {
	if hold <= 0 {
		hold = 60 * time.Second
	}
	var mu sync.Mutex
	var cached []byte
	var cachedAt time.Time
	snapshot := func() []byte {
		mu.Lock()
		defer mu.Unlock()
		if cached != nil && time.Since(cachedAt) < hold {
			return cached
		}
		b, err := json.Marshal(s.StatusSnapshot(name, label))
		if err != nil {
			return []byte("{}")
		}
		cached, cachedAt = b, time.Now()
		return cached
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "public, max-age="+itoa(int(hold/time.Second)))
		switch r.URL.Path {
		case "/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(snapshot())
		case "/healthz":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok\n"))
		default:
			http.NotFound(w, r)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
