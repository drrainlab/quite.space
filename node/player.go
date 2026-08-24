package node

// THE PLAYER — a video plays inside the conversation, and the code that
// plays it never stands next to the session token.
//
// The obvious way to put a video in a chat is to drop an iframe into the
// page. The obvious way is wrong HERE, and not by a little: this origin
// holds the token and can drive every route on the node, which is why its
// policy says script-src 'self' and connect-src 'self' and why the comment
// above uiPolicy calls script execution "the whole game". A third party's
// player loaded into that document is a permission granted forever so that
// one card can play.
//
// So the frame is NESTED, and the nesting is the entire design:
//
//	the interface frames THIS PAGE — same origin, so its policy needs to
//	  permit nothing but 'self', and an injection that abuses the
//	  directive can frame a page of ours it could already have opened;
//	this page frames the EMBED — under its own policy, in a document with
//	  no token, no application script and no way back up (the interface
//	  is cross-origin to nothing here, but there is nothing here to reach
//	  for either).
//
// One frame of distance, bought for one word in one directive. That is the
// difference between "youtube.com may run beside your keys" and "youtube
// .com may run inside a blank page we serve".
//
// It is deliberately unauthenticated. It holds nothing, reads nothing and
// says nothing about this node — its entire content is a video id that the
// person watching just clicked. Requiring the token would only mean
// putting the token in a URL bound for a document whose whole purpose is
// to talk to somebody else.

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// playerPolicy is the player's own policy: 'none' by default, one frame
// source, one image host, and no script at all — including ours.
//
// script-src 'none' is the load-bearing line. Everything permitted here is
// passive: a frame and a poster. Whatever runs, runs one level further
// down under Google's own policy, in a document this one cannot read.
//
// img-src names the poster host because the embed draws its still frame
// from it, and under `default-src 'none'` that was refused — the video
// played, but from a black rectangle. This is the right place to permit
// it and the interface is not: an image loaded HERE is loaded by a page
// carrying no token, only after somebody pressed play.
//
// frame-ancestors is 'self' because being framed by the conversation is
// the point. Nobody else can: this page is only ever served from loopback,
// to a document from the same origin.
const playerPolicy = "default-src 'none'; " +
	"frame-src https://www.youtube-nocookie.com; " +
	"img-src https://i.ytimg.com https://i9.ytimg.com; " +
	"style-src 'unsafe-inline'; " +
	"script-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'self'"

func (a *APIServer) handlePlayer(w http.ResponseWriter, r *http.Request) {
	// The id goes through the same alphabet check the card was built with,
	// here as well as there: this handler is reachable on its own, so it
	// verifies rather than assumes.
	vid := validVideoID(r.URL.Query().Get("v"))
	h := w.Header()
	h.Set("Content-Security-Policy", playerPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	// THE ONE PLACE IN THIS TREE THAT SENDS A REFERRER, and it has to.
	//
	// no-referrer here produced YouTube error 153 — "video player
	// configuration error" — which is what its embed says when it cannot
	// tell who is embedding it. An embed is a contract between a page and
	// a host, and a host that is shown nothing refuses.
	//
	// `origin` sends the ORIGIN and never the path: "http://127.0.0.1:PORT".
	// That is a loopback address, true of every copy of this app, and it
	// says nothing about this person that the request for a specific video
	// did not already say. The interface itself keeps no-referrer.
	h.Set("Referrer-Policy", "origin")
	h.Set("Content-Type", "text/html; charset=utf-8")
	// X-Frame-Options has no "same origin plus a policy" mode that agrees
	// with frame-ancestors, and where the two disagree browsers take the
	// stricter. SAMEORIGIN is what this page means.
	h.Set("X-Frame-Options", "SAMEORIGIN")
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
		// modestbranding is the small courtesy: this frame is a player,
		// not a place.
		"modestbranding": {"1"},
	}
	// Start time, when the person pasted one. Digits only.
	if t := digitsOnly(r.URL.Query().Get("t"), 7); t != "" {
		q.Set("start", t)
	}
	src := "https://www.youtube-nocookie.com/embed/" + vid + "?" + q.Encode()
	// The permissions are delegated a level at a time: this frame was
	// granted them by the conversation, and hands the same set down. Miss
	// one here and the embed silently loses it — autoplay first.
	w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + vid + `</title>` +
		`<style>html,body{margin:0;height:100%;background:#000;overflow:hidden}` +
		`iframe{border:0;width:100%;height:100%;display:block}</style>` +
		`</head><body><iframe src="` + src + `" ` +
		`allow="autoplay; encrypted-media; picture-in-picture; fullscreen" allowfullscreen></iframe></body></html>`))
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

// ---- an http origin for shells that have none ----

// ListenPlayer starts a loopback listener that serves the player page and
// NOTHING else, and returns its origin.
//
// This exists for one measured fact. The desktop shell serves the whole
// interface over a custom scheme — cmd/wails-probe reports
// `origin=wails://localhost` — and an embed refuses a page whose identity
// cannot travel in a Referer header, which a custom scheme's cannot. The
// browser and the phone both reach this node over http on loopback and
// need none of this; the desktop cannot get an http origin any other way,
// because it deliberately opens no port at all.
//
// So it opens the smallest one that can exist. The mux has ONE route. There
// is no token here and nothing a token would protect: whoever can reach
// this port gets a blank page that frames a video they named — which they
// could have opened in a browser without asking this process. Everything
// the node actually holds stays where it was, behind the handler this
// listener is not.
//
// The port is ephemeral and the listener lives as long as the process.
func (a *APIServer) ListenPlayer() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /player", a.handlePlayer)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	// The interface learns the origin the way it learns everything else
	// about its own node: from /api/status. It is a fact about this
	// process, not a secret, and threading it through the shell's window
	// would mean a second channel for one string.
	a.playerOrigin = "http://" + ln.Addr().String()
	return a.playerOrigin, nil
}
