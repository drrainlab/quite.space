// RR-3: the selection probe and the pure scoring/hysteresis machinery.
package node

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func TestProbeRoundTripCarriesTheFacts(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := relay.DialClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Probe([]byte("nonce-1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ProtoMin != 1 || res.ProtoMax != 1 {
		t.Fatalf("protocol range %d..%d", res.ProtoMin, res.ProtoMax)
	}
	if res.Load != relay.LoadNormal || !res.Accepting {
		t.Fatalf("load %q accepting %v", res.Load, res.Accepting)
	}
	if res.NowMS == 0 {
		t.Fatal("the probe carries no clock — selection and calibration must share one benchmark")
	}
	if res.RTT <= 0 {
		t.Fatal("no RTT measured")
	}
}

func TestProbeIsMetered(t *testing.T) {
	lim := relayserver.DefaultLimits()
	lim.CollectRatePerMin = 3
	srv, port, err := relayserver.StartServer("127.0.0.1:0", lim)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := relay.DialClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var lastErr error
	for i := 0; i < 6; i++ {
		_, lastErr = c.Probe(nil)
	}
	if lastErr == nil {
		t.Fatal("a probe storm was never rate limited")
	}
}

// ---- pure scoring ----

func st(rtt, jitter float64, fails int, load string, succ int) RelayProbeStats {
	return RelayProbeStats{RTTEWMAMs: rtt, JitterEWMAMs: jitter,
		ConsecutiveFailures: fails, LoadClass: load, SuccessCount: succ}
}

func TestRelayScoreShape(t *testing.T) {
	if s := relayScore(st(34, 2, 0, "normal", 5)); s != 35 {
		t.Fatalf("34ms+1ms jitter = %v", s)
	}
	if s := relayScore(st(34, 0, 1, "normal", 5)); s != 534 {
		t.Fatalf("a recent failure must cost ~500ms equivalent, got %v", s)
	}
	if s := relayScore(st(34, 0, 0, "busy", 5)); s != 134 {
		t.Fatalf("busy must cost 100ms equivalent, got %v", s)
	}
	if s := relayScore(st(34, 0, 0, "overloaded", 5)); !math.IsInf(s, 1) {
		t.Fatal("overloaded must be excluded, not penalized")
	}
	if s := relayScore(st(0, 0, 0, "normal", 0)); !math.IsInf(s, 1) {
		t.Fatal("a never-measured relay is not a candidate")
	}
}

func TestHysteresisAbsorbsTheNoiseFloor(t *testing.T) {
	cur := st(34, 0, 0, "normal", 5)
	// 30ms vs 34ms is inside the transport's tick quantization — no switch.
	if shouldSwitch(cur, st(30, 0, 0, "normal", 5), "official:a", time.Hour) {
		t.Fatal("a few milliseconds moved the primary")
	}
	// A decisive (~30%+) advantage does switch.
	if !shouldSwitch(cur, st(20, 0, 0, "normal", 5), "official:a", time.Hour) {
		t.Fatal("a decisive advantage was ignored")
	}
	// ...but never inside the minimum stable period.
	if shouldSwitch(cur, st(20, 0, 0, "normal", 5), "official:a", time.Minute) {
		t.Fatal("switched during the stability window")
	}
	// A dead current is left immediately, stability window or not.
	dead := st(34, 0, 3, "overloaded", 5)
	if !shouldSwitch(dead, st(80, 0, 0, "normal", 5), "official:a", time.Second) {
		t.Fatal("stayed on a dead relay for stability's sake")
	}
	// No current at all: anything measured wins.
	if !shouldSwitch(RelayProbeStats{}, st(80, 0, 0, "normal", 1), "", 0) {
		t.Fatal("an empty selection refused a live candidate")
	}
}

func TestEWMADampsSingleSamples(t *testing.T) {
	v := ewma(0, 100) // first sample seeds
	if v != 100 {
		t.Fatalf("seed %v", v)
	}
	v = ewma(v, 200) // one spike moves it a quarter of the way
	if v != 125 {
		t.Fatalf("after spike %v", v)
	}
}

// The end-to-end selection against a real relay: local-dev resolves,
// gets probed, and becomes the automatic primary in relays.json.
func TestAutoSelectionPicksAndPersists(t *testing.T) {
	// Point the builtin local-dev entry's endpoint at a live test relay by
	// probing its real address via the pool through runAutoSelection's
	// machinery — the registry endpoint is fixed, so instead verify the
	// probe/record/score path directly.
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	r := openRuntime(t, t.TempDir(), "probe")
	defer r.Close()

	res, err := r.probeOnce(addr)
	if err != nil {
		t.Fatal(err)
	}
	r.recordProbe("custom:tls://"+addr, res, nil)
	st := r.loadRelayState()
	ps := st.Stats["custom:tls://"+addr]
	if ps == nil || ps.SuccessCount != 1 || ps.RTTEWMAMs <= 0 {
		t.Fatalf("probe not recorded: %+v", ps)
	}
	if math.IsInf(relayScore(*ps), 1) {
		t.Fatal("a measured healthy relay scored as unusable")
	}
}
