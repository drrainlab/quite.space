package main

import (
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Sender forwards one wake to one registration token.
type Sender interface {
	// Send returns the outcome class; the handler maps it to a status.
	Send(token string) Outcome
}

// Outcome is what forwarding came to, coarse on purpose.
type Outcome int

const (
	// Delivered to the push service (not to the phone — nobody can know that).
	Delivered Outcome = iota
	// Gone: the push service says this token is dead. The caller (a relay)
	// ignores statuses today; the 410 is for the day it does not.
	Gone
	// Failed: a transient failure upstream.
	Failed
	// Refused: the push service refused the request itself (a bad key, a
	// bad project) — operator trouble, not the token's.
	Refused
)

type dryRun struct{}

func (dryRun) Send(string) Outcome { return Delivered }

// tokenShape is what an FCM registration token looks like: URL-safe
// characters and colons, and long. Anything else is not forwarded — this
// endpoint is reachable by anyone who can guess a URL, and the one thing it
// must not become is a way to make Google requests with arbitrary strings.
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_:\-]{64,512}$`)

// coalesce is the quiet period per token. The relay already coalesces per
// endpoint for a minute; this is the guard for a caller that does not.
const coalesce = 30 * time.Second

type handler struct {
	sender Sender
	mu     sync.Mutex
	last   map[string]time.Time
	// counters for /healthz — numbers, never tokens
	accepted, forwarded, gone, failed, refused atomic.Uint64
}

// NewHandler serves POST /fcm/{token} and GET /healthz.
func NewHandler(s Sender) http.Handler {
	h := &handler{sender: s, last: map[string]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fcm/{token}", h.ping)
	mux.HandleFunc("GET /healthz", h.health)
	go h.sweep()
	return mux
}

func (h *handler) ping(w http.ResponseWriter, r *http.Request) {
	// The body is the relay's fixed marker; whatever it is, it is not read
	// past a few bytes and never forwarded.
	_ = r.Body.Close()
	token := r.PathValue("token")
	if !tokenShape.MatchString(token) {
		http.Error(w, "not a registration token", http.StatusNotFound)
		return
	}
	h.accepted.Add(1)
	if !h.admit(token) {
		// Covered by the ping already sent: the phone's answer to any ping
		// is "drain everything".
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch h.sender.Send(token) {
	case Delivered:
		h.forwarded.Add(1)
		w.WriteHeader(http.StatusNoContent)
	case Gone:
		h.gone.Add(1)
		w.WriteHeader(http.StatusGone)
	case Refused:
		h.refused.Add(1)
		log.Printf("quiet-push: the push service refused a send — check the key and the project")
		w.WriteHeader(http.StatusBadGateway)
	default:
		h.failed.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}
}

func (h *handler) admit(token string) bool {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.last[token]; ok && now.Sub(t) < coalesce {
		return false
	}
	h.last[token] = now
	return true
}

// sweep forgets tokens whose quiet period is over — the map holds a token
// for half a minute and not a second longer than it must.
func (h *handler) sweep() {
	t := time.NewTicker(coalesce)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		h.mu.Lock()
		for tok, at := range h.last {
			if now.Sub(at) >= coalesce {
				delete(h.last, tok)
			}
		}
		h.mu.Unlock()
	}
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	var b strings.Builder
	b.WriteString("ok\n")
	b.WriteString("accepted " + itoa(h.accepted.Load()) + "\n")
	b.WriteString("forwarded " + itoa(h.forwarded.Load()) + "\n")
	b.WriteString("gone " + itoa(h.gone.Load()) + "\n")       // the push service says the token is dead
	b.WriteString("failed " + itoa(h.failed.Load()) + "\n")   // transient upstream failures
	b.WriteString("refused " + itoa(h.refused.Load()) + "\n") // our key or project refused
	_, _ = w.Write([]byte(b.String()))
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
