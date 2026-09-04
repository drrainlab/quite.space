package node

// The instrument door (QI-B1 Ф2), gated in CI: a synthetic instrument —
// the externalDevice fixture over a REAL TLS dial — knocks on the LAN
// listener with the node's cert fingerprint as its binding, and the four
// laws get their pins: the knock earns the epochs
// and a frame reaches the reducer; the link scopes to its one space; a
// bad knock is a silent close with no epochs and no distinguishable
// refusal; a rotation is pushed to the live conn.

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
	"time"

	kernelsync "github.com/drrainlab/quiet_places/kernel/sync"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/transports/lan"
)

// doorClient is the synthetic board's half of the wire.
type doorClient struct {
	conn  *lan.Conn
	reasm *kernelsync.Reassembler
}

func dialDoor(t *testing.T, rt *Runtime) *doorClient {
	t.Helper()
	n, err := lan.NewNode()
	if err != nil {
		t.Fatal(err)
	}
	c, err := n.Dial(fmt.Sprintf("127.0.0.1:%d", rt.LAN().Port))
	if err != nil {
		t.Skipf("could not dial the node: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return &doorClient{conn: c, reasm: kernelsync.NewReassembler()}
}

func (dc *doorClient) knock(t *testing.T, ext *externalDevice, space id.TerminalID) {
	t.Helper()
	fp, ok := dc.conn.PeerCertFingerprint()
	if !ok {
		t.Fatal("the dialed conn cannot fingerprint the node's certificate")
	}
	msg := append([]byte(instrDoorLabel+":"), fp[:]...)
	msg = append(msg, space[:]...)
	sig := ed25519.Sign(ext.dev.SignKey(), msg)
	dc.send(t, kernelsync.EncodeEpochReqMessage(
		encodeEpochReqPayload(space, ext.dev.ID, sig)))
}

func (dc *doorClient) send(t *testing.T, msg []byte) {
	t.Helper()
	pkts, err := kernelsync.FragmentStream(0, msg, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkts {
		if err := dc.conn.Send(p); err != nil {
			t.Fatal(err)
		}
	}
}

// collect drains complete messages off the wire for the given window.
func (dc *doorClient) collect(window time.Duration) [][]byte {
	var out [][]byte
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		for _, pkt := range dc.conn.Poll() {
			raw, err := dc.reasm.Feed(pkt)
			if errors.Is(err, kernelsync.ErrNotFragment) {
				raw, err = pkt, nil
			}
			if err != nil || raw == nil {
				continue
			}
			out = append(out, raw)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return out
}

func (dc *doorClient) waitEpochs(t *testing.T, window time.Duration) (id.TerminalID, [][]byte, uint64) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		for _, raw := range dc.collect(50 * time.Millisecond) {
			if p, ok := kernelsync.ExtractEpochs(raw); ok {
				space, frames, unix, err := decodeEpochsPayload(p)
				if err != nil {
					t.Fatal(err)
				}
				return space, frames, unix
			}
		}
	}
	t.Fatal("no epochs arrived at the door")
	panic("unreachable")
}

func doorRuntime(t *testing.T) (*Runtime, id.TerminalID, *externalDevice) {
	t.Helper()
	rt := openRuntime(t, t.TempDir(), "owner")
	t.Cleanup(rt.Close)
	if err := rt.StartLAN("127.0.0.1:0", ""); err != nil {
		t.Skipf("no LAN in this environment: %v", err)
	}
	tid, err := rt.CreateSpace("workshop")
	if err != nil {
		t.Fatal(err)
	}
	ext := newExternalDevice(t, rt.PrincipalID, tid, "Heltec")
	prov, _, err := rt.AttachInstrumentByEnrollment(tid, ext.enroll, uint64(time.Now().Unix()))
	if err != nil {
		t.Fatal(err)
	}
	ext.provision(t, prov)
	return rt, tid, ext
}

func TestTheDoorAnswersACertifiedKnockAndCarriesAFrame(t *testing.T) {
	rt, tid, ext := doorRuntime(t)
	dc := dialDoor(t, rt)
	dc.knock(t, ext, tid)

	space, frames, unix := dc.waitEpochs(t, 5*time.Second)
	if space != tid {
		t.Fatal("epochs answer names the wrong space")
	}
	if len(frames) == 0 {
		t.Fatal("a sealed space answered with no epoch frame")
	}
	if unix == 0 {
		t.Fatal("the clock floor is missing")
	}

	// The board absorbs the freight and speaks: a frame up the same pipe.
	for _, f := range frames {
		_ = ext.replica.AbsorbInstrumentEpochFrame(f, rt.PrincipalID)
	}
	frame := ext.reading(t, 231, uint64(time.Now().Unix()))
	dc.send(t, kernelsync.EncodeFramesMessage(tid, [][]byte{frame}))

	deadline := time.Now().Add(5 * time.Second)
	for {
		if mag, ok := guestTemperature(t, rt, tid, ext.part.TerminalID); ok {
			if mag != 231 {
				t.Fatalf("reducer holds %d, wire carried 231", mag)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the frame never reached the reducer")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTheDoorScopesTheLinkToItsSpace(t *testing.T) {
	rt, tid, ext := doorRuntime(t)
	// A second space with life in it: its summaries must never touch the
	// instrument's wire — a summary names its terminal in plaintext.
	other, err := rt.CreateSpace("private-life")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Say(other, "not for the balcony sensor", SayOptions{}); err != nil {
		t.Fatal(err)
	}

	dc := dialDoor(t, rt)
	dc.knock(t, ext, tid)
	dc.waitEpochs(t, 5*time.Second)

	for _, raw := range dc.collect(2 * time.Second) {
		if term, ok := kernelsync.PeekTerminal(raw); ok && term == other {
			t.Fatal("another space's traffic crossed the instrument's wire")
		}
	}
}

func TestTheDoorIsNotAnOracle(t *testing.T) {
	rt, tid, ext := doorRuntime(t)

	refused := func(name string, knock func(dc *doorClient)) {
		dc := dialDoor(t, rt)
		knock(dc)
		// One refusal, indistinguishable: no epochs, and the conn dies.
		for _, raw := range dc.collect(1500 * time.Millisecond) {
			if _, ok := kernelsync.ExtractEpochs(raw); ok {
				t.Fatalf("%s: the door answered a bad knock", name)
			}
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if closed, _ := dc.conn.Closed(); closed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the conn outlived a refused knock", name)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	refused("bad signature", func(dc *doorClient) {
		sig := make([]byte, ed25519.SignatureSize)
		dc.send(t, kernelsync.EncodeEpochReqMessage(
			encodeEpochReqPayload(tid, ext.dev.ID, sig)))
	})
	refused("unknown space", func(dc *doorClient) {
		var ghost id.TerminalID
		ghost[0] = 0xEE
		fp, _ := dc.conn.PeerCertFingerprint()
		msg := append([]byte(instrDoorLabel+":"), fp[:]...)
		msg = append(msg, ghost[:]...)
		dc.send(t, kernelsync.EncodeEpochReqMessage(
			encodeEpochReqPayload(ghost, ext.dev.ID, ed25519.Sign(ext.dev.SignKey(), msg))))
	})
	refused("uncertified device", func(dc *doorClient) {
		stranger := newExternalDevice(t, rt.PrincipalID, tid, "Impostor")
		fp, _ := dc.conn.PeerCertFingerprint()
		msg := append([]byte(instrDoorLabel+":"), fp[:]...)
		msg = append(msg, tid[:]...)
		dc.send(t, kernelsync.EncodeEpochReqMessage(
			encodeEpochReqPayload(tid, stranger.dev.ID, ed25519.Sign(stranger.dev.SignKey(), msg))))
	})
}

func TestARotationIsPushedToTheLiveDoor(t *testing.T) {
	rt, tid, ext := doorRuntime(t)
	dc := dialDoor(t, rt)
	dc.knock(t, ext, tid)
	_, first, _ := dc.waitEpochs(t, 5*time.Second)

	// The plane turns: a second instrument joins the space.
	if _, err := rt.AttachSimulatedInstrument(tid, "Greenhouse", 1, 0); err != nil {
		t.Fatal(err)
	}
	_, pushed, unix := dc.waitEpochs(t, 10*time.Second)
	if len(pushed) == 0 || string(pushed[len(pushed)-1]) == string(first[len(first)-1]) {
		t.Fatal("the rotation never reached the live door")
	}
	if unix == 0 {
		t.Fatal("the push forgot the clock floor")
	}
}
