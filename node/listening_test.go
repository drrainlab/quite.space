package node

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/listening"
	"github.com/drrainlab/quiet_places/terminals"
	"github.com/drrainlab/quiet_places/transports/relayserver"
)

func mustIID(t *testing.T, s string) [16]byte {
	t.Helper()
	iid, err := hex16(s)
	if err != nil {
		t.Fatal(err)
	}
	return iid
}

// LR-2 node gates: host-only commands, epoch/sequence assignment with
// persistence, and the relay-calibrated shared clock.
func TestListeningHostGateAndCounters(t *testing.T) {
	dir := t.TempDir()
	rt := openRuntime(t, dir, "alice")
	tid, err := rt.CreateSpace("Listening Room")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := rt.CreateAppInstance(tid, "listening-room", "", "",
		map[string]string{"title": "EP demo", "duration_ms": "180000"})
	if err != nil {
		t.Fatal(err)
	}

	// Start session → epoch 1, sequence 1.
	if _, err := rt.AppAction(tid, inst.InstanceID, "command",
		map[string]any{"action": "play", "position_ms": float64(0), "start_session": true}); err != nil {
		t.Fatal(err)
	}
	// Pause → same epoch, sequence 2.
	if _, err := rt.AppAction(tid, inst.InstanceID, "command",
		map[string]any{"action": "pause", "position_ms": float64(5000)}); err != nil {
		t.Fatal(err)
	}
	sp, _ := rt.spaceForTest(tid)
	iid, _ := hex16(inst.InstanceID)
	sess, ok := sp.State.ListeningSession(iid)
	if !ok || !sess.HasCommand {
		t.Fatal("session not folded")
	}
	if sess.Command.SessionEpoch != 1 || sess.Command.Sequence != 2 ||
		sess.Command.Action != "pause" {
		t.Fatalf("wrong counters: %+v", sess.Command)
	}

	// Seek beyond the declared duration → refused at emit.
	if _, err := rt.AppAction(tid, inst.InstanceID, "command",
		map[string]any{"action": "seek", "position_ms": float64(999_999)}); err == nil ||
		!strings.Contains(err.Error(), "duration") {
		t.Fatalf("over-seek must be refused, got %v", err)
	}

	// New session after restart → epoch persisted, bumps to 2.
	rt.Close()
	rt2 := openRuntime(t, dir, "alice")
	defer rt2.Close()
	if _, err := rt2.AppAction(tid, inst.InstanceID, "command",
		map[string]any{"action": "play", "position_ms": float64(0), "start_session": true}); err != nil {
		t.Fatal(err)
	}
	sp2, _ := rt2.spaceForTest(tid)
	sess2, _ := sp2.State.ListeningSession(iid)
	if sess2.Command.SessionEpoch != 2 || sess2.Command.Sequence != 1 {
		t.Fatalf("epoch not persisted across restart: %+v", sess2.Command)
	}
}

// A follower (not the instance creator) is refused the command action even
// though the instance's grants cover the schema — permanent host, v1.
func TestListeningFollowerRefused(t *testing.T) {
	aliceDir, bobDir := t.TempDir(), t.TempDir()
	alice := openRuntime(t, aliceDir, "alice")
	defer alice.Close()
	bob := openRuntime(t, bobDir, "bob")
	defer bob.Close()

	tid, err := alice.CreateSpace("Listening Room")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := alice.CreateAppInstance(tid, "listening-room", "", "",
		map[string]string{"title": "EP demo"})
	if err != nil {
		t.Fatal(err)
	}

	// Move the space to Bob via invite + direct sync (same path as node_test).
	invite, err := alice.MintInvite(tid, bob.Device.ID, bob.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bob.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if err := alice.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := bob.StartLAN("127.0.0.1:0", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := bob.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", alice.LAN().Port)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if withSpace(bob, tid, func(s *terminals.Space) bool {
			_, ok := s.State.AppInstanceByID(mustIID(t, inst.InstanceID))
			return ok
		}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("instance did not sync to bob")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if _, err := bob.AppAction(tid, inst.InstanceID, "command",
		map[string]any{"action": "pause", "position_ms": float64(0)}); err == nil ||
		!strings.Contains(err.Error(), "host") {
		t.Fatalf("follower command must be refused with an honest reason, got %v", err)
	}
}

// The relay answers time queries; the node calibrates and reports the relay
// as THE source (correction 4: one common source, node proxies, never
// substitutes).
func TestRelayTimeCalibration(t *testing.T) {
	srv, port, err := relayserver.StartServer("127.0.0.1:0", relayserver.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	rt := openRuntime(t, t.TempDir(), "alice")
	defer rt.Close()

	_, source, _ := rt.sharedNow()
	if !strings.HasPrefix(source, "node:") {
		t.Fatalf("uncalibrated source must be the node itself: %q", source)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := rt.CalibrateRelayClock(addr); err != nil {
		t.Fatal(err)
	}
	now, source, unc := rt.sharedNow()
	if source != "relay:"+addr {
		t.Fatalf("calibrated source must be the relay: %q", source)
	}
	if now == 0 || unc > 5000 {
		t.Fatalf("implausible calibration: now=%d unc=%d", now, unc)
	}
	_ = listening.MaxEffectiveAheadMS
}
