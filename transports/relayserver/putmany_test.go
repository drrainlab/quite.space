package relayserver

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// waitParked waits until a listener is registered on hint; waitUnparked
// until none is left on any hint.
func waitParked(t *testing.T, srv *Server, hint []byte) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.listenMu.Lock()
		n := len(srv.listeners[string(hint)])
		srv.listenMu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the listener never parked")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitUnparked(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.listenMu.Lock()
		n := len(srv.listeners)
		srv.listenMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a listener never left")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func putManyStand(t *testing.T) (*Server, string, *relay.Client) {
	t.Helper()
	srv, port, err := StartServer("127.0.0.1:0", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	c, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return srv, addr, c
}

func capsAndHints(n int) (caps, hints [][]byte) {
	for i := 0; i < n; i++ {
		cap := bytes.Repeat([]byte{byte('a' + i)}, 32)
		caps = append(caps, cap)
		hints = append(hints, relay.CollectHint(cap))
	}
	return caps, hints
}

// One body, four mailboxes, one round trip: every mailbox holds one copy,
// the status counts four puts and one PutMany.
func TestPutManyLaysOneCopyPerHint(t *testing.T) {
	srv, _, c := putManyStand(t)
	caps, hints := capsAndHints(4)
	if _, err := c.PutMany(hints, 0, []byte("the same word for the room"), false); err != nil {
		t.Fatal(err)
	}
	for i, cap := range caps {
		items, err := c.Collect([][]byte{cap})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || string(items[0]) != "the same word for the room" {
			t.Fatalf("mailbox %d holds %d item(s)", i, len(items))
		}
	}
	st := srv.StatusSnapshot("", "")
	if st.Traffic.PutsTotal != 4 || st.Traffic.PutManyTotal != 1 {
		t.Fatalf("status puts=%d put_many=%d, want 4 and 1", st.Traffic.PutsTotal, st.Traffic.PutManyTotal)
	}
}

// All or nothing: when one of the mailboxes is full, no mailbox gains a
// copy, and the refusal names the quota.
func TestPutManyIsAllOrNothingOnQuota(t *testing.T) {
	lim := DefaultLimits()
	lim.PerHint = 2
	srv, port, err := StartServer("127.0.0.1:0", lim)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := relay.DialClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	caps, hints := capsAndHints(3)
	// Fill the second mailbox.
	if _, err := c.Put(hints[1], 0, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put(hints[1], 0, []byte("two")); err != nil {
		t.Fatal(err)
	}
	_, err = c.PutMany(hints, 0, []byte("for everybody"), false)
	var re relay.ErrRelay
	if err == nil || !errors.As(err, &re) || re.Reason != relay.ReasonQuotaExceeded {
		t.Fatalf("a PutMany into a full mailbox answered %v, want quota exceeded", err)
	}
	for i := range []int{0, 2} {
		items, err := c.Collect([][]byte{caps[i*2]})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("mailbox %d gained %d item(s) from a refused PutMany", i*2, len(items))
		}
	}
}

// A quiet PutMany rings no doorbell; a loud one rings each mailbox that
// has a doorbell.
func TestPutManyRingsEachMailboxUnlessQuiet(t *testing.T) {
	srv, addr, c := putManyStand(t)
	mu, got := pushProbe(srv)
	_, hints := capsAndHints(2)
	for i, h := range hints {
		l, err := relay.DialClient(addr)
		if err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		go func() {
			_ = l.ListenPush([][]byte{h}, fmt.Sprintf("https://push.example/dev/%d", i), stop, func([]byte) {})
		}()
		waitParked(t, srv, h)
		close(stop)
		l.Close()
	}
	waitUnparked(t, srv)
	if _, err := c.PutMany(hints, 0, []byte("a receipt"), true); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 1, 800*time.Millisecond); n != 0 {
		t.Fatalf("a quiet PutMany rang %d time(s)", n)
	}
	if _, err := c.PutMany(hints, 0, []byte("a word"), false); err != nil {
		t.Fatal(err)
	}
	if n := rings(mu, got, 2, 3*time.Second); n != 2 {
		t.Fatalf("a loud PutMany into two mailboxes rang %d time(s), want 2", n)
	}
}
