// The resident USB stand (QI-M4): cmd/instrument-serial moved into the
// runtime, so a desktop shell can hold the wire without a terminal window.
//
// It is still the DEV STAND — not a bearer, and it does not pretend to be
// one (the bearer decision belongs to QI-B1). What changed is only who
// holds the loop: instead of a person running a CLI beside their node,
// the node itself keeps the port open once the owner has pointed at a
// space and a plug in the UI. The wire grammar is byte-identical to the
// CLI stand's; a board cannot tell which of the two it is talking to.
//
// One door, one instrument: at most one resident stand runs at a time,
// mirroring the law the CLI stand lives by. The choice (port, space) is
// persisted in the encrypted settings blob so the stand re-arms when the
// app reopens — an unplugged board is waited for, never errored at.
package node

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"

	"github.com/drrainlab/quiet_places/protocol/id"
)

// InstrumentSerialConfig is the persisted arming of the resident stand:
// which plug, into which space. Stored in Settings so a restart re-arms.
type InstrumentSerialConfig struct {
	Port  string `json:"port"`
	Space string `json:"space"` // space id, hex
}

// SerialStandStatus is what the UI sees of the resident stand.
type SerialStandStatus struct {
	Armed         bool   `json:"armed"`
	Port          string `json:"port,omitempty"`
	Space         string `json:"space,omitempty"`
	State         string `json:"state,omitempty"` // waiting | listening
	InstrumentID  string `json:"instrument_id,omitempty"`
	LastFrameUnix int64  `json:"last_frame_unix,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// SerialInstrumentPorts lists plugs that plausibly carry an instrument:
// the usual USB-UART bridge names on macOS and Linux. A listing is not a
// claim that a QuietInstrument sits behind the plug — only enrollment
// proves that — so the UI offers, never asserts.
func SerialInstrumentPorts() []string {
	patterns := []string{
		"/dev/cu.usbserial*", "/dev/cu.SLAB_USBtoUART*",
		"/dev/cu.wchusbserial*", "/dev/cu.usbmodem*",
		"/dev/ttyUSB*", "/dev/ttyACM*",
	}
	var out []string
	for _, p := range patterns {
		m, _ := filepath.Glob(p)
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}

// AttachSerialInstrument arms the resident stand: hold this port open as
// the dev stand for that space, persistently. An already-armed stand is
// re-armed with the new choice — one door, one instrument.
func (r *Runtime) AttachSerialInstrument(space id.TerminalID, port string) error {
	if port == "" {
		return fmt.Errorf("node: serial stand needs a port")
	}
	s := r.GetSettings()
	s.InstrumentSerial = &InstrumentSerialConfig{Port: port, Space: space.Hex()}
	if err := r.SetSettings(s); err != nil {
		return err
	}
	r.armSerialStand(space, port)
	return nil
}

// DetachSerialInstrument stops the resident stand and forgets the arming.
// The instrument's membership in the space is untouched: unplugging the
// courier is not detaching the member (that is DELETE /instruments/{iid}).
func (r *Runtime) DetachSerialInstrument() error {
	s := r.GetSettings()
	s.InstrumentSerial = nil
	if err := r.SetSettings(s); err != nil {
		return err
	}
	r.stopSerialStand()
	return nil
}

// ArmInstrumentSerialFromSettings re-arms the stand a previous session
// left armed. Called once by the host (shell, terminal ui) after Open —
// not by Open itself, so headless tools don't grab serial ports.
func (r *Runtime) ArmInstrumentSerialFromSettings() {
	cfg := r.GetSettings().InstrumentSerial
	if cfg == nil {
		return
	}
	space, err := id.ParseTerminalID(cfg.Space)
	if err != nil {
		return
	}
	r.armSerialStand(space, cfg.Port)
}

// SerialInstrumentStatus reports the stand's current life.
func (r *Runtime) SerialInstrumentStatus() SerialStandStatus {
	r.standMu.Lock()
	defer r.standMu.Unlock()
	return r.standStat
}

func (r *Runtime) armSerialStand(space id.TerminalID, port string) {
	r.stopSerialStand()
	r.standMu.Lock()
	stop := make(chan struct{})
	r.standStop = stop
	r.standStat = SerialStandStatus{Armed: true, Port: port, Space: space.Hex(),
		State: "waiting"}
	r.standMu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.serialStandLoop(stop, space, port)
	}()
}

func (r *Runtime) stopSerialStand() {
	r.standMu.Lock()
	if r.standStop != nil {
		close(r.standStop)
		r.standStop = nil
	}
	r.standStat = SerialStandStatus{}
	r.standMu.Unlock()
}

func (r *Runtime) standSet(f func(*SerialStandStatus)) {
	r.standMu.Lock()
	f(&r.standStat)
	r.standMu.Unlock()
}

// serialStandLoop holds the plug for as long as the stand is armed. A
// port that cannot open — board unplugged, cable moved — is retried
// quietly: absence of a board is a state, not a failure.
func (r *Runtime) serialStandLoop(stop chan struct{}, space id.TerminalID, port string) {
	for {
		select {
		case <-stop:
			return
		case <-r.stop:
			return
		default:
		}
		p, err := serial.Open(port, &serial.Mode{BaudRate: 115200})
		if err != nil {
			r.standSet(func(s *SerialStandStatus) { s.State = "waiting"; s.LastError = "" })
			select {
			case <-stop:
				return
			case <-r.stop:
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}
		r.standSet(func(s *SerialStandStatus) { s.State = "listening"; s.LastError = "" })
		// Closing the port is what unblocks a blocked Read — the session
		// cannot watch channels from inside a scanner.
		done := make(chan struct{})
		go func() {
			select {
			case <-stop:
			case <-r.stop:
			case <-done:
			}
			p.Close()
		}()
		r.standSession(stop, p, space)
		close(done)
		select {
		case <-stop:
			return
		case <-r.stop:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// standSession speaks the CLI stand's grammar over one open wire. Split
// from the loop and typed against io.ReadWriter so a test can be the
// board.
func (r *Runtime) standSession(stop chan struct{}, rw io.ReadWriter, space id.TerminalID) {
	var wmu sync.Mutex
	send := func(line string) {
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprint(rw, line+"\n")
	}
	greet := func() {
		send("QI TIME " + fmt.Sprint(time.Now().Unix()))
		send("QI PRINCIPAL " + r.PrincipalID.Hex())
		send("QI ENROLL?")
	}
	greet()

	var imu sync.Mutex
	var iid id.TerminalID
	var haveIID bool
	var lastEpoch string
	// heard flips on the first line from the board. Opening the port
	// asserts DTR, and on these boards DTR is wired to reset: the board
	// reboots under the greeting and the greeting lands on a UART that is
	// not up yet. A board silent four seconds after a greeting is greeted
	// again, up to three times.
	var heard bool

	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		for tries := 0; tries < 3; tries++ {
			select {
			case <-sessionDone:
				return
			case <-stop:
				return
			case <-r.stop:
				return
			case <-time.After(4 * time.Second):
			}
			imu.Lock()
			h := heard
			imu.Unlock()
			if h {
				return
			}
			greet()
		}
	}()

	// Epoch watcher: rotations ride down the same wire the frames ride
	// up, and time rides with them so the board's clock floor advances.
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sessionDone:
				return
			case <-stop:
				return
			case <-r.stop:
				return
			case <-t.C:
			}
			imu.Lock()
			enrolled := haveIID
			imu.Unlock()
			if !enrolled {
				continue
			}
			frames, err := r.ExternalInstrumentEpochFrames(space)
			if err != nil || len(frames) == 0 {
				continue
			}
			cur := hex.EncodeToString(frames[len(frames)-1])
			imu.Lock()
			changed := cur != lastEpoch
			if changed {
				lastEpoch = cur
			}
			imu.Unlock()
			if changed {
				send("QI TIME " + fmt.Sprint(time.Now().Unix()))
				send("QI EPOCH " + cur)
			}
		}
	}()

	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 1<<16), 1<<16)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		imu.Lock()
		heard = true
		imu.Unlock()
		switch {
		case line == "QI TIME?":
			// A clockless board asks. A reboot keeps the USB session alive
			// (the bridge chip never drops), so the greeting's one-shot
			// TIME was never repeated — and a board without a clock neither
			// emits nor computes its discovery hint. Found on the owner's
			// desk: "in the space" beside "wifi: no-time".
			send("QI TIME " + fmt.Sprint(time.Now().Unix()))
		case strings.HasPrefix(line, "QI NOTE ") && strings.HasSuffix(line, " ready"):
			// A boot marker on a live session: re-run the greeting so the
			// board gets its clock and its invitation again.
			greet()
		case strings.HasPrefix(line, "QI ENROLLMENT "):
			raw, err := hex.DecodeString(strings.TrimPrefix(line, "QI ENROLLMENT "))
			if err != nil {
				r.standSet(func(s *SerialStandStatus) { s.LastError = "enrollment: bad hex" })
				continue
			}
			prov, got, err := r.AttachInstrumentByEnrollment(space, raw, uint64(time.Now().Unix()))
			if err != nil {
				r.standSet(func(s *SerialStandStatus) { s.LastError = "enroll: " + err.Error() })
				continue
			}
			imu.Lock()
			iid, haveIID = got, true
			imu.Unlock()
			r.standSet(func(s *SerialStandStatus) {
				s.InstrumentID = got.Hex()
				s.LastError = ""
			})
			send("QI PROVISION " + hex.EncodeToString(prov))
		case strings.HasPrefix(line, "QI FRAME "):
			imu.Lock()
			target, enrolled := iid, haveIID
			imu.Unlock()
			if !enrolled {
				// A frame before enrollment means the board remembers a
				// space this node has not admitted it to on this wire yet.
				send("QI ENROLL?")
				continue
			}
			raw, err := hex.DecodeString(strings.TrimPrefix(line, "QI FRAME "))
			if err != nil {
				r.standSet(func(s *SerialStandStatus) { s.LastError = "frame: bad hex" })
				continue
			}
			if _, err := r.ingestInstrumentFrames(space, target, [][]byte{raw}); err != nil {
				r.standSet(func(s *SerialStandStatus) { s.LastError = "ingest: " + err.Error() })
				continue
			}
			r.standSet(func(s *SerialStandStatus) {
				s.LastFrameUnix = time.Now().Unix()
				s.LastError = ""
			})
		}
	}
}
