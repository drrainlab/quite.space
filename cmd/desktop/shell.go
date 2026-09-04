package main

// The shell's whole logic, with no window in it.
//
// Everything here is net/http and node, so all of it is testable with
// httptest and none of it needs a WebView, a display or the Wails runtime.
// That split is deliberate: an alpha framework is the part most likely to
// change under us, and the part least worth writing tests against.
//
// The one idea is a HANDLER SWAP. The node has no locked state — node.Open
// either succeeds with a passphrase or does not happen — so rather than teach
// the API to be half-open, the shell serves a different handler depending on
// where it is:
//
//	locked   the lock gate, plus 503 {"error":"locked"} on /api/*
//	opening  a page that waits, because node.Open is not instant
//	open     api.Handler(), the real thing
//	failed   one sentence about why, and no way to pretend otherwise
//
// The window never learns about any of this. It is pointed at "/" once and
// the pages navigate themselves: the gate hands out the session token in the
// reply that let somebody in, its page goes to /?token=…, and the opening
// page reloads that same URL until the API answers it.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drrainlab/quiet_places/clients/lockgate"
	"github.com/drrainlab/quiet_places/node"
)

// Shell owns the node and decides what the window is currently looking at.
type Shell struct {
	dataDir string
	ui      fs.FS
	token   string
	gate    *lockgate.Gate

	handler atomic.Pointer[http.Handler]

	mu sync.Mutex
	rt *node.Runtime

	closeOnce sync.Once
}

// NewShell prepares a locked shell. Nothing is opened, nothing is created,
// and node.Inspect — which the gate calls to decide what to ask for — writes
// nothing either, so launching the app on a fresh machine leaves the disk
// exactly as it was until somebody chooses a passphrase.
func NewShell(dataDir string, ui fs.FS) (*Shell, error) {
	token, err := lockgate.NewToken()
	if err != nil {
		return nil, err
	}
	g, err := lockgate.New(dataDir, token)
	if err != nil {
		return nil, err
	}
	s := &Shell{dataDir: dataDir, ui: ui, token: token, gate: g}
	s.swap(lockedHandler(g))
	return s, nil
}

// StartURL is where the window points. Always the gate: the pages navigate
// onward themselves, so the shell needs no channel into the WebView at all.
func (s *Shell) StartURL() string { return "/" }

func (s *Shell) swap(h http.Handler) { s.handler.Store(&h) }

func (s *Shell) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.handler.Load()).ServeHTTP(w, r)
}

// Await blocks until somebody gets through the gate, then opens the node.
//
// The caller runs this OFF the UI thread: node.Open is a scrypt derivation
// plus a replay of every space's log, which on a large node is seconds, and
// a frozen window during it would read as a crash.
func (s *Shell) Await(ctx context.Context) error {
	var creds lockgate.Credentials
	select {
	case creds = <-s.gate.Opened():
	case <-ctx.Done():
		return ctx.Err()
	}

	s.swap(openingHandler())

	if err := s.open(creds); err != nil {
		s.swap(failedHandler(err))
		return err
	}
	return nil
}

func (s *Shell) open(c lockgate.Credentials) error {
	name := c.DisplayName
	if name == "" {
		// Only a first run carries one. For an existing keystore node.Open
		// takes the stored name and ignores this, exactly as the CLI relies
		// on — so a returning person is never quietly renamed.
		name = "me"
	}
	rt, err := node.Open(s.dataDir, c.Passphrase, name)
	if err != nil {
		return err
	}
	// A desktop is on mains and is not a pocket: instruments find this
	// node by its announcements whether or not the window is in front.
	rt.DiscoverableInBackground = true
	// THE CODE IS BOUND ONLY ONCE THE KEY HAS PROVED ITSELF — and the gate
	// does the binding, because the passcode is the gate's business. All
	// that is known here, and nowhere else, is that Open said yes.
	if err := s.gate.Confirm(c); err != nil {
		log.Printf("desktop: identity created, but the code did not bind: %v", err)
	}
	api, err := node.NewAPIServer(rt, s.ui)
	if err != nil {
		rt.Close()
		return err
	}
	api.SetToken(s.token)

	s.mu.Lock()
	s.rt = rt
	s.mu.Unlock()

	// Best effort, and said so: a laptop with no local network is an
	// ordinary situation, not a startup failure.
	// A STABLE PORT FIRST. A board built with a static node address
	// (QI-B1: networks that filter multicast) must find this node after a
	// restart, which an ephemeral port cannot promise. 47181 is asked for;
	// if something else holds it the node falls back to any free port —
	// discovery still works, only the static address does not.
	if err := rt.StartLAN(":47181", node.DefaultLANMulticast); err != nil {
		if err := rt.StartLAN(":0", node.DefaultLANMulticast); err != nil {
			fmt.Println("LAN disabled:", err)
		}
	}

	// AN HTTP ORIGIN FOR THE VIDEO PLAYER, and only for it.
	//
	// This window serves the whole interface over a custom scheme —
	// cmd/wails-probe measures `origin=wails://localhost` — and an embed
	// refuses a page whose identity cannot travel in a Referer header,
	// which a custom scheme's cannot. So the node opens the smallest
	// listener that can exist: one route, no token, nothing behind it
	// (node/player.go). Best effort, and quiet about it: a machine that
	// will not give out a loopback port loses inline video and nothing
	// else.
	if _, err := api.ListenPlayer(); err != nil {
		fmt.Println("video player disabled:", err)
	}

	// Re-arm the resident USB stand a previous session left armed
	// (QI-M4): the desktop is exactly the host it exists for. Explicitly
	// here, never inside node.Open — headless tools must not grab plugs.
	rt.ArmInstrumentSerialFromSettings()

	s.swap(api.Handler())
	return nil
}

// SetForeground forwards the window's attention edge to the node, which
// stretches its relay heartbeat while nobody looks. Safe before the gate
// opens — a locked shell has no node yet, and the first thing an opened
// one hears is the CURRENT state, because Await runs after the window
// exists and the runtime defaults to foregrounded anyway.
func (s *Shell) SetForeground(fg bool) {
	s.mu.Lock()
	rt := s.rt
	s.mu.Unlock()
	if rt != nil {
		rt.SetForeground(fg)
	}
}

// Shutdown closes the node once, whatever asks for it — the tray's Quit, an
// OS shutdown, or the run loop returning. Runtime.Close is idempotent and
// bounded on its own (DS-1b); the Once here is about the two OTHER callers
// arriving at the same moment.
func (s *Shell) Shutdown() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		rt := s.rt
		s.rt = nil
		s.mu.Unlock()
		if rt != nil {
			rt.Close()
		}
	})
}

// ---- the four handlers ------------------------------------------------------

// isAPI is the one path test the locked handlers make. It exists so a stale
// poll from a page left over across a swap gets an honest refusal instead of
// the lock screen's HTML parsed as JSON.
func isAPI(p string) bool { return strings.HasPrefix(p, "/api/") }

func refuse(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func lockedHandler(g *lockgate.Gate) http.Handler {
	gate := g.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPI(r.URL.Path) {
			refuse(w, http.StatusServiceUnavailable, `{"error":"locked"}`)
			return
		}
		gate.ServeHTTP(w, r)
	})
}

// openingHandler covers the gap the gate cannot: its page hands out the token
// and navigates to /?token=… after a moment, but node.Open may still be
// replaying logs when that navigation lands. Without this the arrival would
// hit the gate again — which by then reports the directory as already in use,
// because WE hold the lock — and the app would accuse itself of being open
// somewhere else.
//
// So the page simply waits, on the URL it was given, until the real interface
// answers it. Reload rather than a poll: it preserves the query string, needs
// no token of its own, and cannot get out of step with what is being served.
func openingHandler() http.Handler {
	page := waitPage("Opening…",
		"Unlocking your keys and replaying what this device holds.")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPI(r.URL.Path) {
			refuse(w, http.StatusServiceUnavailable, `{"error":"opening"}`)
			return
		}
		servePage(w, page)
	})
}

// failedHandler says the one thing that is true and offers nothing false.
//
// There is no retry: the gate fires once, and re-arming it after a failed
// Open would mean deciding whether the passphrase that got through is still
// the right one to try — a question with no honest answer here. Quitting and
// starting again is the correct instruction, and ErrAlreadyRunning, which is
// the failure this path will actually see, has its own sentence.
func failedHandler(err error) http.Handler {
	msg := "Could not open this device."
	switch {
	case errors.Is(err, node.ErrAlreadyRunning):
		msg = "Another Quite Space window already has this device open. " +
			"Use that one, or quit it and start again."
	case errors.Is(err, node.ErrWrongPassphrase):
		msg = "That passphrase did not open this device. Quit and try again."
	default:
		msg = "Could not open this device: " + err.Error()
	}
	page := waitPage("Not open", msg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPI(r.URL.Path) {
			refuse(w, http.StatusServiceUnavailable, `{"error":"not open"}`)
			return
		}
		servePage(w, page)
	})
}

// withRequestLog prints one line per request, and it exists because a WebView
// has no console anybody can see from outside it.
//
// A request that fails inside the page — a throw before fetch, a CSP refusal —
// produces NO line at all, and that absence is the most useful reading of the
// three: it separates "the page never sent it" from "it arrived empty" (a
// body the scheme handler dropped, which is finding №1's whole shape) from
// "it arrived and the node refused it", which the status says.
//
// Only under --debug. A permanent log of every request, with paths carrying
// space and asset ids, is a diary of what somebody read and when.
func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "-"
		}
		fmt.Printf("REQ %-6s %-46s ct=%-46.46s len=%-9d -> %d  %s\n",
			r.Method, r.URL.Path, ct, r.ContentLength, rec.status,
			time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.written {
		s.status, s.written = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.written = true
	return s.ResponseWriter.Write(b)
}

// Flush is forwarded rather than dropped. A diagnostic that quietly changes
// what it observes is worse than none, and this one is switched on precisely
// while somebody is testing media — the responses most likely to be streamed.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func servePage(w http.ResponseWriter, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(page)
}

// waitPage is deliberately one self-contained string with no asset, no font
// file and no script but its own: it is served in the moments when the
// interface is precisely what does not exist yet.
//
// The reload only arms itself on the opening page — a failure that reloaded
// forever would be a flickering wall rather than a sentence.
func waitPage(title, detail string) []byte {
	reload := ""
	if strings.HasSuffix(title, "…") {
		reload = `<script>setTimeout(() => location.reload(), 600)</script>`
	}
	return []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>quite.space</title><style>
html,body{height:100%;margin:0}
body{background:#07070c;color:#e8e6f0;display:grid;place-items:center;
 font:15px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,Inter,sans-serif;
 padding:24px;text-align:center}
h1{font-size:15px;font-weight:600;letter-spacing:.02em;margin:0 0 8px}
p{margin:0;color:#8b88a0;max-width:34em}
</style></head><body><main><h1>` + html.EscapeString(title) + `</h1><p>` +
		html.EscapeString(detail) + `</p></main>` + reload + `</body></html>`)
}
