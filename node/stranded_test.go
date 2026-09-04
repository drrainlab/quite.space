package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

// The protocol's frame cap fits inside one relay item, with the bundle's
// own headroom to spare. This is the fact that makes splitBundles' oversize
// branch unreachable for any frame a verifier would accept — and the fact
// noteStranded stands guard over, should either number move.
func TestTheProtocolFrameCapFitsInsideOneRelayItem(t *testing.T) {
	if signal.MaxFrameLen > maxRelayItem-bundleHeadroom {
		t.Fatalf("a valid frame (up to %d bytes) can exceed one relay item (%d minus %d headroom): "+
			"every later frame of that device would strand relay-only readers — "+
			"noteStranded now names it, but the caps should not have crossed",
			signal.MaxFrameLen, maxRelayItem, bundleHeadroom)
	}
}

// logFrames lists one space's applied frames and ids, index-aligned, the
// way deliverSpaceRouted builds them.
func logFrames(t *testing.T, rt *Runtime, tid id.TerminalID) ([][]byte, []id.EventID) {
	t.Helper()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st, ok := rt.spaces[tid]
	if !ok {
		t.Fatal("unknown space")
	}
	var frames [][]byte
	var ids []id.EventID
	if err := st.space.Log.Replay(func(a eventlog.Applied) error {
		frames = append(frames, a.Frame)
		ids = append(ids, a.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return frames, ids
}

// A frame that cannot travel is NAMED — in the owner's log once, not once
// per two-second cycle, and in the diagnostics bundle with the chain it
// stalls — and the record clears, out loud, when it no longer applies.
func TestAStrandedFrameIsNamedOnceAndCarriedInDiagnostics(t *testing.T) {
	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	tid, err := alice.CreateSpace("Long Memory")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "before", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "the one that would not fit", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	frames, ids := logFrames(t, alice, tid)
	last := len(frames) - 1
	env, err := signal.Decode(frames[last])
	if err != nil {
		t.Fatal(err)
	}

	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)

	alice.noteStranded(tid, frames, ids, []int{last})
	alice.noteStranded(tid, frames, ids, []int{last}) // the next cycle: same verdict
	short := ids[last].Hex()[:8]
	if n := strings.Count(logged.String(), "frame "+short); n != 1 {
		t.Fatalf("the owner's log names the stranded frame %d times, want exactly once:\n%s",
			n, logged.String())
	}
	if !strings.Contains(logged.String(), fmt.Sprintf("cannot read that device past seq %d", env.Sequence)) {
		t.Fatalf("the log states the cause without the consequence:\n%s", logged.String())
	}

	d := alice.RelayDiagnosticsSnapshot()
	if len(d.Stranded) != 1 {
		t.Fatalf("diagnostics report %d stranded frames, want 1: %+v", len(d.Stranded), d.Stranded)
	}
	sf := d.Stranded[0]
	if sf.Event != short || sf.Device != alice.Device.ID.Hex()[:8] || sf.Seq != env.Sequence ||
		sf.Bytes != len(frames[last]) || sf.Reason != strandedReason || sf.Space != tid.Hex()[:8] {
		t.Fatalf("the stranded record is not the frame: %+v (seq %d, %d bytes)", sf, env.Sequence, len(frames[last]))
	}

	// Cleared: the record goes, and the log says so — once.
	alice.noteStranded(tid, frames, ids, nil)
	alice.noteStranded(tid, frames, ids, nil)
	if d := alice.RelayDiagnosticsSnapshot(); len(d.Stranded) != 0 {
		t.Fatalf("a cleared verdict still shows: %+v", d.Stranded)
	}
	if n := strings.Count(logged.String(), "no stranded frames any more"); n != 1 {
		t.Fatalf("the all-clear was logged %d times, want once:\n%s", n, logged.String())
	}
}

// The reader's side: frames parked behind a hole are COUNTED in the room's
// state, and the hole is named. This is the surface the field case lacked
// — a newcomer's empty room and a newcomer's stuck room used to be the same
// screen.
//
// The hole is made by hand (the frame after the next expected one is fed
// to the replica's log directly): every path that feeds a log lands in the
// same Ingest, and this test is about what the log then reports, not about
// which transport dropped the frame.
func TestAHoleInTheChainIsCountedInTheRoom(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	alice := openRuntime(t, t.TempDir(), "alice")
	defer alice.Close()
	bob := openRuntime(t, t.TempDir(), "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Long Memory")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "one", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alice.PushToRelay(addr, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.PullFromRelay(addr); err != nil {
		t.Fatal(err)
	}
	if msgCount(bob, tid) != 1 {
		t.Fatalf("bob holds %d messages after the pull, want 1", msgCount(bob, tid))
	}

	// Alice says two more. Bob receives only the SECOND of them.
	if _, err := alice.Say(tid, "two — lost on the way", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Say(tid, "three", SayOptions{}); err != nil {
		t.Fatal(err)
	}
	frames, _ := logFrames(t, alice, tid)
	tip := withSpace(bob, tid, func(s *terminals.Space) uint64 {
		seq, _, _ := s.Log.ChainTip(alice.Device.ID)
		return seq
	})
	var lost uint64
	bob.mu.Lock()
	for _, f := range frames {
		env, err := signal.Decode(f)
		if err != nil {
			bob.mu.Unlock()
			t.Fatal(err)
		}
		if env.Device != alice.Device.ID || env.Sequence <= tip {
			continue
		}
		if env.Sequence == tip+1 {
			lost = env.Sequence
			continue // the one that does not arrive
		}
		if _, err := bob.spaces[tid].space.Log.Ingest(f); err != nil {
			bob.mu.Unlock()
			t.Fatal(err)
		}
	}
	bob.mu.Unlock()
	if lost == 0 {
		t.Fatal("the test lost nothing — alice's new frames were not found")
	}

	api, err := NewAPIServer(bob, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(api.Handler())
	defer hs.Close()
	get := func(path string, out any) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("X-QP-Token", api.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var state stateResp
	get("/api/spaces/"+tid.Hex()+"/state", &state)
	if state.Pending != 1 || len(state.PendingGaps) != 1 {
		t.Fatalf("state reports pending=%d gaps=%+v — an empty room and a stuck room look alike again",
			state.Pending, state.PendingGaps)
	}
	if g := state.PendingGaps[0]; g.Device != alice.Device.ID.Hex()[:8] || g.WaitingFor != lost || g.Held != 1 {
		t.Fatalf("the gap does not name the lost frame: gap=%+v want device %s seq %d held 1",
			g, alice.Device.ID.Hex()[:8], lost)
	}
	var spaces []spaceResp
	get("/api/spaces", &spaces)
	found := false
	for _, s := range spaces {
		if s.ID == tid.Hex() {
			found = true
			if s.Pending != 1 {
				t.Fatalf("the space list says pending=%d for a room with a parked frame", s.Pending)
			}
		}
	}
	if !found {
		t.Fatal("bob's space list lost the space")
	}
}

// splitBundles hands back WHICH frames it left behind, not how many.
func TestSplitBundlesNamesTheFramesItCannotCarry(t *testing.T) {
	var tid id.TerminalID
	tid[0] = 7
	small := []byte("small enough")
	big := bytes.Repeat([]byte("b"), maxRelayItem)
	out, oversize := splitBundles(tid, [][]byte{small, big, small}, nil, nil, nil, nil)
	if len(oversize) != 1 || oversize[0] != 1 {
		t.Fatalf("oversize indices = %v, want [1]", oversize)
	}
	if len(out) != 1 {
		t.Fatalf("the two small frames should share one body, got %d bodies", len(out))
	}
	if _, none := splitBundles(tid, [][]byte{small}, nil, nil, nil, nil); len(none) != 0 {
		t.Fatalf("nothing oversized, yet %v reported", none)
	}
}
