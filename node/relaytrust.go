// Relay trust profiles (RR-1). Every relay dial in the node funnels
// through dialRelay, which picks the profile from the ADDRESS:
//
//	official (registry pins)  → SPKI-verified against the embedded pin set
//	loopback / local-lan      → plain dial (identity lives in event
//	                            signatures, as on the LAN)
//	custom, confirmed TOFU    → SPKI-verified against the confirmed pin
//	custom, unconfirmed       → REFUSED with ErrRelayNeedsTrust — a pin is
//	                            never stored silently; the person confirms
//	                            the fingerprint once (UI or `terminal
//	                            relay trust`), then it is enforced forever
//
// A pin MISMATCH is ErrRelayUntrusted — a distinct state from unhealthy:
// it is never auto-retried, because retrying an identity failure is how a
// MITM gets a second chance.
package node

import (
	"errors"
	"net"
	"strings"

	"github.com/drrainlab/quiet_places/transports/relay"
)

// ErrRelayUntrusted: the relay presented a key that does not match its
// pin. Not a network failure — do not retry, tell the person.
type ErrRelayUntrusted struct {
	Endpoint string
	Got      string
}

func (e ErrRelayUntrusted) Error() string {
	return "node: relay " + e.Endpoint + " presented an unexpected identity (" +
		e.Got + ") — refusing; if the operator really rotated the key, re-confirm it explicitly"
}

// ErrRelayNeedsTrust: a custom non-local relay has no confirmed pin yet.
type ErrRelayNeedsTrust struct {
	Endpoint string
	Pin      string // observed identity, when known ("" until probed)
}

func (e ErrRelayNeedsTrust) Error() string {
	msg := "node: relay " + e.Endpoint + " has no confirmed identity — run " +
		"`terminal relay show-identity " + e.Endpoint + "`, verify the fingerprint " +
		"with the operator, then `terminal relay trust " + e.Endpoint + " <fingerprint>`"
	return msg
}

// loopbackAddr reports whether the endpoint's host is this machine —
// the local-lan trust profile.
func loopbackAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pinVerifier builds the handshake callback for a pin set.
func pinVerifier(endpoint string, pins []string) func(string) error {
	return func(got string) error {
		for _, p := range pins {
			if got == p {
				return nil
			}
		}
		return ErrRelayUntrusted{Endpoint: endpoint, Got: got}
	}
}

// dialRelay opens a relay connection under the right trust profile.
func (r *Runtime) dialRelay(addr string) (*relay.Client, error) {
	// Official entry for this endpoint? Its pin set decides.
	for _, d := range BuiltinRelayRegistry().Relays {
		if d.Endpoint != addr {
			continue
		}
		if len(d.SPKIPins) == 0 {
			return relay.DialClient(addr) // official local-lan (local-dev)
		}
		c, err := relay.DialClientPinned(addr, pinVerifier(addr, d.SPKIPins))
		return c, unwrapPinError(err)
	}
	if loopbackAddr(addr) {
		return relay.DialClient(addr)
	}
	// Custom, non-local: a confirmed TOFU pin or nothing.
	if pin, ok := r.loadRelayState().TrustedPin(addr); ok {
		c, err := relay.DialClientPinned(addr, pinVerifier(addr, []string{pin}))
		return c, unwrapPinError(err)
	}
	return nil, ErrRelayNeedsTrust{Endpoint: addr}
}

// unwrapPinError digs the typed pin failure out of the TLS handshake
// error chain so callers (and the pool's untrusted state) can switch on it.
func unwrapPinError(err error) error {
	if err == nil {
		return nil
	}
	var untrusted ErrRelayUntrusted
	if errors.As(err, &untrusted) {
		return untrusted
	}
	// crypto/tls wraps the VerifyPeerCertificate error in text on some
	// paths; recover the type by marker when As fails.
	if s := err.Error(); strings.Contains(s, "unexpected identity") {
		return ErrRelayUntrusted{Endpoint: "", Got: ""}
	}
	return err
}

// RelayIdentity dials an endpoint ONLY to observe its identity pin —
// nothing is trusted, nothing is stored, no protocol byte is sent. This
// is the `show-identity` / UI-confirmation primitive.
func RelayIdentity(addr string) (string, error) {
	var observed string
	c, err := relay.DialClientPinned(addr, func(pin string) error {
		observed = pin
		return nil // accept the handshake; we are here to look, not talk
	})
	if err != nil {
		return "", err
	}
	c.Close()
	if observed == "" {
		return "", errors.New("node: the relay presented no identity")
	}
	return observed, nil
}

// TrustRelayAt stores a CONFIRMED pin for a custom endpoint. fingerprint
// must match what the relay currently presents — confirming blind would
// defeat the point of confirmation. Free function: `terminal relay trust`
// runs without a passphrase (relays.json holds no secrets), and there is
// deliberately no --accept-any-certificate anywhere.
func TrustRelayAt(dataDir, endpoint, fingerprint string) error {
	observed, err := RelayIdentity(endpoint)
	if err != nil {
		return err
	}
	if observed != fingerprint {
		return ErrRelayUntrusted{Endpoint: endpoint, Got: observed}
	}
	now := int64(nowUnix())
	return UpdateRelayStateAt(dataDir, func(st *RelayLocalState) {
		for i := range st.Trust {
			if st.Trust[i].Endpoint == endpoint {
				st.Trust[i].SPKIPin = fingerprint
				st.Trust[i].ConfirmedUnix = now
				return
			}
		}
		st.Trust = append(st.Trust, RelayTrust{
			Endpoint: endpoint, SPKIPin: fingerprint, ConfirmedUnix: now,
		})
	})
}

// ForgetRelayAt drops a confirmed pin.
func ForgetRelayAt(dataDir, endpoint string) error {
	return UpdateRelayStateAt(dataDir, func(st *RelayLocalState) {
		out := st.Trust[:0]
		for _, t := range st.Trust {
			if t.Endpoint != endpoint {
				out = append(out, t)
			}
		}
		st.Trust = out
	})
}

func (r *Runtime) TrustRelay(endpoint, fingerprint string) error {
	return TrustRelayAt(r.dataDir, endpoint, fingerprint)
}
func (r *Runtime) ForgetRelay(endpoint string) error {
	return ForgetRelayAt(r.dataDir, endpoint)
}
