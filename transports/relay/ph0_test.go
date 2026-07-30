// PH-0 acceptance: the availability floor. Fetch was the only verb with
// abuse bounds; Collect, Replace and Put had none — and all three are
// reachable by anyone who can compute a hint, which for a public space is
// anyone holding the link. These tests pin the bounds directly on the
// handler, so they fail the moment a limit is dropped.
package relay

import (
	"strings"
	"testing"
)

// handleN drives the dispatcher directly: the abuse accounting lives on
// connState, which is per-connection, so one cs across the loop is exactly
// what a single hostile connection looks like.
func handleN(s *Server, cs *connState, m *Msg, n int) *Msg {
	var last *Msg
	for range n {
		last = s.handle(m, cs)
	}
	return last
}

func testServer() (*Server, *connState) {
	lim := DefaultLimits()
	return &Server{store: NewStore(lim.PerHint, lim.MaxItemBytes), limits: lim},
		&connState{}
}

func TestCollectIsRateLimited(t *testing.T) {
	s, cs := testServer()
	m := &Msg{Type: msgCollect, Hints: [][]byte{make([]byte, HintLen)}}
	// One under the limit still passes.
	if r := handleN(s, cs, m, s.limits.collectRatePerMin()); r.Type == msgError {
		t.Fatalf("refused inside the limit: %s", r.Reason)
	}
	if r := s.handle(m, cs); r.Type != msgError || !strings.Contains(r.Reason, "rate") {
		t.Fatalf("collect flood not refused: %+v", r)
	}
}

func TestCollectHintCountIsCapped(t *testing.T) {
	s, cs := testServer()
	hints := make([][]byte, s.limits.collectMaxHints()+1)
	for i := range hints {
		hints[i] = make([]byte, HintLen)
	}
	r := s.handle(&Msg{Type: msgCollect, Hints: hints}, cs)
	if r.Type != msgError || !strings.Contains(r.Reason, "hints") {
		t.Fatalf("unbounded hint list accepted: %+v", r)
	}
}

func TestCollectReplyIsByteBounded(t *testing.T) {
	s, cs := testServer()
	// Four hints, each holding well over the reply budget on its own.
	big := make([]byte, 1<<20)
	var hints [][]byte
	for h := range 4 {
		hint := make([]byte, HintLen)
		hint[0] = byte(h)
		hints = append(hints, hint)
		for range 16 {
			s.store.Put(Item{DestinationHint: string(hint), Ciphertext: big})
		}
	}
	r := s.handle(&Msg{Type: msgCollect, Hints: hints}, cs)
	if r.Type != msgItems {
		t.Fatalf("unexpected reply: %+v", r)
	}
	total := 0
	for _, it := range r.Items {
		total += len(it)
	}
	if total > s.limits.collectMaxBytes() {
		t.Fatalf("collect reply unbounded: %d bytes over a %d budget",
			total, s.limits.collectMaxBytes())
	}
	if total == 0 {
		t.Fatal("budget starved the reply entirely")
	}
}

func TestReplaceIsRateLimited(t *testing.T) {
	s, cs := testServer()
	m := &Msg{Type: msgReplace, Hint: make([]byte, HintLen), Body: []byte("x")}
	if r := handleN(s, cs, m, s.limits.writeRatePerMin()); r.Type == msgError {
		t.Fatalf("refused inside the limit: %s", r.Reason)
	}
	if r := s.handle(m, cs); r.Type != msgError || !strings.Contains(r.Reason, "rate") {
		t.Fatalf("replace flood not refused: %+v", r)
	}
}

func TestPutIsRateLimited(t *testing.T) {
	s, cs := testServer()
	// Distinct hints AND bodies: Put is content-idempotent within a hint and
	// capped per hint, so hammering one hint would hit those bounds first and
	// tell us nothing about the rate limit.
	put := func(i int) *Msg {
		hint := make([]byte, HintLen)
		hint[0], hint[1] = byte(i), byte(i>>8)
		return s.handle(&Msg{Type: msgPut, Hint: hint, Body: []byte{byte(i)}}, cs)
	}
	var last *Msg
	for i := range s.limits.writeRatePerMin() {
		last = put(i)
	}
	if last.Type == msgError {
		t.Fatalf("refused inside the limit: %s", last.Reason)
	}
	if r := put(1 << 12); r.Type != msgError || !strings.Contains(r.Reason, "rate") {
		t.Fatalf("put flood not refused: %+v", r)
	}
}

// The write limiter must not steal from the read limiter or vice versa: a
// busy publisher must still be able to read, and a busy reader must still
// be able to publish.
func TestReadAndWriteBudgetsAreSeparate(t *testing.T) {
	s, cs := testServer()
	handleN(s, cs, &Msg{Type: msgFetch, Hints: [][]byte{make([]byte, HintLen)}},
		s.limits.fetchRatePerMin())
	r := s.handle(&Msg{Type: msgPut, Hint: make([]byte, HintLen), Body: []byte("v")}, cs)
	if r.Type == msgError {
		t.Fatalf("a spent fetch budget blocked a write: %s", r.Reason)
	}
	s2, cs2 := testServer()
	handleN(s2, cs2, &Msg{Type: msgPut, Hint: make([]byte, HintLen), Body: []byte("v")},
		s2.limits.writeRatePerMin())
	if r := s2.handle(&Msg{Type: msgFetch, Hints: [][]byte{make([]byte, HintLen)}}, cs2); r.Type == msgError {
		t.Fatalf("a spent write budget blocked a read: %s", r.Reason)
	}
}
