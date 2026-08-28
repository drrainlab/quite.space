// EN-3 — the doorbell that survives the process.
//
// Wave 2's parked connection covers every minute the client's process is
// alive and its socket intact. Android's deep Doze severs exactly that:
// the network is cut for everything except the platform's own push lane.
// So a client may leave, beside its park, an opaque PUSH ENDPOINT — a
// UnifiedPush (or compatible) URL — and the relay's rule is one sentence:
//
//	a Put lands at a registered hint, and NO parked connection is here
//	to hear it → POST a contentless ping to the endpoint.
//
// What crosses the third party is NOTHING: a fixed two-byte body, no
// hint, no sender, no size, no count. The endpoint learns "check your
// relay", which is exactly what the device's own poll would have asked a
// minute later. The relay, for its part, learns one more stable fact
// about a device than the rotating hints alone would give it — an
// endpoint URL that persists across buckets — which is why the client
// side of this is an OPT-IN switch with that sentence printed on it.
//
// The registry is in-memory on purpose. Persisting it would mean a file
// of push endpoints keyed by mailbox hints sitting on every relay disk;
// losing it on restart merely means the doorbell is quiet until the app
// next parks (phones re-park at every bucket rotation while alive).
package relayserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// pushTTL is how long a registration outlives its last refresh. A
	// week covers a phone that opens the app rarely; the refresh is free
	// (it rides every park).
	pushTTL = 7 * 24 * time.Hour
	// pushCoalesce is the quiet period per endpoint after a ping: every
	// Put inside it is covered by the ping already sent — the device's
	// answer to any ping is "drain everything" anyway.
	pushCoalesce = time.Minute
	// pushTimeout bounds one delivery attempt.
	pushTimeout = 10 * time.Second
	// maxPushEndpoints bounds the whole registry; oldest-refreshed falls
	// out first. A relay is not a push broker for the world.
	maxPushEndpoints = 10000
)

// pushReg is one endpoint's registration: the hints that ring it.
type pushReg struct {
	hints     map[string]struct{}
	refreshed time.Time
	lastPing  time.Time
}

type pushRegistry struct {
	mu   sync.Mutex
	regs map[string]*pushReg // endpoint URL → registration
	// post is the delivery seam — replaced in tests, where the SSRF guard
	// would otherwise refuse the loopback test server.
	post func(endpoint string)
}

func newPushRegistry() *pushRegistry {
	p := &pushRegistry{regs: map[string]*pushReg{}}
	p.post = p.deliver
	return p
}

// register replaces the endpoint's hint set. An empty hint set removes it.
func (p *pushRegistry) register(endpoint string, hints [][]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(hints) == 0 {
		delete(p.regs, endpoint)
		return
	}
	reg := p.regs[endpoint]
	if reg == nil {
		if len(p.regs) >= maxPushEndpoints {
			p.evictOldestLocked()
		}
		reg = &pushReg{}
		p.regs[endpoint] = reg
	}
	reg.hints = map[string]struct{}{}
	for _, h := range hints {
		reg.hints[string(h)] = struct{}{}
	}
	reg.refreshed = time.Now()
}

// remove drops every registration for one endpoint.
func (p *pushRegistry) remove(endpoint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.regs, endpoint)
}

func (p *pushRegistry) evictOldestLocked() {
	var oldest string
	var when time.Time
	for ep, reg := range p.regs {
		if oldest == "" || reg.refreshed.Before(when) {
			oldest, when = ep, reg.refreshed
		}
	}
	if oldest != "" {
		delete(p.regs, oldest)
	}
}

// ring pings every endpoint registered for the hint, coalesced and
// asynchronous: a Put must never wait on a third party's HTTP server.
func (p *pushRegistry) ring(hint string) {
	now := time.Now()
	var due []string
	p.mu.Lock()
	for ep, reg := range p.regs {
		if now.Sub(reg.refreshed) > pushTTL {
			delete(p.regs, ep)
			continue
		}
		if _, ok := reg.hints[hint]; !ok {
			continue
		}
		if now.Sub(reg.lastPing) < pushCoalesce {
			continue
		}
		reg.lastPing = now
		due = append(due, ep)
	}
	p.mu.Unlock()
	for _, ep := range due {
		go p.post(ep)
	}
}

// deliver POSTs the contentless ping. The dialer refuses non-public
// addresses AT THE SOCKET — an endpoint is client-supplied text, which
// makes this exactly the request-forgery shape the unfurl fetch already
// guards against, with the same remedy: check the address the socket is
// about to use, after resolution, where there is no second answer to
// disagree with.
func (p *pushRegistry) deliver(endpoint string) {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: publicAddrOnly}
	client := &http.Client{
		Timeout: pushTimeout,
		Transport: &http.Transport{
			DialContext:       d.DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("push endpoint redirected")
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader("qp"))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return // best effort: the poll underneath is the guarantee
	}
	resp.Body.Close()
}

// publicAddrOnly is the socket-level guard, the same doctrine as the
// node's unfurl fetch: loopback, private, link-local, multicast and the
// carrier/benchmark ranges are refused by name.
func publicAddrOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("push: unroutable address")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("push: unresolved address")
	}
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsUnspecified(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return fmt.Errorf("push: %s is not a public address", host)
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1]&0xC0 == 64 {
			return fmt.Errorf("push: %s is a carrier-local address", host)
		}
		if v4[0] == 198 && v4[1]&0xFE == 18 {
			return fmt.Errorf("push: %s is a benchmarking address", host)
		}
	}
	return nil
}
