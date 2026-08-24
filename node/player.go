package node

// THE PLAYER WINDOW — watching a video without letting the video into the
// room where the keys are.
//
// The interface's Content-Security-Policy says `frame-src 'none'`, and the
// comment above it says why: this origin holds the session token and can
// drive every route on the node, so the policy's job is to make an
// injection worth as little as possible. Embedding a video player INTO
// that document would put a third party's code beside the token — which
// is survivable (a cross-origin frame cannot read its parent) but is
// exactly the kind of "survivable" a security policy exists to not rely
// on, and it would mean relaxing the app's own policy for everyone,
// forever, so that one card can play.
//
// So the video gets a room of its own. This route serves a document with
// NO token, NO application script and a policy of its own that permits
// exactly one thing: a frame from youtube-nocookie.com. Even a wholly
// compromised embed here has nothing next to it to take.
//
// It is deliberately unauthenticated. It holds nothing, reads nothing and
// says nothing about this node — its entire content is a video id that the
// person watching just clicked. Requiring the token would only mean
// putting the token in a URL that then travels to a window whose whole
// purpose is to talk to somebody else.

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// playerPolicy is the video window's own policy. 'none' by default, one
// frame source, and no script at all — including ours.
const playerPolicy = "default-src 'none'; " +
	"frame-src https://www.youtube-nocookie.com; " +
	"style-src 'unsafe-inline'; " +
	"script-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

func (a *APIServer) handlePlayer(w http.ResponseWriter, r *http.Request) {
	// The id goes through the same alphabet check the card was built with,
	// here as well as there: this handler is reachable on its own, so it
	// verifies rather than assumes.
	vid := validVideoID(r.URL.Query().Get("v"))
	h := w.Header()
	h.Set("Content-Security-Policy", playerPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Type", "text/html; charset=utf-8")
	if vid == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`<!doctype html><title>—</title><body style="background:#0b0910;color:#8a8296;` +
			`font:14px/1.6 ui-sans-serif,system-ui;display:grid;place-items:center;height:100vh;margin:0">` +
			`that address is not a video`))
		return
	}
	q := url.Values{
		"autoplay": {"1"},
		"rel":      {"0"},
		// modestbranding is the small courtesy: this window is a player,
		// not a place.
		"modestbranding": {"1"},
	}
	// Start time, when the person pasted one. Digits only.
	if t := digitsOnly(r.URL.Query().Get("t"), 7); t != "" {
		q.Set("start", t)
	}
	src := "https://www.youtube-nocookie.com/embed/" + vid + "?" + q.Encode()
	w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + vid + `</title>` +
		`<style>html,body{margin:0;height:100%;background:#000}iframe{border:0;width:100%;height:100%;display:block}</style>` +
		`</head><body><iframe src="` + src + `" allow="autoplay; encrypted-media; picture-in-picture; fullscreen" allowfullscreen></iframe></body></html>`))
}

func digitsOnly(s string, max int) string {
	if len(s) > max {
		return ""
	}
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return ""
	}
	return s
}

// ---- opening the window ----

// handleOpenPlayer opens the video window through the HOST's own opener.
//
// It exists because of a fact about the desktop shell that is easy to
// mistake for a bug in this feature: the macOS webview implements no
// WKUIDelegate for new windows, so `target="_blank"` and `window.open` do
// NOTHING there — silently. A play control that worked in a browser and
// was inert in the app would be the worst of both.
//
// The seam is deliberately not "open this URL". It takes a VIDEO ID,
// checks it against the same alphabet as everything else here, and builds
// the address itself, at this node's own origin. So the widest thing a
// caller can ask for is "show the player window for some video" — never
// "make the host machine open an address of my choosing", which is what
// an open-url endpoint would have been.
func (a *APIServer) handleOpenPlayer(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		V string `json:"v"`
		T string `json:"t"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	vid := validVideoID(body.V)
	if vid == "" {
		httpErr(w, http.StatusBadRequest, errRefusedVideoID)
		return
	}
	// The origin is the one this very request arrived at — the page asking
	// is the page we serve — and it must be a local one, because that is
	// the only kind this node is ever reached at.
	host := r.Host
	if !isLocalHostPort(host) {
		httpErr(w, http.StatusBadRequest, errNotLocalOrigin)
		return
	}
	q := "v=" + url.QueryEscape(vid)
	if t := digitsOnly(body.T, 7); t != "" {
		q += "&t=" + t
	}
	hostOpener("http://" + host + "/player?" + q)
	writeJSON(w, map[string]string{"opened": vid})
}

var (
	errRefusedVideoID = errors.New("that is not a video id")
	errNotLocalOrigin = errors.New("the player opens only at this node's own origin")
)

// isLocalHostPort accepts only the names this node is actually reached at.
// A host with NO port is the case that matters: SplitHostPort fails on it,
// and an early version treated that failure as "nothing to check" — so a
// bare "evil.example" passed straight through into the address the host
// was told to open. Anything unparsable is refused now, in both shapes.
func isLocalHostPort(host string) bool {
	h := host
	if x, _, err := net.SplitHostPort(host); err == nil {
		h = x
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// hostOpener is the seam a test replaces: everything above it decides
// WHICH address may be opened, and that decision is what is worth testing
// — launching a browser to prove it would be testing the operating system.
var hostOpener = openOnHost

// openOnHost hands one address to the operating system's opener.
// Best-effort by design: a headless box has nothing to open with, and that
// is not a failure the person needs a dialog about.
func openOnHost(target string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	case "android", "ios":
		// The phone shells hand an address to the system themselves —
		// there is no opener to exec here, and pretending otherwise would
		// spawn a process that cannot exist.
		return
	default: // linux, *bsd
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, target)...).Start()
}
