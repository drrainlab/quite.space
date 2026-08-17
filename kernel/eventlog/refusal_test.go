package eventlog

import (
	"errors"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
)

// THE MEASUREMENT (MD-0). Turning the certification gate on makes refusal a
// routine event: a peer's message can arrive before the certificate that
// would let it in. Everything then depends on one property nobody had
// checked — whether a refusal is a DELAY or a LOSS.
//
// It is a delay only if the receiver still advertises the gap afterwards, so
// the ordinary sync summary asks for the frame again. If instead the chain
// advanced past it, or the frame were remembered as seen, the refusal would
// be permanent and no amount of certificate arriving later would help; a
// holding area would then be a requirement rather than an optimisation.
//
// This test answers that, and nothing else.
func TestARefusedFrameIsStillAdvertisedAsMissing(t *testing.T) {
	term := id.TerminalID{1}
	a := newAuthor(t, term, 7)
	frame := a.next(t, "the frame that arrives before its certificate")

	l := New(term, nil)
	// A gate that refuses this device, exactly as an uncertified device is
	// refused by node/identity_admit.go.
	refuse := errors.New("device not certified")
	l.SetIdentityAdmit(func(env *signal.Envelope) error { return refuse })

	if _, err := l.Ingest(frame); !errors.Is(err, refuse) {
		t.Fatalf("the gate did not refuse: %v", err)
	}

	// 1. The frame is NOT held.
	if len(l.Summary()) != 0 {
		t.Fatalf("a refused frame created chain state: %+v", l.Summary())
	}

	// 2. The refusal did not mark it seen — re-offering it is possible.
	//    (If Has() were true here the sender would be told we hold it.)
	for _, eid := range l.order {
		_ = eid
		t.Fatal("a refused frame entered the log's order")
	}

	// 3. THE ANSWER. The gate opens — the certificate arrived — and the very
	//    same frame is offered again. If this lands, a refusal is a delay:
	//    the ordinary summary already says the gap is there, so the sender
	//    re-offers on the next round and the message is not lost.
	l.SetIdentityAdmit(nil)
	applied, err := l.Ingest(frame)
	if err != nil {
		t.Fatalf("the same frame was refused forever: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("re-offer did not apply: %d events", len(applied))
	}
	st := l.Summary()
	if len(st) != 1 || st[0].ContiguousUntil != 1 {
		t.Fatalf("the chain did not take the re-offered frame: %+v", st)
	}
}

// The other half of the same question: a refusal must not poison the chain.
// If it left the device quarantined, or advanced the expected sequence past
// the refused slot, the peer would be permanently unable to speak here even
// after presenting a perfectly good certificate.
func TestARefusalDoesNotForkOrAdvanceTheChain(t *testing.T) {
	term := id.TerminalID{2}
	a := newAuthor(t, term, 9)
	first := a.next(t, "one")
	second := a.next(t, "two")

	l := New(term, nil)
	refuse := errors.New("device not certified")
	l.SetIdentityAdmit(func(env *signal.Envelope) error { return refuse })
	if _, err := l.Ingest(first); !errors.Is(err, refuse) {
		t.Fatalf("expected refusal, got %v", err)
	}
	l.SetIdentityAdmit(nil)

	// Both frames, in order, exactly as a re-offer would deliver them.
	if _, err := l.Ingest(first); err != nil {
		t.Fatalf("the first frame could not be taken after a refusal: %v", err)
	}
	if _, err := l.Ingest(second); err != nil {
		t.Fatalf("the chain did not continue after a refusal: %v", err)
	}
	st := l.Summary()
	if len(st) != 1 || st[0].Forked || st[0].ContiguousUntil != 2 {
		t.Fatalf("a refusal damaged the chain: %+v", st)
	}
}
