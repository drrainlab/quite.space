package relayserver

// A listener that parks over a mailbox which already holds something is told
// so in the acknowledgement itself — no gap in which a Put can go unannounced,
// and nothing read out of the box.

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestParkingOverWaitingMailSaysSoAtOnce(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	full := []byte("0123456789abcdef")
	empty := []byte("fedcba9876543210")

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Put(full, uint64(time.Now().Add(time.Hour).Unix()), []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}

	park := func(hints [][]byte) (woke chan []byte, stop chan struct{}) {
		c, err := relay.DialClient(addr)
		if err != nil {
			t.Fatal(err)
		}
		woke = make(chan []byte, 4)
		stop = make(chan struct{})
		go func() {
			defer c.Close()
			_ = c.Listen(hints, stop, func(h []byte) { woke <- h })
		}()
		return woke, stop
	}

	// Over a box that holds something: one wake, with no hint named.
	woke, stop := park([][]byte{empty, full})
	select {
	case h := <-woke:
		if h != nil {
			t.Fatalf("the waiting wake must name no mailbox, got %x", h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parked over waiting mail and heard nothing")
	}
	close(stop)

	// Nothing was taken: the mail is still there for whoever holds the cap.
	if got := srv.Pending(); got != 1 {
		t.Fatalf("parking must not touch the mailbox; pending = %d, want 1", got)
	}

	// Over an empty box: silence.
	woke2, stop2 := park([][]byte{empty})
	select {
	case h := <-woke2:
		t.Fatalf("an empty mailbox must not wake anybody, got %x", h)
	case <-time.After(700 * time.Millisecond):
	}
	close(stop2)
}
