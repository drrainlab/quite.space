// OpenStream must be the same driver, not a second one.
//
// The phone cannot use Open: Android creates no device node for a USB
// peripheral, so there is no path to pass. What it can supply is the three
// verbs the driver actually uses — and this pins that claim, because the
// temptation when a platform is awkward is to grow a parallel code path that
// then drifts.
package rnode

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// fakeModem answers DETECT and accepts configuration, like a board that is
// awake. It is deliberately NOT a serial port: it implements exactly Link.
type fakeModem struct {
	mu       sync.Mutex
	toHost   bytes.Buffer
	fromHost bytes.Buffer
	closed   bool
	cfg      []byte // command bytes the host sent, in order
}

func (m *fakeModem) Read(p []byte) (int, error) {
	for i := 0; i < 200; i++ {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return 0, io.EOF
		}
		if m.toHost.Len() > 0 {
			n, err := m.toHost.Read(p)
			m.mu.Unlock()
			return n, err
		}
		m.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return 0, nil // a quiet line, which is not an error
}

func (m *fakeModem) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, errors.New("closed")
	}
	m.fromHost.Write(p)
	// Remember which commands were issued: KISS is FEND cmd ... FEND.
	for i := 0; i+1 < len(p); i++ {
		if p[i] == fend && p[i+1] != fend {
			m.cfg = append(m.cfg, p[i+1])
		}
	}
	return len(p), nil
}

func (m *fakeModem) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func mustProfile(t *testing.T) Settings {
	t.Helper()
	s, ok := SettingsForProfile(ProfileLongFastRU)
	if !ok {
		t.Fatal("this build does not know its own default profile")
	}
	return s
}

func TestAModemReachedWithoutAPathIsTheSameModem(t *testing.T) {
	m := &fakeModem{}
	// The board reports its radio as ON, which is the one answer Open
	// refuses to assume — see ErrRadioWillNotStart.
	m.mu.Lock()
	m.toHost.Write([]byte{fend, cmdRadioState, 0x01, fend})
	m.mu.Unlock()

	r, err := OpenStream(m, mustProfile(t))
	if err != nil {
		t.Fatalf("a stream that behaves like a board was refused: %v", err)
	}
	defer r.Close()

	if len(m.cfg) == 0 {
		t.Fatal("nothing was configured: the driver never spoke to the stream")
	}
	// The MTU is the modem's, not a re-derivation from somewhere else.
	if r.MTU() != MaxFrame {
		t.Fatalf("MTU %d, want the modem's %d", r.MTU(), MaxFrame)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	m.mu.Lock()
	shut := m.closed
	m.mu.Unlock()
	if !shut {
		t.Fatal("closing the radio must close the stream the host gave it — " +
			"a phone that keeps a claimed USB interface open cannot re-attach")
	}
}
