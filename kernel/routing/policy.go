// Delivery-class policy + airtime budget (TN-1/TN-2, ADR-015 §9). A radio
// carrier admits small, meaningful frames; media bytes arrive later over a
// fast path. Airtime is a per-link token bucket; the queue's aging keeps
// low lanes from starving forever.
package routing

import (
	"strings"
	"sync"
	"time"
)

// RadioFrameCap is the hard per-frame size limit for radio custody.
const RadioFrameCap = 8 << 10

// radioAdmittedPrefixes are the schema families a radio carrier accepts
// (ADR-015 §9). Everything else waits for a fast path.
var radioAdmittedPrefixes = []string{
	"membership.", "identity.", "terminal.", "receipt.", "presence.",
	"message.", "block.", "reaction.", "resonance.", "observation.",
	"card.", "publication.", "appdef.", "poll.", "form.", "appearance.",
	"composition.", "space.", "listening.",
}

// RadioAdmits decides whether a frame may occupy radio custody/airtime.
func RadioAdmits(schema string, size int) bool {
	if size > RadioFrameCap {
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
