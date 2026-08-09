// The radio seam for a phone.
//
// WHY THIS FILE HAS TO EXIST. Every other host reaches an RNode by opening a
// serial path. Android has none: it creates no device node for a USB
// peripheral, so /dev holds one `tty` and nothing that a driver could open —
// measured on the device, not assumed. The hardware is reachable only through
// UsbManager, which lives in Kotlin, behind a permission the person grants
// per device.
//
// So the host does the ACQUIRING and the core does the PROTOCOL. Kotlin lists
// what is plugged in and hands over three verbs for the one that was chosen;
// the modem driver above is the same one a laptop uses, unchanged, and every
// layer above that — the transfer endpoint, the peer link, the pump — never
// learns which way the board was reached.
package quietcore

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/node"
	"github.com/drrainlab/quiet_places/transports/rnode"
)

// UsbSerial is one claimed modem, implemented in KOTLIN. Three obligations,
// stated because nothing across the boundary enforces them:
//
//	it is called from a Go goroutine, not the main thread — be thread-safe
//	ReadChunk BLOCKS until bytes arrive or its own timeout passes, and
//	  returns an empty slice for "nothing yet", which is not an error
//	CloseLink releases the USB interface; after it, reads and writes may
//	  fail, and that is expected
//
// An error from ReadChunk ends the radio: the driver treats a read failure as
// the modem being gone, which is exactly right for a cable pulled out.
type UsbSerial interface {
	ReadChunk() ([]byte, error)
	WriteChunk(b []byte) error
	CloseLink() error
}

// UsbHost is the platform's USB service, implemented in KOTLIN.
//
// Open MAY BLOCK for a long time: the first use of a device raises the
// system's permission dialog, and the person may take as long as they like.
// It is called from a goroutine for exactly that reason.
type UsbHost interface {
	// ListJSON returns a JSON array of {name,label,supported,why}.
	ListJSON() (string, error)
	// Open claims one device by the name ListJSON gave it.
	Open(name string) (UsbSerial, error)
}

// quietBeat bounds how fast a host that does not block can be asked again.
const quietBeat = 15 * time.Millisecond

// usbLink adapts the host's chunk-shaped interface to the byte stream the
// modem driver reads. The only real work is the leftover buffer: a bulk
// transfer returns whatever the endpoint had, which is regularly more than
// the caller's slice, and dropping the remainder would corrupt KISS framing
// in a way that looks like a flaky radio.
type usbLink struct {
	host UsbSerial

	mu   sync.Mutex
	rest []byte
	shut bool
}

func (l *usbLink) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	l.mu.Lock()
	if len(l.rest) > 0 {
		n := copy(p, l.rest)
		l.rest = l.rest[n:]
		l.mu.Unlock()
		return n, nil
	}
	shut := l.shut
	l.mu.Unlock()
	if shut {
		return 0, io.EOF
	}

	chunk, err := l.host.ReadChunk()
	if err != nil {
		return 0, err
	}
	if len(chunk) == 0 {
		// A quiet line is not an error — the driver loops. But it loops
		// TIGHTLY: on a desktop the serial port's own read timeout does the
		// waiting, and here nothing does unless the host chooses to. A host
		// that returns immediately would spin a core for as long as the
		// radio is attached, on a battery. The beat is short enough to cost
		// no latency the air can notice.
		time.Sleep(quietBeat)
		return 0, nil
	}
	n := copy(p, chunk)
	if n < len(chunk) {
		l.mu.Lock()
		l.rest = append(l.rest, chunk[n:]...)
		l.mu.Unlock()
	}
	return n, nil
}

func (l *usbLink) Write(p []byte) (int, error) {
	if err := l.host.WriteChunk(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (l *usbLink) Close() error {
	l.mu.Lock()
	if l.shut {
		l.mu.Unlock()
		return nil
	}
	l.shut = true
	l.mu.Unlock()
	return l.host.CloseLink()
}

// usbRadioHost translates the binding-shaped host into the one the node
// declares. The JSON hop exists because a slice of structs is more trouble
// than it is worth across a language binding, and the list is small and
// produced once per scan.
type usbRadioHost struct{ host UsbHost }

func (h usbRadioHost) ListRadios() ([]node.HostRadio, error) {
	raw, err := h.host.ListJSON()
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var out []node.HostRadio
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, errors.New("the host's device list could not be read: " + err.Error())
	}
	return out, nil
}

func (h usbRadioHost) OpenRadio(name string) (rnode.Link, error) {
	s, err := h.host.Open(name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("the host returned no link")
	}
	return &usbLink{host: s}, nil
}

var (
	radioHostMu sync.Mutex
	radioHost   UsbHost
)

// SetUsbHost installs the platform's USB service, or removes it with nil.
//
// Safe before the core is open, which is the usual case: the host registers
// at Activity scope and Start picks it up. Safe while it is running too.
func SetUsbHost(host UsbHost) {
	radioHostMu.Lock()
	radioHost = host
	radioHostMu.Unlock()

	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	if r != nil {
		applyUsbHost(r)
	}
}

// applyUsbHost hands the registered host to a runtime. Called by Start, so a
// host registered before the node opened is not forgotten.
func applyUsbHost(r *node.Runtime) {
	radioHostMu.Lock()
	h := radioHost
	radioHostMu.Unlock()
	if h == nil {
		r.SetRadioHost(nil)
		return
	}
	r.SetRadioHost(usbRadioHost{host: h})
}

// DetachRadio puts the radio down and forgets it, so the next start does not
// bring back something somebody switched off.
func DetachRadio() error {
	stateMu.Lock()
	r := rt
	stateMu.Unlock()
	if r == nil {
		return errors.New("radio: the node is not open yet")
	}
	return r.DetachRadio()
}
