// The relay CLIENT, and the vocabulary of refusals it shares with the server.
//
// Apache-2.0, deliberately: a client needs this, an independent relay
// implementation needs this, and anything built on Quite Space needs to be
// able to speak to a relay without inheriting a copyleft obligation. The
// server half lives in transports/relayserver under AGPL-3.0-only — see the
// package comment there for why one Go package could not hold both.
package relay

import (
	"errors"
	"time"

	"github.com/drrainlab/quiet_places/transports/lan"
)

// The reasons a relay refuses, as CONSTANTS the server sends and the client
// classifies by. Two private copies of one string is how a classification
// drifts from what is actually sent — this codebase has already paid for that
// once, when a pool looked for "use of closed" while the transport said
// "lan: connection closed" and a dead connection was never retired.
const (
	ReasonRateLimited   = "rate limited"
	ReasonTooManyHints  = "too many hints"
	ReasonNoCapability  = "collect requires a capability"
	ReasonQuotaExceeded = "quota exceeded or item too large"
)

// RefusalKind says what a relay's refusal MEANS, because "the relay said no"
// covers three situations that call for three different responses.
type RefusalKind int

const (
	// RefusalUnknown — an older or newer relay refusing for a reason this
	// build does not recognise. Treated as retryable-but-unhelpful: the
	// connection is fine, and guessing harder would be inventing semantics.
	RefusalUnknown RefusalKind = iota
	// RefusalThrottled — asking again later will work. The connection is
	// HEALTHY: a rate limit is a schedule, not a broken socket, and throwing
	// the connection away makes the next attempt more expensive for exactly
	// the reason it was refused.
	RefusalThrottled
	// RefusalRequestShape — this request was malformed or too big for this
	// relay. Asking again identically will fail identically, so it is a bug
	// or a version mismatch and belongs in a diagnostic, not in a retry loop.
	// The connection is healthy.
	RefusalRequestShape
)

// ErrRelay wraps a relay-reported error.
//
// It is a REFUSAL, never a dead connection: the relay answered. Callers ask
// Kind rather than reading Reason, so the classification lives in one place
// beside the constants the server sends.
type ErrRelay struct {
	Reason string
	// RetryAfter is how long the relay asked the caller to wait. Zero means it
	// did not say — an older relay, or a refusal that waiting will not fix —
	// and a caller must NOT read zero as "immediately".
	RetryAfter time.Duration
}

func (e ErrRelay) Error() string {
	if e.RetryAfter > 0 {
		return "relay: " + e.Reason + " (retry after " + e.RetryAfter.String() + ")"
	}
	return "relay: " + e.Reason
}

// Kind classifies the refusal. Matched against the constants above rather
// than by substring: a message that changes wording must break a compile or a
// test, not silently change a policy.
func (e ErrRelay) Kind() RefusalKind {
	switch e.Reason {
	case ReasonRateLimited:
		return RefusalThrottled
	case ReasonTooManyHints, ReasonQuotaExceeded, ReasonNoCapability:
		return RefusalRequestShape
	default:
		return RefusalUnknown
	}
}

// Throttled reports whether waiting is the right response.
func (e ErrRelay) Throttled() bool { return e.Kind() == RefusalThrottled }

// Client is one connection to a relay.
type Client struct {
	conn *lan.Conn
}

// DialClient connects to a relay with NO identity check — the local-lan
// trust profile (loopback and LAN, where identity lives in event
// signatures). Public relays are dialed through DialClientPinned.
func DialClient(addr string) (*Client, error) {
	node, err := lan.NewNodeWithMaxPacket(lan.RelayMaxPacket)
	if err != nil {
		return nil, err
	}
	c, err := node.Dial(addr)
	if err != nil {
		return nil, err
	}
	return &Client{conn: c}, nil
}

// DialClientPinned connects with SPKI verification (RR-1): verify gets the
// peer's pin during the handshake; returning an error aborts the
// connection before any protocol byte flows.
func DialClientPinned(addr string, verify func(pin string) error) (*Client, error) {
	node, err := lan.NewNodeWithMaxPacket(lan.RelayMaxPacket)
	if err != nil {
		return nil, err
	}
	c, err := node.DialPinned(addr, verify)
	if err != nil {
		return nil, err
	}
	return &Client{conn: c}, nil
}

// Close disconnects.
func (c *Client) Close() { c.conn.Close() }

// ProbeResult is one measured exchange with a relay (RR-3).
type ProbeResult struct {
	RTT       time.Duration
	NowMS     uint64 // the relay's wall clock — clock calibration for free
	ProtoMin  uint64
	ProtoMax  uint64
	Load      string
	Accepting bool
}

// Probe runs one MsgProbe round trip. The nonce binds the reply to this
// request; a legacy relay answers "unknown message type" (an ErrRelay),
// which the caller may treat as "fall back to the MsgTime-only profile".
func (c *Client) Probe(nonce []byte) (ProbeResult, error) {
	start := time.Now()
	reply, err := c.roundTrip(&Msg{Type: MsgProbe, Nonce: nonce}, 5*time.Second)
	if err != nil {
		return ProbeResult{}, err
	}
	if reply.Type != MsgProbeOK {
		return ProbeResult{}, errors.New("relay: unexpected probe reply")
	}
	if len(nonce) > 0 && string(reply.Nonce) != string(nonce) {
		return ProbeResult{}, errors.New("relay: probe nonce mismatch")
	}
	return ProbeResult{
		RTT: time.Since(start), NowMS: reply.Now,
		ProtoMin: reply.ProtoMin, ProtoMax: reply.ProtoMax,
		Load: reply.Load, Accepting: reply.Accepting == 1,
	}, nil
}

func (c *Client) roundTrip(m *Msg, timeout time.Duration) (*Msg, error) {
	if err := c.conn.Send(m.Encode()); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, pkt := range c.conn.Poll() {
			reply, err := DecodeMsg(pkt)
			if err != nil {
				continue
			}
			if reply.Type == MsgError {
				return nil, ErrRelay{
					Reason:     reply.Reason,
					RetryAfter: time.Duration(reply.RetryAfterMs) * time.Millisecond,
				}
			}
			return reply, nil
		}
		if closed, err := c.conn.Closed(); closed {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("relay: timed out waiting for reply")
}

// Put stores one opaque item; returns the relay's accepted deadline. This
// is a transport receipt: it proves accepted_by_relay, never delivery.
func (c *Client) Put(hint []byte, expiresAt uint64, body []byte) (uint64, error) {
	reply, err := c.roundTrip(&Msg{Type: MsgPut, Hint: hint, Expires: expiresAt, Body: body}, 10*time.Second)
	if err != nil {
		return 0, err
	}
	if reply.Type != MsgPutOK {
		return 0, errors.New("relay: unexpected reply")
	}
	return reply.Expires, nil
}

// Time asks the relay for its wall clock (unix ms) and reports the local
// monotonic round-trip. The caller derives offset = relay_now + rtt/2 - local
// and keeps the sample with the smallest RTT.
func (c *Client) Time() (nowMS uint64, rtt time.Duration, err error) {
	start := time.Now() // monotonic
	reply, err := c.roundTrip(&Msg{Type: MsgTime}, 5*time.Second)
	if err != nil {
		return 0, 0, err
	}
	if reply.Type != MsgTimeOK || reply.Now == 0 {
		return 0, 0, errors.New("relay: unexpected time reply")
	}
	return reply.Now, time.Since(start), nil
}

// Collect fetches (and removes) everything stored under the given hints.
// Collect drains the mailboxes these CAPABILITIES address (PH-1). The
// caller proves it may empty a box by holding the preimage of the box's
// address; hints alone no longer open anything.
func (c *Client) Collect(caps [][]byte) ([][]byte, error) {
	reply, err := c.roundTrip(&Msg{Type: MsgCollectCap, Caps: caps}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if reply.Type != MsgItems {
		return nil, errors.New("relay: unexpected reply")
	}
	return reply.Items, nil
}

// Fetch reads the given hints WITHOUT removing anything — the many-reader
// verb for public mailboxes (PA-0). Server-side budgets bound the reply.
func (c *Client) Fetch(hints [][]byte) ([][]byte, error) {
	reply, err := c.roundTrip(&Msg{Type: MsgFetch, Hints: hints}, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if reply.Type != MsgFetchItems {
		return nil, errors.New("relay: unexpected reply")
	}
	return reply.Items, nil
}

// Replace atomically swaps a hint's contents with one item (the public
// projection mailbox). Same receipt semantics as Put: accepted, not
// delivered.
func (c *Client) Replace(hint []byte, expiresAt uint64, body []byte) (uint64, error) {
	reply, err := c.roundTrip(&Msg{Type: MsgReplace, Hint: hint, Expires: expiresAt, Body: body}, 10*time.Second)
	if err != nil {
		return 0, err
	}
	if reply.Type != MsgPutOK {
		return 0, errors.New("relay: unexpected reply")
	}
	return reply.Expires, nil
}
