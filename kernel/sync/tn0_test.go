package sync

import (
	"bytes"
	"testing"
	"time"

	"github.com/drrainlab/quiet_places/kernel/eventlog"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/schemas"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/transports/loopback"
)

// framePri builds a signed frame with an explicit priority lane and
// optional custody expiry (TN-0 helpers on top of the conformance author).
func (a *author) framePri(t *testing.T, schema string, payload []byte,
	pri signal.Priority, expiresAt uint64) []byte {
	t.Helper()
	a.seq++
	a.clk++
	env := &signal.Envelope{
		Terminal: a.term, Principal: a.prin, Device: a.dev,
		Sequence: a.seq, Schema: schema, LogicalClock: a.clk,
		ProducedBy: signal.AuthorshipHuman, PayloadEncoding: signal.PayloadCBOR,
		Payload: payload, Priority: pri, ExpiresAt: expiresAt,
	}
	if a.seq > 1 {
		prev := a.tip
		env.Previous = &prev
	}
	f, err := env.Sign(a.priv)
	if err != nil {
		t.Fatal(err)
	}
	a.tip = id.EventIDOf(f)
	return f
}

// TN-0: an expired frame still ingests and the chain advances — expiry is
// CUSTODY expiry, never an ingest hole (ADR-015 §2).
func TestExpiredFrameIngestsChainAdvances(t *testing.T) {
	var term id.TerminalID
	term[0] = 0xE0
	log := eventlog.New(term, nil)
	a := newAuthor(t, term, 0x61)

	past := uint64(time.Now().Add(-time.Hour).Unix())
	frames := [][]byte{
		a.framePri(t, "message.text.v1", textPayload(t, "one"), signal.PriorityMessage, 0),
		a.framePri(t, "message.text.v1", textPayload(t, "two — expired"), signal.PriorityMessage, past),
		a.framePri(t, "message.text.v1", textPayload(t, "three"), signal.PriorityMessage, 0),
	}
	for _, f := range frames {
		if _, err := log.Ingest(f); err != nil {
			t.Fatalf("expired frame must ingest: %v", err)
		}
	}
	if log.Len() != 3 {
		t.Fatalf("log len %d", log.Len())
	}
	sum := log.Summary()
	if len(sum) != 1 || sum[0].ContiguousUntil != 3 {
		t.Fatalf("chain stalled: %+v", sum)
	}
}

// TN-0: pushMissing schedules chains by priority lane — a security-lane
// chain reaches the peer before a message-lane chain, while order INSIDE
// each chain is preserved.
func TestLaneOrderingSecurityBeforeMessages(t *testing.T) {
	var term id.TerminalID
	term[0] = 0xE1
	src, dst := newNode(term), newNode(term)

	msgAuthor := newAuthor(t, term, 0x71)
	secAuthor := newAuthor(t, term, 0x72)

	// Interleave writes: messages first into the log, then security — the
	// scheduler must still push the security chain first.
	for i := 0; i < 5; i++ {
		src.write(t, msgAuthor.framePri(t, "message.text.v1",
			textPayload(t, "chat"), signal.PriorityMessage, 0))
	}
	for i := 0; i < 3; i++ {
		src.write(t, secAuthor.framePri(t, "message.text.v1",
			textPayload(t, "key material stand-in"), signal.PrioritySecurity, 0))
	}

	var arrival []signal.Priority
	dst.eng.OnApplied = func(ap eventlog.Applied) {
		arrival = append(arrival, ap.Env.Priority)
	}

	pair := loopback.NewPair(loopback.Faults{Seed: 9})
	if err := dst.eng.SendSummary(pair.B); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		if _, _, err := src.eng.Pump(pair.A); err != nil {
			t.Fatal(err)
		}
		if _, _, err := dst.eng.Pump(pair.B); err != nil {
			t.Fatal(err)
		}
	}
	if len(arrival) != 8 {
		t.Fatalf("expected 8 applied, got %d", len(arrival))
	}
	// All security frames must arrive before any message frame.
	for i := 0; i < 3; i++ {
		if arrival[i] != signal.PrioritySecurity {
			t.Fatalf("security lane not first: %v", arrival)
		}
	}
}

// TN-0: the OnSent hook reports exactly the frames handed to the endpoint.
func TestOnSentReportsHandedFrames(t *testing.T) {
	var term id.TerminalID
	term[0] = 0xE2
	src, dst := newNode(term), newNode(term)
	a := newAuthor(t, term, 0x73)
	var want []id.EventID
	for i := 0; i < 4; i++ {
		f := a.message(t, "note")
		want = append(want, id.EventIDOf(f))
		src.write(t, f)
	}
	var got []id.EventID
	src.eng.OnSent = func(ids []id.EventID) { got = append(got, ids...) }

	pair := loopback.NewPair(loopback.Faults{Seed: 4})
	if err := dst.eng.SendSummary(pair.B); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		if _, _, err := src.eng.Pump(pair.A); err != nil {
			t.Fatal(err)
		}
		if _, _, err := dst.eng.Pump(pair.B); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("OnSent ids: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i][:], want[i][:]) {
			t.Fatalf("OnSent id %d mismatch", i)
		}
	}
}

func textPayload(t *testing.T, s string) []byte {
	t.Helper()
	p, err := (&schemas.TextMessage{Text: s}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
