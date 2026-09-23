package relayserver

import (
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// A quiet put — a receipt, a presence, a media chunk — is stored and
// served like any other item, but it rings no doorbell and makes no park
// "waiting": the tester's phone said "Something is waiting" about the ✓✓
// on his own message, every time the other side read one.
func TestAQuietPutRingsNothingAndWaitsForNobody(t *testing.T) {
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

	cap := []byte("capcapcapcapcapcapcapcapcapcapcb")
	hint := relay.CollectHint(cap)
	endpoint := "https://push.example/dev/quiet"

	// A phone registers its doorbell and dies: the doorbell is the only ear.
	listener, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() { _ = listener.ListenPush([][]byte{hint}, endpoint, stop, func([]byte) {}) }()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	listener.Close()
	time.Sleep(200 * time.Millisecond)

	writer, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.PutQuiet(hint, 0, []byte("a receipt")); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, 3*pushGrace); n != 0 {
		t.Fatalf("a quiet put rang the doorbell %d time(s)", n)
	}
	// And a park that comes back is not told anything is waiting: the
	// "already waiting" answer arrives as an immediate notify.
	parker, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer parker.Close()
	told := make(chan struct{}, 4)
	pstop := make(chan struct{})
	defer close(pstop)
	go func() {
		_ = parker.Listen([][]byte{hint}, pstop, func([]byte) { told <- struct{}{} })
	}()
	select {
	case <-told:
		t.Fatal("a mailbox holding only a quiet item reported waiting")
	case <-time.After(600 * time.Millisecond):
	}
	// The item is still there for whoever collects.
	items, err := writer.Collect([][]byte{cap})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("the quiet item was not stored: %d items", len(items))
	}
	// A loud put rings, as before.
	if _, err := writer.Put(hint, 0, []byte("a word")); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, 3*time.Second); n != 1 {
		t.Fatalf("a loud put rang %d time(s), want 1", n)
	}
	if st := srv.StatusSnapshot("", ""); st.Traffic.QuietPutsTotal != 1 || st.Traffic.PutsTotal != 2 {
		t.Fatalf("status counts puts=%d quiet=%d, want 2 and 1", st.Traffic.PutsTotal, st.Traffic.QuietPutsTotal)
	}
}
