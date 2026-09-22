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

// rings waits up to d for the probe to record at least n rings.
func rings(mu *sync.Mutex, got *[]string, n int, d time.Duration) int {
	deadline := time.Now().Add(d)
	for {
		mu.Lock()
		k := len(*got)
		mu.Unlock()
		if k >= n || time.Now().After(deadline) {
			return k
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// A parked connection that COLLECTS keeps the doorbell quiet; when the
// process dies, the doorbell is the only ear left. The registration must
// survive the socket — that is its whole point.
func TestTheDoorbellStaysQuietForAParkThatCollects(t *testing.T) {
	old := pushGrace
	pushGrace = 400 * time.Millisecond
	defer func() { pushGrace = old }()

	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	mu, got := pushProbe(srv)

	cap := []byte("capcapcapcapcapcapcapcapcapcapca")
	hint := relay.CollectHint(cap)
	endpoint := "https://push.example/dev/abc"

	drainer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer drainer.Close()
	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		// A live phone: the notify is answered with a collect.
		_ = listener.ListenPush([][]byte{hint}, endpoint, stop, func([]byte) {
			_, _ = drainer.Collect([][]byte{cap})
		})
	}()
	time.Sleep(300 * time.Millisecond)

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Put(hint, 0, []byte("heard on the socket")); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, 3*pushGrace); n != 0 {
		t.Fatalf("the doorbell rang %d time(s) for a park that collected", n)
	}

	// The process dies. Now the doorbell is the only ear left.
	close(stop)
	listener.Close()
	time.Sleep(300 * time.Millisecond)
	if _, err := writer.Put(hint, 0, []byte("for the dead process")); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, 3*time.Second); n != 1 {
		t.Fatalf("doorbell rang %d time(s) for a dead process, want 1", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if (*got)[0] != endpoint {
		t.Fatalf("doorbell record: %v", *got)
	}
}

// THE DOZE CASE. Android cuts the app's network and closes nothing: the
// park looks alive from here for up to listenIdle. A park that does not
// collect within the grace is not here, and the doorbell rings anyway.
func TestADozingParkDoesNotSilenceTheDoorbell(t *testing.T) {
	old := pushGrace
	pushGrace = 400 * time.Millisecond
	defer func() { pushGrace = old }()

	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	mu, got := pushProbe(srv)

	hint := []byte("dozedozedozedoze")
	endpoint := "https://push.example/dev/doze"
	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		// Parked, notified, and never collecting — a phone in a pocket.
		_ = listener.ListenPush([][]byte{hint}, endpoint, stop, func([]byte) {})
	}()
	time.Sleep(300 * time.Millisecond)

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	start := time.Now()
	if _, err := writer.Put(hint, 0, []byte("into the pocket")); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, pushGrace/2); n != 0 {
		t.Fatalf("rang %d time(s) before the grace — a live park would have been given no chance", n)
	}
	if n := rings(mu, got, 1, 3*time.Second); n != 1 {
		t.Fatalf("rang %d time(s) after the grace, want exactly 1", n)
	}
	if took := time.Since(start); took < pushGrace {
		t.Fatalf("rang at %v, before the grace of %v", took, pushGrace)
	}
	// A hint nobody registered for never grows a timer.
	srv.pushRegs().ring("unregisteredhint", true)
	srv.pushRegs().mu.Lock()
	pending := len(srv.pushRegs().unproven)
	srv.pushRegs().mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d unproven timer(s) for hints nobody registered", pending)
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

// EN-4: the socket rings for everything parked; the out-of-band doorbell
// only for the hints the client named as worth a wake. A push hint that is
// not parked is a request-shape problem, refused as such.
func TestOnlyTheNamedHintsRingTheDoorbell(t *testing.T) {
	old := pushGrace
	pushGrace = 300 * time.Millisecond
	defer func() { pushGrace = old }()

	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	mu, got := pushProbe(srv)

	mail := []byte("mailmailmailmail")
	plane := []byte("planeplaneplanep")
	endpoint := "https://push.example/dev/named"
	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		// Parked on both, never collecting; the doorbell named for mail only.
		_ = listener.ListenPushFor([][]byte{mail, plane}, [][]byte{mail}, endpoint, stop, func([]byte) {})
	}()
	time.Sleep(300 * time.Millisecond)

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Put(plane, 0, []byte("a grant offer")); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, 4*pushGrace); n != 0 {
		t.Fatalf("the identity plane rang the doorbell %d time(s)", n)
	}
	if _, err := writer.Put(mail, 0, []byte("a word")); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, 3*time.Second); n != 1 {
		t.Fatalf("mail rang %d time(s), want 1", n)
	}

	// Naming a hint that is not parked is refused.
	other, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stop2 := make(chan struct{})
	defer close(stop2)
	err = other.ListenPushFor([][]byte{mail}, [][]byte{plane}, endpoint, stop2, func([]byte) {})
	if err == nil {
		t.Fatal("a push hint that is not parked was accepted")
	}
}
