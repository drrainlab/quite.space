// PA-0.3 acceptance: the public-mailbox verbs. Fetch is non-destructive
// (many readers), Replace is atomic (I5), Put is idempotent by content
// within a hint, hint namespaces never collide, and the abuse bounds hold.
package relayserver

import (
	"bytes"
	"fmt"
	"github.com/drrainlab/quiet_places/transports/relay"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/protocol/id"
)

func newTestStore() *Store { return NewStore(64, 1<<20) }

func TestFetchIsNonDestructive(t *testing.T) {
	s := newTestStore()
	if !s.Put(Item{DestinationHint: "h", Ciphertext: []byte("one")}) {
		t.Fatal("put refused")
	}
	if !s.Put(Item{DestinationHint: "h", Ciphertext: []byte("two")}) {
		t.Fatal("put refused")
	}
	a := s.Fetch("h", 0, 0)
	b := s.Fetch("h", 0, 0)
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("both readers must see both items: %d/%d", len(a), len(b))
	}
	if s.Pending() != 2 {
		t.Fatalf("fetch mutated the store: %d", s.Pending())
	}
	// Collect afterwards still drains.
	if got := s.Collect("h", 0); len(got) != 2 {
		t.Fatalf("collect after fetch: %d", len(got))
	}
	if s.Pending() != 0 {
		t.Fatal("collect did not drain")
	}
}

func TestReplaceIsAtomicAndSupersedes(t *testing.T) {
	s := newTestStore()
	for i := 0; i < 64; i++ { // jam the hint to the per-hint cap
		if !s.Put(Item{DestinationHint: "h", Ciphertext: []byte(fmt.Sprintf("v%d", i))}) {
			t.Fatalf("put %d refused", i)
		}
	}
	if s.Put(Item{DestinationHint: "h", Ciphertext: []byte("overflow")}) {
		t.Fatal("cap not enforced")
	}
	// Replace succeeds where Put jams, leaving exactly one item.
	if !s.Replace(Item{DestinationHint: "h", Ciphertext: []byte("latest")}) {
		t.Fatal("replace refused on a full hint")
	}
	got := s.Fetch("h", 0, 0)
	if len(got) != 1 || !bytes.Equal(got[0], []byte("latest")) {
		t.Fatalf("replace did not supersede: %v", got)
	}
	if s.Pending() != 1 {
		t.Fatalf("total accounting wrong after replace: %d", s.Pending())
	}
	// An INVALID replacement (oversized) leaves the previous value intact.
	huge := make([]byte, 2<<20)
	if s.Replace(Item{DestinationHint: "h", Ciphertext: huge}) {
		t.Fatal("oversized replace accepted")
	}
	got = s.Fetch("h", 0, 0)
	if len(got) != 1 || !bytes.Equal(got[0], []byte("latest")) {
		t.Fatal("failed replace mutated the mailbox")
	}
}

func TestPutIdempotentByContent(t *testing.T) {
	s := newTestStore()
	bundle := []byte("cumulative pending bundle")
	for i := 0; i < 100; i++ { // owner offline: contributor retries hard
		if !s.Put(Item{DestinationHint: "in", Ciphertext: bundle}) {
			t.Fatalf("retry %d refused", i)
		}
	}
	if got := s.Fetch("in", 0, 0); len(got) != 1 {
		t.Fatalf("identical retries consumed %d slots, want 1", len(got))
	}
	// Collect removes it; the same bytes may then be inserted again.
	if got := s.Collect("in", 0); len(got) != 1 {
		t.Fatal("collect failed")
	}
	if !s.Put(Item{DestinationHint: "in", Ciphertext: bundle}) {
		t.Fatal("re-insert after collect refused")
	}
	// A changed pending set is a NEW item.
	if !s.Put(Item{DestinationHint: "in", Ciphertext: []byte("changed bundle")}) {
		t.Fatal("changed bundle refused")
	}
	if got := s.Fetch("in", 0, 0); len(got) != 2 {
		t.Fatalf("changed digest must occupy a new slot: %d", len(got))
	}
}

func TestFetchSkipsExpiredAndHonorsBudget(t *testing.T) {
	s := newTestStore()
	s.Put(Item{DestinationHint: "h", ExpiresAt: 50, Ciphertext: []byte("stale")})
	s.Put(Item{DestinationHint: "h", ExpiresAt: 500, Ciphertext: bytes.Repeat([]byte("a"), 100)})
	s.Put(Item{DestinationHint: "h", ExpiresAt: 500, Ciphertext: bytes.Repeat([]byte("b"), 100)})
	got := s.Fetch("h", 100, 0)
	if len(got) != 2 {
		t.Fatalf("expired item served: %d", len(got))
	}
	// Newest-first with a budget that only fits one.
	got = s.Fetch("h", 100, 150)
	if len(got) != 1 || got[0][0] != 'b' {
		t.Fatalf("budget/order wrong: %d items", len(got))
	}
}

func TestHintNamespacesDistinct(t *testing.T) {
	var tid id.TerminalID
	tid[0] = 7
	var dev id.DeviceID
	dev[0] = 9
	seen := map[string]string{}
	add := func(name string, h []byte) {
		if prev, dup := seen[string(h)]; dup {
			t.Fatalf("hint collision: %s == %s", name, prev)
		}
		seen[string(h)] = name
	}
	add("member", relay.Hint(tid, 1))
	add("inbox", relay.HintFor(tid, dev, 1))
	add("outbox", relay.HintPublicOutbox(tid, 1))
	for sh := byte(0); sh < relay.IngressShards; sh++ {
		add(fmt.Sprintf("ingress-%d", sh), relay.HintPublicIngress(tid, 1, sh))
	}
	// Same author always lands in the same shard (fresh copy of the id).
	devCopy := dev
	if relay.IngressShard(dev) != relay.IngressShard(devCopy) {
		t.Fatal("shard not deterministic")
	}
}

// 65+ honest ingress writers do not jam the mailbox thanks to sharding:
// distinct devices spread across 8 shards, each with its own 64-item cap.
func TestIngressShardingBeatsPerHintCap(t *testing.T) {
	s := newTestStore()
	var tid id.TerminalID
	accepted := 0
	for i := 0; i < 200; i++ {
		var dev id.DeviceID
		dev[0], dev[1] = byte(i), byte(i>>8)
		hint := relay.HintPublicIngress(tid, 1, relay.IngressShard(dev))
		if s.Put(Item{DestinationHint: string(hint),
			Ciphertext: []byte(fmt.Sprintf("bundle-from-%d", i))}) {
			accepted++
		}
	}
	if accepted < 200 {
		t.Fatalf("only %d/200 honest writers accepted — sharding insufficient", accepted)
	}
}

// End-to-end over the wire: Fetch/Replace verbs through a real server; two
// clients both read the outbox; rate limit answers with relay.MsgError; a wiped
// mailbox is repaired by a later Replace (heartbeat path).
func TestServerFetchReplaceEndToEnd(t *testing.T) {
	limits := DefaultLimits()
	limits.FetchRatePerMin = 5
	srv, port, err := StartServer("127.0.0.1:0", limits)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	var tid id.TerminalID
	tid[3] = 3
	outbox := relay.HintPublicOutbox(tid, relay.Bucket(uint64(time.Now().Unix())))

	owner, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Replace(outbox, 0, []byte("projection-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Replace(outbox, 0, []byte("projection-2")); err != nil {
		t.Fatal(err)
	}

	r1, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer r1.Close()
	r2, err := relay.DialClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	for i, rc := range []*relay.Client{r1, r2} {
		items, err := rc.Fetch([][]byte{outbox})
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
		if len(items) != 1 || !bytes.Equal(items[0], []byte("projection-2")) {
			t.Fatalf("reader %d sees %d items", i, len(items))
		}
	}

	// Simulated squatter wipe → owner heartbeat Replace repairs it.
	srv.store.Collect(string(outbox), uint64(time.Now().Unix()))
	if _, err := owner.Replace(outbox, 0, []byte("projection-2")); err != nil {
		t.Fatal(err)
	}
	if items, _ := r1.Fetch([][]byte{outbox}); len(items) != 1 {
		t.Fatal("heartbeat did not repair the wiped mailbox")
	}

	// Rate limit: the 5/min budget is spent (r1 used 2 fetches) — burn the
	// rest and expect relay.MsgError.
	var rateErr error
	for i := 0; i < 6; i++ {
		if _, err := r1.Fetch([][]byte{outbox}); err != nil {
			rateErr = err
			break
		}
	}
	if rateErr == nil {
		t.Fatal("fetch rate limit never triggered")
	}

	// Old-client tolerance: an unknown message type is answered with
	// relay.MsgError and nothing crashes.
	//
	// Asserted at the SERVER's own seam rather than by pushing a raw frame
	// through a Client. The property belongs to the dispatcher — a relay from
	// the future says something this build has no name for — and reaching
	// through the client to prove it needed the client's unexported
	// roundTrip, which the licence split put in another package. Testing it
	// where it lives is the better shape anyway, and it is what ph0_test
	// already does for every limit.
	if r := srv.handle(&relay.Msg{Type: 99}, &connState{}); r.Type != relay.MsgError {
		t.Fatalf("unknown type must be answered with an error, got %+v", r)
	}
}
