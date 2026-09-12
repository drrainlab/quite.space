package relayserver

// LT-2: a parked session whose pings go unanswered is dead, and says so
// within PongTimeout — not when the kernel gives up on the socket.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

func TestAParkWhosePingsGoUnansweredEndsItself(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.SilentPongsForTest()

	c, err := relay.DialClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.PingEvery = 50 * time.Millisecond
	old := relay.PongTimeout
	relay.PongTimeout = 200 * time.Millisecond
	defer func() { relay.PongTimeout = old }()

	hint := make([]byte, relay.HintLen)
	stop := make(chan struct{})
	done := make(chan error, 1)
	t0 := time.Now()
	go func() { done <- c.Listen([][]byte{hint}, stop, func([]byte) {}) }()
	select {
	case err := <-done:
		if !errors.Is(err, relay.ErrListenSilent) {
			t.Fatalf("the session ended with %v, want ErrListenSilent", err)
		}
		if el := time.Since(t0); el > 3*time.Second {
			t.Fatalf("declared dead after %s — should be about one ping plus PongTimeout", el)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a park with no pongs stayed alive — the silent-death hole LT-2 named")
	}
	st := c.ListenStats()
	if st.Pings == 0 || st.Pongs != 0 || st.ParkedAt.IsZero() {
		t.Fatalf("stats do not tell the story: %+v", st)
	}
}

func TestAnAnsweredPingKeepsTheParkAndCountsIt(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := relay.DialClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.PingEvery = 50 * time.Millisecond
	hint := make([]byte, relay.HintLen)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.Listen([][]byte{hint}, stop, func([]byte) {}) }()
	time.Sleep(400 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("a healthy park ended: %v", err)
	default:
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st := c.ListenStats()
	if st.Pongs == 0 || st.LastPong.IsZero() {
		t.Fatalf("pongs were not counted: %+v", st)
	}
}
