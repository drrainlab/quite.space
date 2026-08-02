package transports

import (
	"testing"
	"time"
)

// plainEndpoint is a carrier that says nothing about pacing — a socket, a
// loopback, everything that existed before Pacer did.
type plainEndpoint struct{}

func (plainEndpoint) Send([]byte) error          { return nil }
func (plainEndpoint) Poll() [][]byte             { return nil }
func (plainEndpoint) Capabilities() Capabilities { return Capabilities{} }

type metered struct {
	plainEndpoint
	credit Credit
}

func (m metered) Credit() Credit { return m.credit }

// An endpoint that does not implement Pacer must behave exactly as it did
// before this contract existed. Anything else would make adding backpressure
// a change to every transport rather than an option for the ones that need it.
func TestACarrierThatSaysNothingIsUnmetered(t *testing.T) {
	c := CreditOf(plainEndpoint{})
	if !c.Unlimited() || !c.Known {
		t.Fatalf("a plain endpoint reported %+v, want unlimited and known", c)
	}
	if !c.Allows(1000) {
		t.Fatal("a plain endpoint refused a send — every existing transport just stopped working")
	}
}

// "I can take nothing" and "I cannot say" are different instructions, and
// confusing them is how a radio either floods or stalls forever.
func TestNothingRightNowIsNotTheSameAsCannotSay(t *testing.T) {
	full := Credit{Packets: 0, Known: true, RetryAfter: 3 * time.Second}
	if full.Allows(1) {
		t.Fatal("a carrier with zero room accepted a packet")
	}
	if full.RetryAfter == 0 {
		t.Fatal("a full carrier gave no hint when to return")
	}

	// A radio between attaching and its first status report. It must not
	// stall traffic: on most links the first message is what makes the
	// carrier report anything at all.
	silent := Credit{Known: false}
	if !silent.Allows(5) {
		t.Fatal("a carrier that has not reported yet blocked the send that " +
			"would have made it report")
	}
}

// The whole-message rule. Losing one fragment loses the message, so a
// carrier with room for four must not be handed five.
func TestAMessageIsOfferedWholeOrNotAtAll(t *testing.T) {
	ep := metered{credit: Credit{Packets: 4, Known: true}}
	c := CreditOf(ep)
	if !c.Allows(4) {
		t.Fatal("room for exactly four refused four")
	}
	if c.Allows(5) {
		t.Fatal("room for four accepted five — the fifth fragment is lost, and " +
			"with it the four that already spent airtime")
	}
}
