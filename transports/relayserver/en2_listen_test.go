package relayserver

// EN-2 — the doorbell's contract, proved against a real server over a
// real connection: a Put into a parked hint rings, a Put elsewhere stays
// silent, re-parking replaces the set, and a parked connection outlives
// the two-minute reaper on pings alone.

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func listenServer(t *testing.T, limits ServerLimits) (addr string, stop func()) {
	t.Helper()
	srv, port, err := StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), func() { srv.Close() }
}

func TestAPutIntoAParkedHintRings(t *testing.T) {
	addr, stop := listenServer(t, DefaultLimits())
	defer stop()

	parked := []byte("0123456789abcdef")
	other := []byte("fedcba9876543210")

	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	rang := make(chan []byte, 8)
	sessionStop := make(chan struct{})
	defer close(sessionStop)
	go func() {
		_ = listener.Listen([][]byte{parked}, sessionStop, func(h []byte) {
			rang <- append([]byte(nil), h...)
		})
	}()
	time.Sleep(300 * time.Millisecond) // let the park land

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	// A Put SOMEWHERE ELSE first: the bell must know whose door it is.
	if _, err := writer.Put(other, 0, []byte("not for the listener")); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-rang:
		t.Fatalf("a foreign hint rang the bell: %x", h)
	case <-time.After(700 * time.Millisecond):
	}

	if _, err := writer.Put(parked, 0, []byte("ding")); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-rang:
		if string(h) != string(parked) {
			t.Fatalf("rang with the wrong hint: %x", h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the parked hint never rang")
	}
}

func TestReListeningReplacesThePark(t *testing.T) {
	addr, stop := listenServer(t, DefaultLimits())
	defer stop()

	first := []byte("aaaaaaaaaaaaaaaa")
	second := []byte("bbbbbbbbbbbbbbbb")

	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	rang := make(chan []byte, 8)
	stop1 := make(chan struct{})
	go func() {
		_ = listener.Listen([][]byte{first}, stop1, func(h []byte) { rang <- append([]byte(nil), h...) })
	}()
	time.Sleep(300 * time.Millisecond)
	close(stop1) // end the first session; same connection re-parks
	time.Sleep(100 * time.Millisecond)
	stop2 := make(chan struct{})
	defer close(stop2)
	go func() {
		_ = listener.Listen([][]byte{second}, stop2, func(h []byte) { rang <- append([]byte(nil), h...) })
	}()
	time.Sleep(300 * time.Millisecond)

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Put(first, 0, []byte("stale door")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Put(second, 0, []byte("live door")); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-rang:
		if string(h) != string(second) {
			t.Fatalf("the replaced park still rings: %x", h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the fresh park never rang")
	}
}

func TestAParkedConnectionOutlivesTheReaper(t *testing.T) {
	// A short reaper for everyone, a longer one for parked connections —
	// the test shrinks both so the ordinary conn dies visibly inside the
	// run while the parked one, kept alive by its ping, survives past it.
	limits := DefaultLimits()
	limits.ListenIdle = 5 * time.Second
	addr, stop := listenServer(t, limits)
	defer stop()

	parked := []byte("cccccccccccccccc")
	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	rang := make(chan []byte, 8)
	sessionStop := make(chan struct{})
	defer close(sessionStop)
	go func() {
		_ = listener.Listen([][]byte{parked}, sessionStop, func(h []byte) {
			rang <- append([]byte(nil), h...)
		})
	}()
	time.Sleep(300 * time.Millisecond)

	// Silence longer than ListenIdle would reap it; the client's Listen
	// pings on ListenPing which is minutes — so ring the bell instead to
	// prove the connection is still parked after a couple of idle
	// seconds within its window.
	time.Sleep(2 * time.Second)
	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Put(parked, 0, []byte("still here")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rang:
	case <-time.After(3 * time.Second):
		t.Fatal("the parked connection went deaf inside its idle window")
	}
}

func TestListenIsBounded(t *testing.T) {
	limits := DefaultLimits()
	limits.ListenMaxHints = 2
	addr, stop := listenServer(t, limits)
	defer stop()

	c, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	hints := [][]byte{
		[]byte("1111111111111111"), []byte("2222222222222222"), []byte("3333333333333333"),
	}
	err = c.Listen(hints, nil, func([]byte) {})
	if err == nil {
		t.Fatal("an oversized park was accepted")
	}
}
