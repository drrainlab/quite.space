// lan.peers must mean peers on the local network.
//
// It used to mean "every live link", and a radio is a live link — one, to the
// air. Two devices talking over LoRa in a field with no Wi-Fi anywhere
// reported a LAN peer each, and the connection chip, which trusts the field
// name, told them they were talking across the room.
package node

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/rnode"
)

// quietLink is a modem that takes everything and answers nothing: a board
// alone on its segment.
type quietLink struct{}

func (quietLink) Read(p []byte) (int, error) {
	time.Sleep(5 * time.Millisecond)
	return 0, nil
}
func (quietLink) Write(p []byte) (int, error) { return len(p), nil }
func (quietLink) Close() error                { return nil }

func TestARadioIsNotALanPeer(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "in a field")
	defer rt.Close()
	if _, err := rt.CreateSpace("the line"); err != nil {
		t.Fatal(err)
	}
	if n := rt.LAN().Peers; n != 0 {
		t.Fatalf("a node with nothing attached reports %d LAN peers", n)
	}

	// A radio, and nothing else: no Wi-Fi, no LAN, nobody across the room.
	// The modem accepts what it is told and says nothing back, which is a
	// board alone on a segment — the exact situation being tested.
	host := &fakeHost{list: []HostRadio{{Name: "usb1", Supported: true}},
		linkFn: func() rnode.Link { return &quietLink{} }}
	rt.SetRadioHost(host)
	if err := rt.AttachHostRadio("usb1", "correct horse battery staple"); err != nil {
		t.Fatalf("the modem would not come up: %v", err)
	}

	if n := rt.LAN().Peers; n != 0 {
		t.Fatalf("the radio was counted as %d LAN peer(s) — this is the bug "+
			"that told two people in a field they were on the same network", n)
	}
	// And the radio still reports itself as a radio.
	if !rt.RadioState().Connected {
		t.Fatal("the radio is not reported as attached")
	}
}

func TestALanPeerIsCountedOnce(t *testing.T) {
	// The counter is honest in the other direction too: a real local link is
	// a peer, and it is one peer however many spaces it carries.
	a := openRuntime(t, t.TempDir(), "alice")
	defer a.Close()
	b := openRuntime(t, t.TempDir(), "bob")
	defer b.Close()
	if err := a.StartLAN("127.0.0.1:0", ""); err != nil {
		t.Skipf("no LAN in this environment: %v", err)
	}
	if err := b.StartLAN("127.0.0.1:0", ""); err != nil {
		t.Skipf("no LAN in this environment: %v", err)
	}
	tid, err := a.CreateSpace("one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateSpace("two"); err != nil {
		t.Fatal(err)
	}
	invite, err := a.MintInvite(tid, b.Device.ID, b.Device.X25519Pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.JoinInvite(invite); err != nil {
		t.Fatal(err)
	}
	if err := b.ConnectPeer(fmt.Sprintf("127.0.0.1:%d", a.LAN().Port)); err != nil {
		t.Skipf("could not dial the peer: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.LAN().Peers == 1 && b.LAN().Peers == 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("a single local link counted as alice=%d bob=%d",
		a.LAN().Peers, b.LAN().Peers)
}

// dyingLink is a modem whose cable comes out: it works, then every read
// fails, which is exactly what the USB layer and a serial port both report.
type dyingLink struct {
	mu   sync.Mutex
	dead bool
}

func (l *dyingLink) kill() { l.mu.Lock(); l.dead = true; l.mu.Unlock() }
func (l *dyingLink) Read(p []byte) (int, error) {
	l.mu.Lock()
	dead := l.dead
	l.mu.Unlock()
	if dead {
		return 0, errors.New("the modem was detached")
	}
	time.Sleep(5 * time.Millisecond)
	return 0, nil
}
func (l *dyingLink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dead {
		return 0, errors.New("the modem was detached")
	}
	return len(p), nil
}
func (l *dyingLink) Close() error { l.kill(); return nil }

// A radio that was UNPLUGGED must be attachable again. It was not: nothing
// released the one-radio-per-node slot except the explicit Detach switch, so
// pulling a cable and putting it back was answered with "a radio is already
// connected" — for the rest of the session.
func TestAnUnpluggedRadioCanBeAttachedAgain(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "somebody in a field")
	defer rt.Close()
	if _, err := rt.CreateSpace("the line"); err != nil {
		t.Fatal(err)
	}

	var current *dyingLink
	host := &fakeHost{
		list: []HostRadio{{Name: "usb1", Supported: true}},
		linkFn: func() rnode.Link {
			current = &dyingLink{}
			return current
		},
	}
	rt.SetRadioHost(host)

	if err := rt.AttachHostRadio("usb1", "correct horse battery staple"); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if !rt.RadioState().Connected {
		t.Fatal("the radio did not come up")
	}

	// The cable comes out.
	current.kill()

	// The node has to NOTICE, on its own, without anybody pressing detach.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && rt.RadioState().Connected {
		time.Sleep(200 * time.Millisecond)
	}
	if rt.RadioState().Connected {
		t.Fatal("the node still believes a radio it cannot reach is connected")
	}

	// And it must be plugged back in. This is the whole test.
	deadline = time.Now().Add(15 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = rt.AttachHostRadio("usb1", ""); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("plugging the same board back in was refused: %v", err)
	}
	if !rt.RadioState().Connected {
		t.Fatal("the second attach reported success without a radio")
	}
	// And it did not have to ask for the segment phrase again: an unplug is
	// not a detach, and the empty phrase above is the assertion.
}
