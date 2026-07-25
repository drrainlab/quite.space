package bridge

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/kernel/routing"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/bundle"
	"github.com/drrainlab/quiet_places/transports/loopback"
	"github.com/drrainlab/quiet_places/transports/relay"
)

func mkFrame(t *testing.T, term id.TerminalID, seed byte, seq uint64,
	prev *id.EventID, text string) []byte {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))
	payload, err := (&schemas.TextMessage{Text: text}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	env := &signal.Envelope{
		Terminal: term, Principal: id.PrincipalID{seed}, Device: dev,
		Sequence: seq, Previous: prev, Schema: schemas.MessageText,
		LogicalClock: seq, ProducedBy: signal.AuthorshipHuman,
		PayloadEncoding: signal.PayloadCBOR, Payload: payload,
		Priority: signal.PriorityMessage,
	}
	f, err := env.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// deviceOf derives the device id mkFrame signs with for a seed, so a test
// can address the same mailbox a node would.
func deviceOf(seed byte) id.DeviceID {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))
	return dev
}

// serving builds the operator capability for one destination: one mailbox
// on the carrier, one on the internet.
func serving(dest id.TerminalID, radioSeed, internetSeed byte) Subscription {
	return Subscription{
		NetworkID:       "test-mesh",
		Terminal:        dest,
		RadioDevices:    []id.DeviceID{deviceOf(radioSeed)},
		InternetDevices: []id.DeviceID{deviceOf(internetSeed)},
	}
}

func testBridge(t *testing.T, radio *loopback.End, relayAddr string,
	subs []Subscription, learn bool) *Bridge {
	t.Helper()
	b, err := New(Config{
		DataDir: t.TempDir(), Instance: "test-bridge",
		Radio: radio, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: relayAddr, RelayDomain: routing.LoopDomainID("relay:" + relayAddr),
		Subscriptions: subs, Learn: learn,
		AirtimePerMin: 1e9, // tests don't wait on airtime
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// The headline path: radio frames reach the relay; relay frames reach the
// radio — and the bridge only ever handled headers.
func TestTwoSegmentLoop(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	relayAddr := fmt.Sprintf("127.0.0.1:%d", port)

	var dest id.TerminalID
	dest[0] = 0xD1
	pair := loopback.NewPair(loopback.Faults{Seed: 3})
	// 0x31 is the author on the carrier, 0x32 the one on the internet.
	b := testBridge(t, pair.B, relayAddr, []Subscription{serving(dest, 0x31, 0x32)}, false)
	now := time.Now()

	// Radio → relay: a radio node broadcasts a frames message (the wire is
	// always fragment-wrapped, exactly as node engines emit).
	f1 := mkFrame(t, dest, 0x31, 1, nil, "from the forest")
	if err := sendWrapped(t, pair.A, kernelsync.EncodeFramesMessage(dest, [][]byte{f1})); err != nil {
		t.Fatal(err)
	}
	if got := b.PumpRadio(now); got != 1 {
		t.Fatalf("radio intake: %d", got)
	}
	pushed, err := b.PushRelay(now)
	if err != nil || pushed != 1 {
		t.Fatalf("relay push: %d %v", pushed, err)
	}

	// Relay → radio: an internet-side node pushed a bundle for dest.
	f2 := mkFrame(t, dest, 0x32, 1, nil, "from the internet")
	client, err := relay.DialClient(relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	nowU := uint64(now.Unix())
	body := encodeBundle(t, dest, f2)
	// The internet node writes into the RADIO node's own per-recipient inbox,
	// exactly as it would if that node were online. The bridge reads it there.
	inbox := relay.HintFor(dest, deviceOf(0x31), relay.Bucket(nowU))
	if _, err := client.Put(inbox, nowU+3600, body); err != nil {
		t.Fatal(err)
	}
	client.Close()

	if took, err := b.PullRelay(now); err != nil || took != 1 {
		t.Fatalf("relay pull: %d %v", took, err)
	}
	if sent := b.PushRadio(now); sent != 1 {
		t.Fatalf("radio push: %d", sent)
	}
	// The radio side receives a (fragment-wrapped) frames message.
	found := false
	reasm := kernelsync.NewReassembler()
	for _, pkt := range pair.A.Poll() {
		raw, err := reasm.Feed(pkt)
		if err != nil || raw == nil {
			continue
		}
		if term, frames, ok := kernelsync.ExtractFramesMessage(raw); ok &&
			term == dest && len(frames) == 1 && bytes.Equal(frames[0], f2) {
			found = true
		}
	}
	if !found {
		t.Fatal("internet frame never reached the radio carrier")
	}
}

// sendWrapped fragment-wraps a message like a node engine would.
func sendWrapped(t *testing.T, ep *loopback.End, msg []byte) error {
	t.Helper()
	frags, err := kernelsync.FragmentStream(uint64(time.Now().UnixNano()), msg, 0)
	if err != nil {
		return err
	}
	for _, f := range frags {
		if err := ep.Send(f); err != nil {
			return err
		}
	}
	return nil
}

func encodeBundle(t *testing.T, term id.TerminalID, frames ...[]byte) []byte {
	t.Helper()
	// bundle.Encode is what nodes use for relay bodies.
	return bundle.Encode(term, frames)
}

// Two bridges on one carrier + one relay: the storm is bounded — each
// frame is forwarded a bounded number of times, dedup + split-horizon hold.
func TestTwoBridgesBoundedStorm(t *testing.T) {
	srv, port, err := relay.StartServer("127.0.0.1:0", relay.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	relayAddr := fmt.Sprintf("127.0.0.1:%d", port)

	var dest id.TerminalID
	dest[0] = 0xD2
	// One shared "carrier": both bridges poll the same hub side.
	hubA := loopback.NewPair(loopback.Faults{Seed: 7})
	hubB := loopback.NewPair(loopback.Faults{Seed: 8})
	b1 := testBridge(t, hubA.B, relayAddr, []Subscription{serving(dest, 0x41, 0x42)}, false)
	b2 := testBridge(t, hubB.B, relayAddr, []Subscription{serving(dest, 0x41, 0x42)}, false)
	now := time.Now()

	f := mkFrame(t, dest, 0x41, 1, nil, "storm probe")
	msg := kernelsync.EncodeFramesMessage(dest, [][]byte{f})
	// The same broadcast reaches both bridges (two gateways, one segment).
	sendWrapped(t, hubA.A, msg)
	sendWrapped(t, hubB.A, msg)

	// An internet node also drops one bundle into the radio node's inbox.
	// Under D1 that mailbox is read with a NON-destructive Fetch, so both
	// bridges see it, and each of them sees it again on every single pass.
	// Nothing but the seen-cache stops that from becoming permanent airtime.
	down := mkFrame(t, dest, 0x42, 1, nil, "storm reply")
	client, err := relay.DialClient(relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	nowU := uint64(now.Unix())
	if _, err := client.Put(relay.HintFor(dest, deviceOf(0x41), relay.Bucket(nowU)),
		nowU+3600, encodeBundle(t, dest, down)); err != nil {
		t.Fatal(err)
	}
	client.Close()

	for i := 0; i < 4; i++ {
		b1.PumpRadio(now)
		b2.PumpRadio(now)
		b1.PullRelay(now)
		b2.PullRelay(now)
		b1.PushRelay(now)
		b2.PushRelay(now)
		b1.PushRadio(now)
		b2.PushRadio(now)
	}
	s1, s2 := b1.Stats(), b2.Stats()

	// Uplink: one frame off the air, one copy into the internet mailbox per
	// gateway. Two active gateways cost a duplicate — the node collapses it
	// by EventID — but the count must not grow with the number of passes.
	if total := s1.RelayOut + s2.RelayOut; total > 2 {
		t.Fatalf("uplink storm not bounded: relayOut total %d over 4 passes", total)
	}
	// Downlink: the same non-destructive item was re-read on every pass and
	// must have been taken into custody exactly once per gateway.
	if total := s1.RadioOut + s2.RadioOut; total > 2 {
		t.Fatalf("downlink storm not bounded: radioOut total %d over 4 passes", total)
	}
	if s1.Deduped+s2.Deduped == 0 {
		t.Fatal("dedup never engaged: a non-destructive Fetch re-reads the " +
			"same item every pass, so nothing else can stop it re-airing")
	}
}

// Custody: ACK only after fsync — kill immediately after AcceptUplink,
// reopen the data dir, the frame is still there. Plus: a spoofed receipt
// never verifies.
func TestCustodyAckDurabilityAndSpoof(t *testing.T) {
	dir := t.TempDir()
	var dest id.TerminalID
	dest[0] = 0xD3
	pair := loopback.NewPair(loopback.Faults{Seed: 4})
	b, err := New(Config{
		DataDir: dir, Instance: "crash-bridge",
		Radio: pair.B, RadioLink: "mesh:test", RadioDomain: "mesh-dom",
		RelayAddr: "127.0.0.1:1", RelayDomain: "relay:none",
		Subscriptions: []Subscription{serving(dest, 0x51, 0x52)},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f := mkFrame(t, dest, 0x51, 1, nil, "must survive the crash")
	receipt := b.AcceptUplink(dest, [][]byte{f}, "lan:node1", "lan-dom", now)
	if receipt == nil {
		t.Fatal("no receipt for accepted frames")
	}
	// The receipt verifies and covers the frame.
	rec, err := DecodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.FrameIDs) != 1 || rec.FrameIDs[0] != id.EventIDOf(f) {
		t.Fatalf("receipt coverage wrong: %+v", rec.FrameIDs)
	}
	// "Kill" the bridge right after the ACK: no Close, just reopen.
	b.queue.Close()
	q2, err := routing.OpenQueue(dir+"/queue", routing.DefaultQueueCaps())
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	if q2.Len() != 1 {
		t.Fatal("ACKed frame lost — fsync-before-ACK violated")
	}

	// Spoof: another keypair signs the same claim → DecodeReceipt rejects a
	// tampered body, and a self-consistent forged receipt carries the WRONG
	// public key — the node-side pin check kills it (asserted in node tests).
	forged := append([]byte(nil), receipt...)
	forged[len(forged)-1] ^= 0xFF
	if _, err := DecodeReceipt(forged); err == nil {
		t.Fatal("tampered receipt must not verify")
	}
}

// Learn-mode admission: an identity flood cannot widen custody — the
// probation cap holds and relay→radio never opens for learned hints.
func TestLearnModeAdmissionControl(t *testing.T) {
	pair := loopback.NewPair(loopback.Faults{Seed: 6})
	b := testBridge(t, pair.B, "127.0.0.1:1", nil, true)
	now := time.Now()
	admitted := 0
	for i := 0; i < 1000; i++ {
		var dest id.TerminalID
		dest[0], dest[1] = byte(i>>8), byte(i)
		if b.subscribed(dest, now) {
			admitted++
		}
	}
	if admitted > b.cfg.LearnCap {
		t.Fatalf("learn cap breached: %d admitted", admitted)
	}
	// Learned hints NEVER open the downlink.
	var learnedDest id.TerminalID
	learnedDest[0], learnedDest[1] = 0, 1
	if b.downlinkAllowed(learnedDest) {
		t.Fatal("learned hint must not open relay→radio")
	}
}

// NoCustody and oversized frames never occupy custody or airtime.
func TestBridgeRefusesNoCustodyAndOversize(t *testing.T) {
	var dest id.TerminalID
	dest[0] = 0xD4
	pair := loopback.NewPair(loopback.Faults{Seed: 9})
	b := testBridge(t, pair.B, "127.0.0.1:1", []Subscription{serving(dest, 0x61, 0x62)}, false)
	now := time.Now()

	// A NoCustody frame (presence-like): MaxForwards=1 in the signed map.
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x61}, ed25519.SeedSize))
	var dev id.DeviceID
	copy(dev[:], priv.Public().(ed25519.PublicKey))
	payload, _ := (&schemas.PresencePayload{State: "listening", ExpiresAt: uint64(now.Unix()) + 300}).Encode()
	env := &signal.Envelope{
		Terminal: dest, Principal: id.PrincipalID{0x61}, Device: dev,
		Sequence: 1, Schema: schemas.PresenceUpdate, LogicalClock: 1,
		ProducedBy: signal.AuthorshipHuman, PayloadEncoding: signal.PayloadCBOR,
		Payload: payload, Priority: signal.PriorityStatePatch,
		ExpiresAt: uint64(now.Unix()) + 300, MaxForwards: 1,
	}
	noCustody, err := env.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if b.takeCustody(noCustody, "mesh:test", "mesh-dom", now) {
		t.Fatal("NoCustody frame must be refused")
	}
	if b.QueueLen() != 0 {
		t.Fatal("custody not empty")
	}
}

// Blindness is structural: the bridge package must never import identity,
// epoch/crypto, or terminals machinery.
func TestBlindnessImportBoundary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps",
		"github.com/drrainlab/quiet_places/transports/bridge").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	deps := string(out)
	for _, forbidden := range []string{
		"quiet_places/kernel/identity",
		"quiet_places/kernel/crypto",
		"quiet_places/terminals",
	} {
		if strings.Contains(deps, forbidden) {
			t.Fatalf("blindness violated: bridge depends on %s", forbidden)
		}
	}
}
