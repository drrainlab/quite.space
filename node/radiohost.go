// A radio the HOST holds, for platforms where the node cannot reach one.
//
// THE SHAPE OF THE PROBLEM. Everywhere else, finding a modem means listing
// serial ports and opening a path. On Android there are no serial ports:
// the system creates no device node for a USB peripheral, so /dev holds one
// `tty` and nothing that could be opened — measured on a phone, not assumed.
// The board is reachable only through the platform's own USB service, which
// lives in the app, behind a permission granted per device.
//
// So the node keeps the protocol and delegates the ACQUIRING. A host that can
// reach hardware registers itself; the scan asks it what is plugged in, and
// the attach asks it to open one. Nothing else in the node changes, and on
// every platform that does have serial ports nothing changes at all — no host
// is ever registered there.
package node

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/drrainlab/quiet_places/transports/rnode"
)

// HostDevicePrefix marks a port name that belongs to the host rather than to
// the filesystem. It travels through the scan into the interface and comes
// back on the attach, which is how the attach knows which way to open it
// without the interface having to understand platforms.
const HostDevicePrefix = "host:"

// HostRadio is one device the host can see.
type HostRadio struct {
	// Name identifies it to the host on the way back. Opaque here.
	Name string `json:"name"`
	// Label is for a person: what the board calls itself, and its ids.
	Label string `json:"label"`
	// Supported says the host can actually drive this one. Unsupported
	// devices are still reported, deliberately — a list that omits the modem
	// somebody is holding leaves them unable to tell "not plugged in" from
	// "plugged in and not driveable", and those have different next steps.
	Supported bool `json:"supported"`
	// Why explains either answer in a sentence.
	Why string `json:"why"`
}

// RadioHost is implemented by a platform host that can reach USB hardware the
// node cannot open by path.
type RadioHost interface {
	// ListRadios reports what is attached, right now.
	ListRadios() ([]HostRadio, error)
	// OpenRadio claims one and returns the byte stream to its modem. The
	// caller closes it.
	OpenRadio(name string) (rnode.Link, error)
}

// SetRadioHost installs the host, or removes it with nil. Called once, by the
// platform layer, before anybody scans.
func (r *Runtime) SetRadioHost(h RadioHost) {
	r.mu.Lock()
	r.radioHost = h
	r.mu.Unlock()
}

func (r *Runtime) radioHostRef() RadioHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.radioHost
}

// HostRadios reports what the host can see, or nothing when there is no host.
// An error from the host is returned rather than swallowed: "the USB service
// refused" and "nothing is plugged in" are different sentences.
func (r *Runtime) HostRadios() ([]HostRadio, error) {
	h := r.radioHostRef()
	if h == nil {
		return nil, nil
	}
	return h.ListRadios()
}

// HasRadioHost says whether hardware is reachable through the platform at
// all, which is what lets the interface say something true when nothing is
// plugged in rather than offering to scan ports that cannot exist.
func (r *Runtime) HasRadioHost() bool { return r.radioHostRef() != nil }

// HostRadiosJSON is HostRadios for a caller across a language binding, where
// a slice of structs is more trouble than a string.
func (r *Runtime) HostRadiosJSON() string {
	list, err := r.HostRadios()
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b)
	}
	if list == nil {
		list = []HostRadio{}
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// AttachHostRadio opens a device the host holds and brings the modem up on
// it. name is what the scan reported, with or without the prefix.
func (r *Runtime) AttachHostRadio(name, phrase string) error {
	h := r.radioHostRef()
	if h == nil {
		return errors.New("this build has no way to reach USB hardware itself")
	}
	name = strings.TrimPrefix(strings.TrimSpace(name), HostDevicePrefix)
	if name == "" {
		return errors.New("a device is required — scan for radios first")
	}
	// The seed is resolved BEFORE the device is claimed. Claiming a USB
	// interface and then refusing the phrase would leave the modem held by a
	// process that has decided not to use it, and the next attempt would be
	// told it is busy — by us.
	seed, err := r.segmentSeed(phrase)
	if err != nil {
		return err
	}
	link, err := h.OpenRadio(name)
	if err != nil {
		return err
	}
	if err := r.StartRNodeTransferOverLink(link, name, seed); err != nil {
		_ = link.Close()
		return err
	}
	return nil
}
