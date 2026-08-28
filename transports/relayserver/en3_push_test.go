package relayserver

// EN-3 — the out-of-band doorbell's contract: it rings ONLY when nobody
// parked is listening, it coalesces, an empty keyPush is the off switch,
// and the endpoint validator refuses everything the SSRF guard exists
// for. Delivery goes through the test seam — the real poster's socket
// guard would rightly refuse a loopback test server.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// pushProbe swaps the delivery seam and records every ping.
func pushProbe(s *Server) (*sync.Mutex, *[]string) {
	var mu sync.Mutex
	var got []string
	s.pushRegs().post = func(ep string) {
		mu.Lock()
		got = append(got, ep)
		mu.Unlock()
	}
	return &mu, &got
}

func TestTheDoorbellRingsOnlyWhenNobodyIsParked(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	mu, got := pushProbe(srv)

	hint := []byte("dddddddddddddddd")
	endpoint := "https://push.example/dev/abc"

	// Register the endpoint by parking WITH it, then let the connection die
	// — the registration must survive the socket, that is its whole point.
	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		_ = listener.ListenPush([][]byte{hint}, endpoint, stop, func([]byte) {})
	}()
	time.Sleep(300 * time.Millisecond)

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	// Parked and listening: the socket hears it, the doorbell stays quiet.
	if _, err := writer.Put(hint, 0, []byte("heard on the socket")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	rings := len(*got)
	mu.Unlock()
	if rings != 0 {
		t.Fatalf("the doorbell rang %d time(s) while a connection was parked", rings)
	}

	// The process dies. Now the doorbell is the only ear left.
	close(stop)
	listener.Close()
	time.Sleep(300 * time.Millisecond)
	if _, err := writer.Put(hint, 0, []byte("for the dead process")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 || (*got)[0] != endpoint {
		t.Fatalf("doorbell record: %v", *got)
	}
}

func TestTheDoorbellCoalesces(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	mu, got := pushProbe(srv)

	hint := []byte("eeeeeeeeeeeeeeee")
	srv.pushRegs().register("https://push.example/dev/x", [][]byte{hint})

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for i := 0; i < 5; i++ {
		if _, err := writer.Put(hint, 0, []byte("burst")); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 1 {
		t.Fatalf("five puts rang %d times — the answer to any ping is a full drain, one ring covers a burst", len(*got))
	}
}

func TestAnEmptyPushKeyIsTheOffSwitch(t *testing.T) {
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	mu, got := pushProbe(srv)

	hint := []byte("ffffffffffffffff")
	endpoint := "https://push.example/dev/off"

	c, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	stop1 := make(chan struct{})
	go func() { _ = c.ListenPush([][]byte{hint}, endpoint, stop1, func([]byte) {}) }()
	time.Sleep(300 * time.Millisecond)
	close(stop1)
	time.Sleep(100 * time.Millisecond)
	// The switch goes off: park again with an EMPTY endpoint.
	stop2 := make(chan struct{})
	go func() { _ = c.ListenPushClear([][]byte{hint}, stop2, func([]byte) {}) }()
	time.Sleep(300 * time.Millisecond)
	close(stop2)
	c.Close()
	time.Sleep(200 * time.Millisecond)

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Put(hint, 0, []byte("after the off switch")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 0 {
		t.Fatalf("the doorbell rang after the off switch: %v", *got)
	}
}

func TestPushEndpointValidation(t *testing.T) {
	for _, bad := range []string{
		"http://push.example/x",    // cleartext hands observers the wake schedule
		"ftp://push.example/x",     //
		"not a url",                //
		"https://",                 //
		"https://" + longHost(600), // over the length bound
	} {
		if err := relay.ValidatePushEndpoint(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if err := relay.ValidatePushEndpoint("https://ntfy.example/qp-abcdef"); err != nil {
		t.Errorf("a plain https endpoint was refused: %v", err)
	}
}

func longHost(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
