// Delivery-class policy + airtime budget (TN-1/TN-2, ADR-015 §9). A radio
// carrier admits small, meaningful frames; media bytes arrive later over a
// fast path. Airtime is a per-link token bucket; the queue's aging keeps
// low lanes from starving forever.
package routing

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Three different limits, because they answer three different questions.
// They used to be one number, and a single 8 KiB cap read as "this is fine
// on radio" when at 2000 bytes/minute it is four minutes of one node
// holding the channel.
const (
	// RadioDecodeCap bounds what a parser will even look at. This is a
	// safety limit against hostile input, not an opinion about airtime.
	RadioDecodeCap = 8 << 10

	// RadioFrameCap is the old name for the decode cap, kept so existing
	// call sites keep meaning what they meant.
	RadioFrameCap = RadioDecodeCap

	// BetaOutboundCap bounds what this bridge will actually PUT ON THE AIR
	// in one message. Measured before the compact profile runs: compact
	// only ever shrinks, so a message under the cap here is under it on the
	// wire too. Calibrated against a real modem preset in RB-3.
	BetaOutboundCap = 1536

	// MaxRadioFragments bounds how many packets one message may become.
	// Losing any single fragment loses the whole message, so a message
	// spread over dozens of packets is one that statistically never
	// arrives on a lossy link.
	MaxRadioFragments = 12
)

// radioAdmittedPrefixes are the schema families a radio carrier accepts
// (ADR-015 §9). Everything else waits for a fast path.
var radioAdmittedPrefixes = []string{
	"membership.", "identity.", "terminal.", "receipt.", "presence.",
	"message.", "block.", "reaction.", "resonance.", "observation.",
	"card.", "publication.", "appdef.", "poll.", "form.", "appearance.",
	"composition.", "space.", "listening.",
	// SP-3 (ADR-031). "object." also CLOSES A LATENT SP-1 GAP: domain
	// objects shipped without radio admission, so an object revision
	// could never cross a bridge — custody was silently released. Field
	// spaces live on objects (places, routes), so they are admitted; a
	// generic 8 KiB object revision still answers to the size gates and
	// is NOT claimed radio-express-safe (C3 stands for it).
	"object.", "marker.", "checkin.",
}

// ErrTooLarge marks a frame that will never fit this carrier. It is not
// ErrNoRoom: a full store empties, so refusing for space is temporary and
// the sender should retry. Size is permanent for this link, and retrying it
// on radio spends airtime to fail identically every time. The two must be
// distinguishable or a message that needs a broadband path sits in a retry
// loop that cannot succeed.
var ErrTooLarge = errors.New("routing: frame exceeds what this carrier can send")

// RadioAdmits decides whether a frame may occupy radio custody/airtime.
func RadioAdmits(schema string, size int) bool {
	if size > RadioDecodeCap {
		return false
	}
	for _, p := range radioAdmittedPrefixes {
		if strings.HasPrefix(schema, p) {
			return true
		}
	}
	return false
}

// TokenBucket meters airtime bytes per link. Conservative default: polite
// duty-cycle behavior, operator-tunable.
type TokenBucket struct {
	mu         sync.Mutex
	ratePerSec float64
	burst      float64
	tokens     float64
	last       time.Time
}

// NewTokenBucket allows ratePerMin bytes per minute with the given burst.
func NewTokenBucket(ratePerMin, burst float64, now time.Time) *TokenBucket {
	return &TokenBucket{
		ratePerSec: ratePerMin / 60,
		burst:      burst,
		tokens:     burst,
		last:       now,
	}
}

// DefaultAirtime is ~2000 bytes/min with a 1 KiB burst.
func DefaultAirtime(now time.Time) *TokenBucket {
	return NewTokenBucket(2000, 1024, now)
}

// Take consumes n bytes of airtime if available.
func (b *TokenBucket) Take(n int, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens += now.Sub(b.last).Seconds() * b.ratePerSec
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if float64(n) > b.tokens {
		return false
	}
	b.tokens -= float64(n)
	return true
}
