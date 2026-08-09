// The adapter between a chunk-shaped host and a byte-stream driver.
//
// Small, and worth pinning anyway: the leftover buffer is the one place a
// phone can corrupt a radio link in a way that looks like bad reception.
package quietcore

import (
	"errors"
	"testing"
	"time"
)

type scriptedHost struct {
	chunks [][]byte
	i      int
	closed bool
	err    error
}

func (h *scriptedHost) ReadChunk() ([]byte, error) {
	if h.err != nil {
		return nil, h.err
	}
	if h.i >= len(h.chunks) {
		return []byte{}, nil
	}
	c := h.chunks[h.i]
	h.i++
	return c, nil
}
func (h *scriptedHost) WriteChunk(b []byte) error { return nil }
func (h *scriptedHost) CloseLink() error          { h.closed = true; return nil }

func TestABulkChunkBiggerThanTheReadIsNotLost(t *testing.T) {
	// One bulk transfer, more bytes than the driver's slice. Dropping the
	// remainder would eat the middle of a KISS frame, and the symptom would
	// be a radio that "sometimes" works.
	host := &scriptedHost{chunks: [][]byte{[]byte("ABCDEFGH")}}
	l := &usbLink{host: host}

	got := make([]byte, 0, 8)
	buf := make([]byte, 3)
	for i := 0; i < 4; i++ {
		n, err := l.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, buf[:n]...)
	}
	if string(got) != "ABCDEFGH" {
		t.Fatalf("the stream was mangled: %q", got)
	}
}

func TestAQuietLineIsNotAnErrorAndDoesNotSpin(t *testing.T) {
	l := &usbLink{host: &scriptedHost{}}
	start := time.Now()
	for i := 0; i < 3; i++ {
		n, err := l.Read(make([]byte, 8))
		if err != nil || n != 0 {
			t.Fatalf("silence read as %d, %v", n, err)
		}
	}
	// Three empty reads must not return instantly: a host that does not
	// block would otherwise burn a core for as long as the radio is on.
	if time.Since(start) < 2*quietBeat {
		t.Fatal("empty reads returned with no pause — this spins a phone's CPU")
	}
}

func TestAReadErrorEndsTheRadioRatherThanLoopingOnIt(t *testing.T) {
	l := &usbLink{host: &scriptedHost{err: errors.New("device gone")}}
	if _, err := l.Read(make([]byte, 8)); err == nil {
		t.Fatal("an unplugged cable was reported as silence")
	}
}

func TestClosingTwiceReleasesOnce(t *testing.T) {
	h := &scriptedHost{}
	l := &usbLink{host: h}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	h.closed = false
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if h.closed {
		t.Fatal("the interface was released twice — the second belongs to " +
			"whoever claimed it next")
	}
}
