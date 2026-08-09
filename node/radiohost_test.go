// A radio the host holds must travel the same road as one on a serial path:
// the same scan, the same phrase rule, the same attach.
//
// These run WITHOUT hardware, and that is the point — the platform-specific
// part of the phone story is the USB claim in Kotlin, and everything on this
// side of it should be provable on a laptop with no cable in it.
package node

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/drrainlab/quiet_places/transports/rnode"
)

type fakeHost struct {
	mu      sync.Mutex
	list    []HostRadio
	listErr error
	opened  []string
	openErr error
	link    *deadLink
	// linkFn overrides what OpenRadio hands back, for tests that need a
	// modem which behaves rather than one that dies.
	linkFn func() rnode.Link
}

func (h *fakeHost) ListRadios() ([]HostRadio, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.list, h.listErr
}

func (h *fakeHost) OpenRadio(name string) (rnode.Link, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, name)
	if h.openErr != nil {
		return nil, h.openErr
	}
	if h.linkFn != nil {
		return h.linkFn(), nil
	}
	h.link = &deadLink{}
	return h.link, nil
}

// deadLink is a modem whose cable comes out: writes fail, which is what the
// USB layer reports when the device goes away mid-configure.
type deadLink struct {
	mu     sync.Mutex
	closed bool
}

func (l *deadLink) Read(p []byte) (int, error) { return 0, nil }
func (l *deadLink) Write(p []byte) (int, error) {
	return 0, errors.New("the modem stopped accepting bytes")
}
func (l *deadLink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return nil
}
func (l *deadLink) wasClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func TestAHostsDevicesReachTheScanAndComeBackOnTheAttach(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "phone")
	defer rt.Close()

	if rt.HasRadioHost() {
		t.Fatal("a fresh runtime must not claim to reach USB hardware")
	}
	host := &fakeHost{list: []HostRadio{
		{Name: "/dev/bus/usb/001/004", Label: "CP2102 (10c4:ea60)",
			Supported: true, Why: "a Silicon Labs bridge"},
		{Name: "/dev/bus/usb/001/007", Label: "Some hub (1a40:0101)",
			Supported: false, Why: "not a bridge this build speaks"},
	}}
	rt.SetRadioHost(host)

	got, err := rt.HostRadios()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("the scan lost devices: %+v", got)
	}
	// The unsupported one is REPORTED, deliberately: a list that hides it
	// leaves somebody unable to tell "not plugged in" from "plugged in and
	// not driveable".
	if got[1].Supported {
		t.Fatal("the hub was reported as driveable")
	}

	// The phrase is resolved BEFORE the device is claimed. Otherwise a wrong
	// phrase leaves the USB interface held by a process that has decided not
	// to use it, and the next attempt is told the modem is busy — by us.
	err = rt.AttachHostRadio(HostDevicePrefix+"/dev/bus/usb/001/004", "")
	if err == nil {
		t.Fatal("a device with no segment phrase and no remembered seed was accepted")
	}
	if !strings.Contains(err.Error(), "phrase") {
		t.Fatalf("the refusal does not say what is missing: %v", err)
	}
	host.mu.Lock()
	opened := len(host.opened)
	host.mu.Unlock()
	if opened != 0 {
		t.Fatal("the modem was claimed before the phrase was checked")
	}
}

func TestAFailedAttachReleasesTheUsbInterface(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "phone")
	defer rt.Close()
	host := &fakeHost{list: []HostRadio{{Name: "usb1", Supported: true}}}
	rt.SetRadioHost(host)

	// A real phrase, and a device that dies while being configured. What
	// matters is not the error — it is that the USB interface is RELEASED. A
	// phone that keeps a claimed interface after a failed attach cannot try
	// again until the app is killed, which is not an instruction anybody
	// should have to follow.
	err := rt.AttachHostRadio("usb1", "correct horse battery staple")
	if err == nil {
		t.Fatal("a modem that would not take a single byte was accepted")
	}
	host.mu.Lock()
	link := host.link
	host.mu.Unlock()
	if link == nil {
		t.Fatal("the device was never opened")
	}
	if !link.wasClosed() {
		t.Fatal("the usb interface is still claimed after a failed attach")
	}
}

func TestWithoutAHostNothingPretendsToSeeUsb(t *testing.T) {
	rt := openRuntime(t, t.TempDir(), "laptop")
	defer rt.Close()
	list, err := rt.HostRadios()
	if err != nil || list != nil {
		t.Fatalf("a laptop invented USB devices: %+v %v", list, err)
	}
	if err := rt.AttachHostRadio("usb1", "phrase"); err == nil {
		t.Fatal("attaching a host device with no host was accepted")
	} else if !strings.Contains(err.Error(), "USB") {
		t.Fatalf("the refusal does not explain itself: %v", err)
	}
}
