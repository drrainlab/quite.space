package sync

import (
	"testing"

	"github.com/drrainlab/quiet_places/transports"
)

type mtuEndpoint struct{ mtu int }

func (mtuEndpoint) Send([]byte) error { return nil }
func (mtuEndpoint) Poll() [][]byte    { return nil }
func (e mtuEndpoint) Capabilities() transports.Capabilities {
	return transports.Capabilities{MaxPayload: e.mtu}
}

// A radio-scale carrier gets ONE packet per message.
//
// The budget used to be four packets' worth, described as "keep messages a
// few fragments long". At the ~80% packet loss measured on a shared LoRa
// channel that is a message landing about once in three thousand attempts,
// because losing any one fragment loses all of it. One packet lands about
// once in five.
func TestARadioMessageAimsAtOnePacket(t *testing.T) {
	if got := messageBudget(mtuEndpoint{mtu: 200}); got != 200 {
		t.Fatalf("budget on a 200-byte carrier = %d, want 200 — a multi-fragment "+
			"message on a lossy link is one that statistically never arrives", got)
	}
	// An unmetered carrier batches freely: this rule is about radios, and
	// applying it to a socket would make every LAN sync needlessly chatty.
	if got := messageBudget(mtuEndpoint{mtu: 0}); got != framesBudget {
		t.Fatalf("budget on an unmetered carrier = %d, want %d", got, framesBudget)
	}
	// A carrier bigger than the cap is still capped by it.
	if got := messageBudget(mtuEndpoint{mtu: 1 << 20}); got != framesBudget {
		t.Fatalf("budget on a huge carrier = %d, want the %d cap", got, framesBudget)
	}
}
