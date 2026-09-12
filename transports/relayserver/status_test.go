package relayserver

// The Relay Status API, held to its own spec (docs/RELAY_STATUS_API.md):
// one source of truth with the probe, a snapshot that really holds for
// its window, a pinned shape, and a census that counts parks — not hints,
// not addresses, not people.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestStatusSnapshotAgreesWithTheProbe(t *testing.T) {
	srv, _, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	st := srv.StatusSnapshot("t", "test relay")
	if st.Protocol.Min != relay.RelayProtocolMin || st.Protocol.Max != relay.RelayProtocolVersion {
		t.Fatalf("protocol range %+v disagrees with the wire probe", st.Protocol)
	}
	if !st.Accepting || st.Load != srv.loadClass() {
		t.Fatalf("accepting/load %v/%q disagree with the probe", st.Accepting, st.Load)
	}
	if st.Relay != "t" || st.Label != "test relay" {
		t.Fatalf("operator strings lost: %+v", st)
	}
}

func TestStatusHandlerHoldsOneSnapshotPerWindow(t *testing.T) {
	srv, _, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	h := srv.StatusHandler("t", "", time.Minute)
	get := func() Status {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
			rec.Header().Get("Cache-Control") != "public, max-age=60" {
			t.Fatalf("spec headers missing: %v", rec.Header())
		}
		var st Status
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	a := get()
	time.Sleep(5 * time.Millisecond)
	b := get()
	if a.NowMs != b.NowMs {
		t.Fatalf("two calls inside the window computed two snapshots: %d vs %d", a.NowMs, b.NowMs)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST answered %d, want 405", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
	}
}

func TestStatusShapeIsPinned(t *testing.T) {
	srv, _, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	b, _ := json.Marshal(srv.StatusSnapshot("", ""))
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"accepting", "connections", "label", "listeners", "load", "now_ms",
		"protocol", "relay", "store", "traffic", "uptime_seconds"}
	if len(keys) != len(want) {
		t.Fatalf("shape drifted: %v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("shape drifted: %v", keys)
		}
	}
	var lst map[string]int
	if err := json.Unmarshal(top["listeners"], &lst); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"parked", "distinct_6h", "distinct_24h_max"} {
		if _, ok := lst[k]; !ok {
			t.Fatalf("listeners block lost %q: %v", k, lst)
		}
	}
}

func TestCensusCountsParksNotHints(t *testing.T) {
	var c census
	now := int64(1_800_000_000)
	a := [][]byte{[]byte("hint-a"), []byte("hint-b")}
	aShuffled := [][]byte{[]byte("hint-b"), []byte("hint-a")}
	b := [][]byte{[]byte("hint-c")}
	c.note(a, now)
	c.note(aShuffled, now+10) // the same device re-parking the same set
	c.note(b, now+20)
	if d, _ := c.snapshot(now + 30); d != 2 {
		t.Fatalf("two devices parked, census says %d", d)
	}
	// The window rotates with the six-hour bucket: the count moves into
	// the ring, the set and the salt are gone, and the 24h max remembers.
	c.note(b, now+censusWindow+1)
	d, mx := c.snapshot(now + censusWindow + 2)
	if d != 1 || mx != 2 {
		t.Fatalf("after rotation: distinct %d (want 1), max24h %d (want 2)", d, mx)
	}
	if _, ok := c.seen[[16]byte{}]; ok {
		t.Fatal("an unsalted key would mean a hint is recoverable")
	}
}

func TestParkedCountFollowsTheRegistry(t *testing.T) {
	srv, _, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	one, two := &connState{}, &connState{}
	h := make([]byte, relay.HintLen)
	srv.repark(one, [][]byte{h})
	srv.repark(two, [][]byte{h})
	if n := srv.parkedCount(); n != 2 {
		t.Fatalf("two parked connections, count %d", n)
	}
	srv.unpark(one)
	if n := srv.parkedCount(); n != 1 {
		t.Fatalf("one left after unpark, count %d", n)
	}
}
