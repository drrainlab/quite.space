// Local typed API (ADR-011): HTTP on 127.0.0.1 with a per-session token.
// The UI is a pure client of this API and contains no protocol logic. Every
// projection carries its honesty fields — there is no endpoint that returns
// a bare "online" or "delivered".
package node

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drrainlab/quiet_places/kernel/assets"
	"github.com/drrainlab/quiet_places/protocol/id"
	"github.com/drrainlab/quiet_places/protocol/signal"
	"github.com/drrainlab/quiet_places/terminals"
	qrcode "github.com/skip2/go-qrcode"
)

// APIServer wraps a Runtime for local HTTP access.
type APIServer struct {
	rt    *Runtime
	token string
	ui    fs.FS // optional embedded web UI

	// One pairing flow at a time (MD-1): the UI's poll-driven view of it.
	pairingMu sync.Mutex
	pairing   *pairingUI
}

// NewAPIServer creates the server with a fresh session token.
func NewAPIServer(rt *Runtime, ui fs.FS) (*APIServer, error) {
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	return &APIServer{rt: rt, token: hex.EncodeToString(tok), ui: ui}, nil
}

// Token returns the session token (printed as part of the UI URL).
func (a *APIServer) Token() string { return a.token }

// SetToken overrides the session token (dev/test convenience — a stable
// local URL across restarts; the API still binds 127.0.0.1 only).
func (a *APIServer) SetToken(t string) {
	if t != "" {
		a.token = t
	}
}

// Serve binds 127.0.0.1 (only) and serves until the listener closes.
// Returns the bound address.
func (a *APIServer) Serve(port int) (string, net.Listener, error) {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		return "", nil, err
	}
	go http.Serve(l, loopbackOnly(a.Handler()))
	return l.Addr().String(), l, nil
}

// loopbackOnly refuses a request that reached the listener under somebody
// else's name, or from somebody else's page.
//
// BINDING 127.0.0.1 DOES NOT MEAN ONLY THIS MACHINE CAN REACH IT. A web
// page the person is merely visiting can point a name it controls at
// 127.0.0.1 (DNS rebinding) and then talk to this API from its own
// JavaScript, in their browser, with their network position. What arrives
// looks local because it IS local — the packet really did come from
// loopback. The one thing that gives it away is the name the request asked
// for: the interface asks for 127.0.0.1 or localhost, and a rebound page
// asks for evil.example. So the Host is checked, and an Origin, when the
// browser sends one, has to be one of ours too.
//
// This wraps the LISTENER rather than the routes, and that placement is
// the point. A rebinding attack needs a TCP port to rebind ONTO; a host
// that mounts Handler() somewhere with no listener at all — the Wails
// AssetServer in cmd/wails-probe, and the desktop shell after it — has no
// such surface, and would fail a loopback-name check for the honest
// reason that its WebView asks for whatever name the framework chose.
// Guarding the listener guards exactly the thing that can be attacked, and
// leaves the thing that cannot alone.
//
// Not a replacement for the token: this stops a stranger's page from
// reaching the API, and the token stops everything else. Both, or neither
// is worth much.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackName(hostOnly(r.Host)) {
			httpErr(w, http.StatusForbidden,
				errors.New("this API answers to 127.0.0.1 and localhost only"))
			return
		}
		// A same-origin fetch from the interface sends either no Origin or
		// our own; anything else is another page talking to us.
		if o := r.Header.Get("Origin"); o != "" && !loopbackOrigin(o) {
			httpErr(w, http.StatusForbidden,
				errors.New("cross-origin requests are not accepted here"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostOnly strips the port from a Host header, leaving the name.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]") // bare IPv6, or a name with no port
}

// loopbackName says whether a Host names this machine's loopback. An empty
// Host is refused rather than waved through: HTTP/1.1 requires one, and
// nothing that speaks to this API omits it.
func loopbackName(h string) bool {
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	// Any loopback address, not just 127.0.0.1 — the whole 127/8 block and
	// ::1 all reach this listener.
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// loopbackOrigin checks an Origin header the same way. A malformed origin
// is refused, not parsed generously.
func loopbackOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	return loopbackName(hostOnly(u.Host))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Handler builds the route table.
func (a *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", a.auth(a.handleStatus))
	mux.HandleFunc("GET /api/onboarding", a.auth(a.handleOnboarding))
	mux.HandleFunc("POST /api/identity/name", a.auth(a.handleSetName))
	mux.HandleFunc("GET /api/devices", a.auth(a.handleDevices))
	mux.HandleFunc("POST /api/devices/{id}/revoke", a.auth(a.handleRevokeDevice))
	mux.HandleFunc("POST /api/pairing", a.auth(a.handleBeginPairing))
	mux.HandleFunc("GET /api/pairing", a.auth(a.handlePairingStatus))
	mux.HandleFunc("POST /api/pairing/approve", a.auth(a.handleApprovePairing))
	mux.HandleFunc("DELETE /api/pairing", a.auth(a.handleCancelPairing))
	mux.HandleFunc("GET /api/passcode", a.auth(a.handlePasscodeInfo))
	mux.HandleFunc("POST /api/passcode", a.auth(a.handlePasscodeBind))
	mux.HandleFunc("GET /api/spaces", a.auth(a.handleSpaces))
	mux.HandleFunc("POST /api/spaces", a.auth(a.handleCreateSpace))
	// SD-0. DELETE, because that is what it is — and it deletes THIS
	// DEVICE'S copy, which is the only copy anybody here can speak for.
	mux.HandleFunc("DELETE /api/spaces/{id}", a.auth(a.handleDeleteSpace))
	mux.HandleFunc("GET /api/spaces/{id}/messages", a.auth(a.handleMessages))
	mux.HandleFunc("POST /api/spaces/{id}/messages", a.auth(a.handleSay))
	mux.HandleFunc("GET /api/spaces/{id}/state", a.auth(a.handleState))
	mux.HandleFunc("POST /api/spaces/{id}/cards", a.auth(a.handleMakeCard))
	mux.HandleFunc("POST /api/spaces/{id}/cards/{card}/status", a.auth(a.handleCardStatus))
	mux.HandleFunc("POST /api/spaces/{id}/invites", a.auth(a.handleMintInvite))
	mux.HandleFunc("GET /api/spaces/{id}/members", a.auth(a.handleMembers))
	mux.HandleFunc("GET /api/spaces/{id}/entries", a.auth(a.handleEntries))
	mux.HandleFunc("POST /api/spaces/{id}/blocks", a.auth(a.handlePostBlock))
	mux.HandleFunc("GET /api/spaces/{id}/assets/{asset}", a.auth(a.handleGetAsset))
	mux.HandleFunc("POST /api/spaces/{id}/assets/{asset}/fetch", a.auth(a.handleFetchAsset))
	mux.HandleFunc("POST /api/spaces/{id}/reactions", a.auth(a.handleResonance))
	mux.HandleFunc("GET /api/spaces/{id}/resonance/palette", a.auth(a.handleGetResonancePalette))
	mux.HandleFunc("PUT /api/spaces/{id}/resonance/palette", a.auth(a.handleSetResonancePalette))
	mux.HandleFunc("GET /api/spaces/{id}/resonance/{target}/actors", a.auth(a.handleResonanceActors))
	mux.HandleFunc("POST /api/spaces/{id}/presence", a.auth(a.handlePresence))
	mux.HandleFunc("POST /api/invites/accept", a.auth(a.handleJoin))
	mux.HandleFunc("POST /api/public/open", a.auth(a.handlePublicOpen))
	// The transient post preview (PS-3): a card invites a look, and looking
	// persists nothing. Session-scoped asset route — see node/preview.go.
	mux.HandleFunc("POST /api/public/preview", a.auth(a.handlePublicPreview))
	// Looking at a SPACE rather than at one post (CAT-0b) — the same
	// transient session, asked what the place is and what it lists.
	mux.HandleFunc("POST /api/public/inspect", a.auth(a.handlePublicInspect))
	mux.HandleFunc("POST /api/public/previews/{pid}/close", a.auth(a.handlePreviewClose))
	mux.HandleFunc("POST /api/public/follow", a.auth(a.handlePublicFollow))
	mux.HandleFunc("GET /api/public/previews/{pid}/assets/{asset}", a.auth(a.handlePreviewAsset))
	mux.HandleFunc("POST /api/public/previews/{pid}/assets/{asset}/fetch", a.auth(a.handlePreviewFetch))
	mux.HandleFunc("GET /api/spaces/{id}/link", a.auth(a.handlePublicLink))
	mux.HandleFunc("POST /api/spaces/{id}/join", a.auth(a.handlePublicJoin))
	mux.HandleFunc("POST /api/spaces/{id}/policy", a.auth(a.handleRevisePolicy))
	mux.HandleFunc("POST /api/spaces/{id}/mirror", a.auth(a.handleSetMirror))
	// Sending a message somewhere else (SHARE-1). One act, one copy per
	// target, each sealed with that space's own epoch.
	mux.HandleFunc("POST /api/share", a.auth(a.handleShare))
	// What happened to it afterwards (SHARE-2). Runtime.Delivery has been
	// unreachable since RB-1; one act with three destinations has three
	// answers, and "sent" is not one of them.
	mux.HandleFunc("GET /api/deliveries", a.auth(a.handleDeliveries))
	// The local assistant (AI-0): an ordinary Agent Terminal in a space
	// that never leaves this device.
	mux.HandleFunc("GET /api/ai", a.auth(a.handleAI))
	mux.HandleFunc("POST /api/ai/ask", a.auth(a.handleAsk))
	// The Navigator (NAV-0): how this device arranges what it already has.
	// Whole-document PUT with a base version — see node/navigator.go.
	mux.HandleFunc("GET /api/navigator", a.auth(a.handleNavigator))
	mux.HandleFunc("PUT /api/navigator", a.auth(a.handleSetNavigator))
	// The door: who is waiting, and the host's answer.
	mux.HandleFunc("PUT /api/spaces/{id}/name", a.auth(a.handleSetLocalTitle))
	mux.HandleFunc("GET /api/entry-requests", a.auth(a.handleEntryRequests))
	mux.HandleFunc("POST /api/entry-requests/{req}/decide", a.auth(a.handleDecideEntry))
	// QuietRank (AT-0): device-local attention layer.
	mux.HandleFunc("GET /api/signals", a.auth(a.handleSignals))
	mux.HandleFunc("POST /api/signals/{id}/seen", a.auth(a.handleSignalSeen))
	mux.HandleFunc("POST /api/signals/{id}/feedback", a.auth(a.handleSignalFeedback))
	mux.HandleFunc("POST /api/attention/notice", a.auth(a.handleAttentionNotice))
	mux.HandleFunc("GET /api/attention/policy", a.auth(a.handleGetAttentionPolicy))
	mux.HandleFunc("POST /api/attention/policy", a.auth(a.handleSetAttentionPolicy))
	mux.HandleFunc("POST /api/attention/forget", a.auth(a.handleForgetAttention))
	mux.HandleFunc("POST /api/attention/viewing", a.auth(a.handleViewing))
	mux.HandleFunc("POST /api/spaces/{id}/passes", a.auth(a.handleMintPass))
	mux.HandleFunc("GET /api/spaces/{id}/passes", a.auth(a.handleListPasses))
	mux.HandleFunc("DELETE /api/spaces/{id}/passes/{pass}", a.auth(a.handleRevokePass))
	a.routeQuickLinks(mux)
	a.routeBackup(mux)
	mux.HandleFunc("POST /api/join-requests", a.auth(a.handleJoinRequest))
	mux.HandleFunc("GET /api/join-requests/{req}", a.auth(a.handleJoinStatus))
	mux.HandleFunc("GET /api/gateway", a.auth(a.handleGateway))
	mux.HandleFunc("POST /api/gateway/prepare", a.auth(a.handlePrepareRadio))

	// Meeting over the radio: no relay, no internet, no pasted link.
	mux.HandleFunc("POST /api/radio/attach", a.auth(a.handleRadioAttach))
	mux.HandleFunc("POST /api/radio/detach", a.auth(a.handleRadioDetach))
	mux.HandleFunc("POST /api/radio/announce", a.auth(a.handleRadioAnnounce))
	mux.HandleFunc("GET /api/radio/neighbours", a.auth(a.handleRadioNeighbours))
	mux.HandleFunc("POST /api/radio/meet", a.auth(a.handleRadioMeet))
	mux.HandleFunc("POST /api/radio/invite", a.auth(a.handleRadioInvite))
	mux.HandleFunc("GET /api/radio/invitations", a.auth(a.handleRadioOffers))
	mux.HandleFunc("POST /api/radio/invitations/accept", a.auth(a.handleRadioAccept))
	mux.HandleFunc("POST /api/gateway/apply", a.auth(a.handleApplyRadio))
	mux.HandleFunc("POST /api/gateway/scan", a.auth(a.handleScanRadios))
	mux.HandleFunc("POST /api/gateway/attach", a.auth(a.handleAttachRadio))
	mux.HandleFunc("POST /api/gateway/profile", a.auth(a.handleAdoptProfile))
	mux.HandleFunc("POST /api/gateway/pin", a.auth(a.handlePinGateway))
	mux.HandleFunc("POST /api/gateway/unpin", a.auth(a.handleUnpinGateway))
	mux.HandleFunc("GET /api/settings", a.auth(a.handleGetSettings))
	mux.HandleFunc("POST /api/settings", a.auth(a.handleSetSettings))
	mux.HandleFunc("POST /api/settings/llm/test", a.auth(a.handleTestLLM))
	mux.HandleFunc("GET /api/spaces/{id}/appearance", a.auth(a.handleAppearance))
	mux.HandleFunc("PATCH /api/spaces/{id}/appearance", a.auth(a.handlePatchAppearance))
	mux.HandleFunc("POST /api/spaces/{id}/appearance/proposals", a.auth(a.handleProposeAppearance))
	mux.HandleFunc("GET /api/spaces/{id}/publications", a.auth(a.handleListPublications))
	mux.HandleFunc("POST /api/spaces/{id}/assets", a.auth(a.handleUploadAsset))
	mux.HandleFunc("POST /api/spaces/{id}/publications/proposals", a.auth(a.handleProposeDocument))
	mux.HandleFunc("POST /api/spaces/{id}/publications", a.auth(a.handlePublish))
	mux.HandleFunc("GET /api/spaces/{id}/publications/{doc}", a.auth(a.handleGetPublication))
	mux.HandleFunc("POST /api/spaces/{id}/publications/{doc}/archive", a.auth(a.handleArchivePublication))
	mux.HandleFunc("POST /api/spaces/{id}/publications/{doc}/comments", a.auth(a.handleComment))
	mux.HandleFunc("GET /api/spaces/{id}/apps", a.auth(a.handleListApps))
	mux.HandleFunc("POST /api/spaces/{id}/apps", a.auth(a.handleCreateApp))
	mux.HandleFunc("GET /api/spaces/{id}/apps/{instance}", a.auth(a.handleAppMeta))
	mux.HandleFunc("GET /api/spaces/{id}/apps/{instance}/inputs/{input}", a.auth(a.handleAppInput))
	mux.HandleFunc("GET /api/spaces/{id}/apps/{instance}/session", a.auth(a.handleListeningSession))
	mux.HandleFunc("GET /api/time", a.auth(a.handleTime))
	mux.HandleFunc("POST /api/spaces/{id}/apps/{instance}/actions/{action}", a.auth(a.handleAppAction))
	mux.HandleFunc("GET /api/spaces/{id}/drafts", a.auth(a.handleDrafts))
	mux.HandleFunc("POST /api/spaces/{id}/drafts", a.auth(a.handleDrafts))
	mux.HandleFunc("GET /api/spaces/{id}/drafts/{doc}", a.auth(a.handleGetDraft))
	mux.HandleFunc("DELETE /api/spaces/{id}/drafts/{doc}", a.auth(a.handleDeleteDraft))
	mux.HandleFunc("POST /api/spaces/{id}/keep", a.auth(a.handleKeep))
	mux.HandleFunc("POST /api/spaces/{id}/unkeep", a.auth(a.handleUnkeep))
	mux.HandleFunc("GET /api/spaces/{id}/shelf", a.auth(a.handleShelf))
	mux.HandleFunc("GET /api/spaces/{id}/composition", a.auth(a.handleComposition))
	mux.HandleFunc("GET /api/spaces/{id}/bundles", a.auth(a.handleBundles))
	mux.HandleFunc("POST /api/lan/connect", a.auth(a.handleConnect))
	mux.HandleFunc("POST /api/mesh/connect", a.auth(a.handleMeshConnect))
	mux.HandleFunc("POST /api/relay/push", a.auth(a.handleRelayPush))
	mux.HandleFunc("POST /api/relay/pull", a.auth(a.handleRelayPull))
	mux.HandleFunc("GET /api/suggested-directory", a.auth(a.handleSuggestedDirectory))
	mux.HandleFunc("GET /api/relay/status", a.auth(a.handleRelayStatus))
	mux.HandleFunc("GET /api/relay/diagnostics", a.auth(a.handleRelayDiagnostics))
	mux.HandleFunc("POST /api/relay/identity", a.auth(a.handleRelayIdentity))
	mux.HandleFunc("POST /api/relay/trust", a.auth(a.handleRelayTrust))
	mux.HandleFunc("POST /api/relay/remeasure", a.auth(a.handleRelayRemeasure))
	if a.ui != nil {
		mux.Handle("GET /", uiSecurityHeaders(http.FileServerFS(a.ui)))
	}
	return mux
}

// uiPolicy is the interface document's Content-Security-Policy.
//
// This origin holds the session token and can drive every route above, so
// script execution here is the whole game: the policy's job is to make an
// injection worth as little as possible.
//
// connect-src is the load-bearing directive. It is what stops injected
// script from POSTing the log, the token or a space key to somebody else's
// server — the silent channels (fetch, XHR, WebSocket, beacon) are closed,
// and img-src/form-action/base-uri close the quiet ones behind them.
// Top-level navigation remains possible and is deliberately not claimed to
// be covered: navigate-to left the spec, so `location = "https://…"` still
// works — noisily, in front of the person.
//
// script-src IS 'self' — earned, not assumed. It carried 'unsafe-inline'
// while index.html declared ~230 inline on* handlers: all ours, all
// literal, none reachable by remote content, so never the defect — but
// they were what a strict policy would have taken down with them, and
// while they were there the permission that kept them alive was the same
// permission that lets an INJECTED script or a javascript: URI run. They
// now live in handlers.js, bound one listener per element, and the
// permission is gone. An injection has nothing to execute with.
//
// 'unsafe-eval' is NOT present and must not be added: nothing in the tree
// needs it, and ADR-013 rests on that.
//
// style-src keeps 'unsafe-inline' for the same shape of reason (inline
// style attributes plus the renderers' own el.style writes). CSS injection
// is a far smaller prize than script, and the palette/tint values that
// reach a style are hex-validated at renderers.js:115.
const uiPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"frame-src 'none'; " +
	"worker-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// uiSecurityHeaders serves the interface under uiPolicy. The headers ride
// on the document rather than a <meta> tag so that frame-ancestors and
// X-Frame-Options apply at all — a meta CSP silently ignores both.
func uiSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", uiPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// The token rides in query strings on media URLs (an <img> cannot
		// send a header). Keep it out of the Referer of anything we link to.
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (a *APIServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-QP-Token")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		// Constant time, because `!=` on strings returns as soon as two
		// bytes differ and so takes measurably longer for a token that
		// shares a longer prefix. Over loopback against 128 random bits
		// that is not a practical attack — but this token opens every
		// route, and a comparison that leaks its own progress is not the
		// place to be relaxed about it.
		if subtle.ConstantTimeCompare([]byte(tok), []byte(a.token)) != 1 {
			httpErr(w, http.StatusUnauthorized, errors.New("missing or wrong token"))
			return
		}
		next(w, r)
	}
}

func httpErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (a *APIServer) spaceID(r *http.Request) (id.TerminalID, error) {
	return id.ParseTerminalID(r.PathValue("id"))
}

// ---- Handlers ----

type statusResp struct {
	Fingerprint string `json:"fingerprint"`
	DeviceID    string `json:"device_id"`
	DeviceXPub  string `json:"device_xpub"`
	LAN         struct {
		Listening bool `json:"listening"`
		Port      int  `json:"port"`
		Peers     int  `json:"peers"`
		// bound = peers whose DEVICE is authenticated on the wire (T6-LAN):
		// the number the chip may honestly call "in the room".
		Bound int `json:"bound"`
	} `json:"lan"`
	Mesh struct {
		Connected bool   `json:"connected"`
		NodeNum   uint32 `json:"node_num"`
		TX        int    `json:"tx"`
		RX        int    `json:"rx"`
		Err       string `json:"err,omitempty"`
		// What became of the packets we asked to be delivered reliably, as
		// the firmware reported it. TX says what we handed over; these say
		// what happened next, and until they existed a retry that worked
		// and one that never ran looked identical from here.
		Acked       int `json:"acked"`
		GaveUp      int `json:"gave_up"`
		Outstanding int `json:"outstanding"`
		// The radio's own queue, and how many packets it REFUSED. A refusal
		// never reached the air however healthy tx looked.
		QueueFree  int            `json:"queue_free"`
		QueueMax   int            `json:"queue_max"`
		Refused    int            `json:"refused"`
		QueueKnown bool           `json:"queue_known"`
		Transfer   map[string]any `json:"transfer,omitempty"`
	} `json:"mesh"`

	// Radio is the CARRIER-NEUTRAL face, and it is the one a client must read
	// to answer "is a radio there at all".
	//
	// The mesh object above it is a Meshtastic diagnostic wearing a generic
	// name, and Mesh() returns an EMPTY struct whenever that driver is not the
	// one attached. So an RNode could be connected, transmitting and carrying
	// a conversation while every surface in the interface said no radio was
	// connected — which is exactly what happened for the whole of the live
	// gate, unnoticed because the gate drove the API directly and never looked
	// at a screen. RadioStatus existed for this and had no caller.
	Radio RadioStatus `json:"radio"`
	// The policy in force, beside the facts it governs. A screen that reads
	// the sockets alone will describe a way out that the policy forbids: an
	// offline node still HAS a LAN listener, and reporting "local network" to
	// somebody who chose offline tells them their choice did not take.
	Connectivity ConnectivityStatus `json:"connectivity"`
	// What one message may carry, so an interface can refuse a file at the
	// moment it is CHOSEN rather than after somebody has written a caption
	// and an alt text for it. Reported rather than duplicated in the client:
	// two copies of a limit are one limit and one bug waiting for the day
	// they differ.
	MaxAssetBytes int64 `json:"max_asset_bytes"`
}

func (a *APIServer) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"needs_name": a.rt.NeedsOnboarding(),
		// first_run is what may TAKE THE SCREEN; needs_name is only a nudge.
		// Reported separately because the client conflated them and, on a
		// phone, that conflation was a trap the person could not leave.
		"first_run":   a.rt.IsFirstRun(),
		"name":        a.rt.DisplayName(),
		"device_name": deviceName(),
		"fingerprint": a.rt.Fingerprint(),
	})
}

func (a *APIServer) handleSetName(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Name string `json:"name"`
	}](r)
	if err != nil || strings.TrimSpace(body.Name) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("name required"))
		return
	}
	if err := a.rt.SetName(body.Name); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"name": a.rt.DisplayName()})
}

// deviceName returns a friendly, platform-neutral device label.
func deviceName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "this device"
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	var resp statusResp
	resp.Fingerprint = a.rt.Fingerprint()
	resp.DeviceID = a.rt.Device.ID.Hex()
	resp.DeviceXPub = hex.EncodeToString(a.rt.Device.X25519Pub[:])
	l := a.rt.LAN()
	resp.LAN.Listening, resp.LAN.Port, resp.LAN.Peers = l.Listening, l.Port, l.Peers
	resp.LAN.Bound = l.Bound
	// The neutral face first, because it is the one that answers for every
	// carrier. Detail is dropped HERE and nowhere else: the mesh object below
	// already carries the Meshtastic diagnostic in the shape the client reads,
	// and MeshStatus has no json tags, so serialising it a second time would
	// put one set of facts on the wire under two different spellings.
	resp.Radio = a.rt.RadioState()
	resp.Radio.Detail = nil
	resp.Connectivity = a.rt.Connectivity()
	resp.MaxAssetBytes = assets.MaxAssetSize

	m := a.rt.Mesh()
	resp.Mesh.Connected, resp.Mesh.NodeNum = m.Connected, m.NodeNum
	resp.Mesh.TX, resp.Mesh.RX, resp.Mesh.Err = m.TX, m.RX, m.Err
	resp.Mesh.Acked, resp.Mesh.GaveUp, resp.Mesh.Outstanding = m.Acked, m.GaveUp, m.Outstanding
	resp.Mesh.QueueFree, resp.Mesh.QueueMax = m.QueueFree, m.QueueMax
	resp.Mesh.Refused, resp.Mesh.QueueKnown = m.Refused, m.QueueKnown
	// Whole-message delivery. Everything above counts PACKETS, and a packet
	// count cannot tell a carrier problem from a reassembly one — which is
	// what nine days of measurement proved the expensive way.
	if m.Transfer != nil {
		resp.Mesh.Transfer = map[string]any{
			"attempted":   m.Transfer.Attempted,
			"completed":   m.Transfer.Completed,
			"gaveUp":      m.Transfer.GaveUp,
			"framesOut":   m.Transfer.FramesOut,
			"refused":     m.Transfer.Refused,
			"framesIn":    m.Transfer.FramesIn,
			"inbound":     m.Transfer.Inbound,
			"inboundHave": m.Transfer.InboundHave,
			"queued":      m.TransferQueue[0],
			"dropped":     m.TransferQueue[1],
			"failed":      m.TransferQueue[2],
		}
	}
	writeJSON(w, resp)
}

func (a *APIServer) handleMeshConnect(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Target string `json:"target"`
	}](r)
	if err != nil || body.Target == "" {
		httpErr(w, http.StatusBadRequest, errors.New("target required: tcp:HOST[:PORT] or serial:/dev/PATH"))
		return
	}
	if err := a.rt.StartMeshtastic(strings.TrimSpace(body.Target)); err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type characterResp struct {
	Archetype string   `json:"archetype"`
	Mood      string   `json:"mood"`
	Material  string   `json:"material"`
	Motion    string   `json:"motion"`
	Geometry  string   `json:"geometry"`
	Central   string   `json:"central"`
	Memory    string   `json:"memory"`
	Relic     string   `json:"relic"`
	Rituals   []string `json:"rituals"`
	Presence  []string `json:"presence"`
}

func characterOf(c terminals.Character) characterResp {
	return characterResp{
		Archetype: c.Archetype, Mood: c.Mood, Material: c.Material,
		Motion: c.Motion, Geometry: c.Geometry, Central: c.Central,
		Memory: c.Memory, Relic: c.Relic,
		Rituals: c.Rituals, Presence: c.Presence,
	}
}

// displayResp mirrors node.SpaceDisplay on the wire.
type displayResp struct {
	Text  string   `json:"text,omitempty"`
	Key   string   `json:"key,omitempty"`
	Names []string `json:"names,omitempty"`
}

type spaceResp struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// DisplayTitle is what to show in a list. Usually the title; for an
	// unnamed line between two people, the other person. See lineDisplayTitle.
	DisplayTitle string `json:"display_title"`
	// Display is the structured form: either a name somebody chose, or a
	// key plus the people here, so the interface can say it in the
	// reader's language rather than receiving English from Go.
	Display displayResp `json:"display"`
	// Dyad: exactly one other person here. The Navigator's People section
	// reads THIS, never display.key — see node/display.go isDisplayDyad.
	Dyad bool `json:"dyad,omitempty"`
	// AI marks the local assistant's space, so the Navigator draws it as
	// what it is rather than as a person or an ordinary room (AI-0).
	AI            bool          `json:"ai,omitempty"`
	Owned         bool          `json:"owned"`
	Events        int           `json:"events"`
	Messages      int           `json:"messages"`
	Undecryptable int           `json:"undecryptable"`
	Peers         int           `json:"peers"`
	Character     characterResp `json:"character"`
	// PA-0 access surface.
	Visibility      string `json:"visibility,omitempty"` // "" private | unlisted | public
	Join            string `json:"join,omitempty"`       // "" | "open"
	Publish         string `json:"publish,omitempty"`    // "" all | "curated"
	Role            string `json:"role,omitempty"`       // "" member/owner | "reader"
	CanWrite        bool   `json:"can_write"`
	Frozen          bool   `json:"frozen,omitempty"`
	IgnoredByPolicy uint64 `json:"ignored_by_policy,omitempty"`
	// RatePerCycle is the owner's signed contribution limit (IC-1), 0 for
	// none. It comes from the manifest, so the control reflects what the
	// space actually says rather than what was last typed at it.
	RatePerCycle int `json:"rate_per_cycle,omitempty"`
	// Kind is what the space DECLARES it is for (CAT-0b): "" ordinary, or
	// "directory". Signed by the space, so this is the owner's statement
	// rather than a word somebody typed into a tag — but it confers nothing
	// and the client reads it only to draw the place as what it says it is.
	Kind string `json:"kind,omitempty"`
	// PH-3 availability roles this node has volunteered for. Neither confers
	// any authority over the space.
	Mirror bool `json:"mirror,omitempty"`
	Seed   bool `json:"seed,omitempty"`
}

func (a *APIServer) handleSpaces(w http.ResponseWriter, r *http.Request) {
	spaces := a.rt.Spaces()
	aiSpace := a.rt.AI().Space
	out := make([]spaceResp, 0, len(spaces))
	for _, s := range spaces {
		resp := spaceResp{
			ID: s.ID.Hex(), Title: s.Title, DisplayTitle: s.DisplayTitle, Owned: s.Owned,
			Display: displayResp{Text: s.Display.Text, Key: s.Display.Key,
				Names: s.Display.Names},
			Dyad:   s.Dyad,
			AI:     aiSpace != "" && s.ID.Hex() == aiSpace,
			Events: s.Events, Messages: s.Messages,
			Undecryptable: s.Undecryptable, Peers: s.Peers,
		}
		// One locked scope per space. The replica read and the keystore read
		// used to be separate critical sections with an unlocked projection
		// between them; now they are one, so a space cannot be re-shaped by
		// relay sync halfway through its own row.
		//
		// A space listed a moment ago can already be gone (Spaces() snapshots
		// under its own lock): it simply stays unprojected, as before.
		_ = a.rt.withSpace(s.ID, func(st *spaceState) error {
			_, c := st.space.Character()
			resp.Character = characterOf(c)
			pol := st.space.Policy()
			if pol.IsPublic() {
				resp.Visibility = string(pol.Visibility)
				resp.Join = pol.Join
				resp.Publish = pol.Publish
				resp.Frozen = pol.Frozen
				resp.RatePerCycle = pol.MaxFramesPerAuthor
				resp.Kind = pol.Kind
			}
			resp.IgnoredByPolicy = st.space.PolicyStats.IgnoredTotal
			meta := a.rt.ks.Spaces[s.ID]
			resp.Role = meta.Role
			resp.Mirror, resp.Seed = meta.Mirror, meta.Seed
			resp.CanWrite = a.rt.canWrite(st) == nil
			return nil
		})
		out = append(out, resp)
	}
	writeJSON(w, out)
}

func readBody[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&v)
	return v, err
}

func (a *APIServer) handleCreateSpace(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Title     string   `json:"title"`
		Archetype string   `json:"archetype"`
		Mood      string   `json:"mood"`
		Material  string   `json:"material"`
		Motion    string   `json:"motion"`
		Geometry  string   `json:"geometry"`
		Central   string   `json:"central"`
		Memory    string   `json:"memory"`
		Rituals   []string `json:"rituals"`
		Presence  []string `json:"presence"` // extra custom states
		// PA-0 access policy (absent = private, unchanged behavior).
		Visibility string `json:"visibility"` // "" | "unlisted" | "public"
		Join       string `json:"join"`       // "" | "open"
		Publish    string `json:"publish"`    // "" (all) | "curated"
		// What the space is FOR (CAT-0b): "" ordinary | "directory".
		// Public spaces only; a private create carrying one is refused by
		// the policy's own Validate rather than here.
		Kind string `json:"kind"`
	}](r)
	// An empty title is a CHOICE, not a missing value: the space will be
	// called after whoever is in it. Requiring a name here forced people to
	// invent one before they knew what the place was.
	unnamed := strings.TrimSpace(body.Title) == ""
	arch := body.Archetype
	if arch == "" {
		arch = "campfire"
	}
	c := terminals.DefaultCharacter(arch)
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&c.Mood, body.Mood)
	set(&c.Material, body.Material)
	set(&c.Motion, body.Motion)
	set(&c.Geometry, body.Geometry)
	set(&c.Central, body.Central)
	set(&c.Memory, body.Memory)
	if len(body.Rituals) > 0 {
		c.Rituals = body.Rituals
	}
	for _, p := range body.Presence {
		if !slices.Contains(c.Presence, p) {
			c.Presence = append(c.Presence, p)
		}
	}
	pol := terminals.SpacePolicy{
		Visibility: terminals.Visibility(body.Visibility),
		Join:       body.Join,
		Publish:    body.Publish,
		Kind:       body.Kind,
	}
	// Beta simple mode (RR-5): a NEW public space signs its creator's
	// relay as the space's relay set — the one moment the personal relay
	// legitimately becomes a space's address. Members and mirrors follow
	// qp.relay from here on; changing it later is a policy revision.
	if pol.IsPublic() {
		if ref := a.rt.PersonalRelayRef(); ref != "" {
			pol.Relays = []string{ref}
		}
	}
	tid, err := a.rt.CreateSpaceWithOptions(strings.TrimSpace(body.Title),
		CreateOptions{Character: c, Policy: pol})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if unnamed {
		// Record that nobody named it, so the list projects who is in it
		// rather than showing an empty title.
		if err := a.rt.MarkUnnamed(tid); err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, map[string]string{"id": tid.Hex()})
}

// SD-0 — deleting this device's copy of a space.
//
// THE RESPONSE SAYS WHAT WAS ACTUALLY DONE, in the same words the interface
// uses, because this is the operation where a comfortable phrase does real
// harm: nobody else's copy is touched, nothing is sent, and the people in the
// space are not told. A person deleting a conversation deserves to know
// exactly which of those they are getting.
//
// It is not idempotent-by-pretending: deleting a space that is not here
// answers 404 rather than "ok". A client that reports success for a space it
// could not find will one day report it for the wrong one.
func (a *APIServer) handleDeleteSpace(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.rt.DeleteSpace(tid); err != nil {
		if errors.Is(err, ErrNoSuchSpace) {
			httpErr(w, http.StatusNotFound, err)
			return
		}
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{
		"deleted": tid.Hex(),
		"scope":   "this_device",
		"note":    "Deleted from this device. Other members keep their copy.",
	})
}

type messageResp struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Text       string `json:"text"`
	ProducedBy string `json:"produced_by"`
	Revised    bool   `json:"revised"`
	Clock      uint64 `json:"clock"`
	Mine       bool   `json:"mine"`
	// External is who the GATEWAY says this came from (TR-0, key 7).
	// Present only when the envelope's authorship is imported: the payload
	// can carry this structure from anybody, the signature cannot, and a
	// renderer that trusted the payload alone would hand every member a
	// stencil for forging somebody's email.
	External *externalResp `json:"external,omitempty"`
}

type externalResp struct {
	Connector string   `json:"connector,omitempty"`
	Address   string   `json:"address,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	LossFlags []string `json:"loss_flags,omitempty"`
}

func (a *APIServer) handleMessages(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	var out []messageResp
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		msgs := st.space.State.Messages()
		out = make([]messageResp, 0, len(msgs))
		for _, m := range msgs {
			row := messageResp{
				ID: m.ID.Hex(), Author: m.Author.String(), Text: m.Text,
				ProducedBy: m.ProducedBy.String(), Revised: m.Revised,
				Clock: m.Clock, Mine: m.Author == a.rt.PrincipalID,
			}
			if m.External != nil && m.ProducedBy == signal.AuthorshipImported {
				row.External = &externalResp{
					Connector: m.External.ConnectorKind,
					Address:   m.External.Address,
					LossFlags: m.External.LossFlags,
				}
			}
			out = append(out, row)
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, out)
}

func (a *APIServer) handleSay(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Text     string   `json:"text"`
		ReplyTo  string   `json:"reply_to"`
		Mentions []string `json:"mentions"`
	}](r)
	if err != nil || body.Text == "" {
		httpErr(w, http.StatusBadRequest, errors.New("text required"))
		return
	}
	var opt SayOptions
	if body.ReplyTo != "" {
		eid, err := parseEventID(body.ReplyTo)
		if err != nil {
			httpErr(w, http.StatusBadRequest, errors.New("bad reply_to"))
			return
		}
		opt.ReplyTo = &eid
	}
	for _, m := range body.Mentions {
		p, err := id.ParsePrincipalID(strings.TrimSpace(m))
		if err != nil {
			httpErr(w, http.StatusBadRequest, errors.New("bad mention id"))
			return
		}
		opt.Mentions = append(opt.Mentions, p)
	}
	eid, err := a.rt.Say(tid, body.Text, opt)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

type cardResp struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type observationResp struct {
	Display   string `json:"display"`
	Simulated bool   `json:"simulated"`
	Freshness string `json:"freshness"`
	AgeSec    uint64 `json:"age_seconds"`
}

type presenceResp struct {
	Known   bool   `json:"known"`
	Current bool   `json:"current"`
	State   string `json:"state"`
	AgeSec  uint64 `json:"age_seconds"`
}

type stateResp struct {
	Cards         []cardResp       `json:"cards"`
	Observation   *observationResp `json:"observation,omitempty"`
	Undecryptable int              `json:"undecryptable"`
	Events        int              `json:"events"`
}

func (a *APIServer) handleState(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	resp := stateResp{Cards: []cardResp{}}
	if err := a.rt.withSpace(tid, func(st *spaceState) error {
		resp.Undecryptable, resp.Events = st.space.Undecryptable, st.space.Log.Len()
		for _, c := range st.space.State.Cards() {
			resp.Cards = append(resp.Cards, cardResp{ID: c.ID.Hex(), Title: c.Title, Status: c.Status})
		}
		o, ok := st.space.State.LatestObservation()
		if !ok {
			return nil
		}
		now := uint64(time.Now().Unix())
		fresh := "unknown"
		age := uint64(0)
		if now > o.ObservedAt {
			age = now - o.ObservedAt
		}
		if o.Value.StaleAfter > 0 {
			if age > o.Value.StaleAfter {
				fresh = "stale"
			} else {
				fresh = "current"
			}
		}
		sign := ""
		if o.Value.Negative {
			sign = "-"
		}
		resp.Observation = &observationResp{
			Display:   sign + itoa(int(o.Value.CentiValue/100)) + "." + pad2(int(o.Value.CentiValue%100)) + " " + o.Value.Unit,
			Simulated: o.Value.Simulated,
			Freshness: fresh,
			AgeSec:    age,
		}
		return nil
	}); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, resp)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func (a *APIServer) handleMakeCard(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Title  string `json:"title"`
		Origin string `json:"origin"`
	}](r)
	if err != nil || body.Title == "" {
		httpErr(w, http.StatusBadRequest, errors.New("title required"))
		return
	}
	var origin *id.EventID
	if body.Origin != "" {
		h, err := hex.DecodeString(body.Origin)
		if err != nil || len(h) != id.Size {
			httpErr(w, http.StatusBadRequest, errors.New("bad origin event id"))
			return
		}
		var e id.EventID
		copy(e[:], h)
		origin = &e
	}
	eid, err := a.rt.MakeCard(tid, body.Title, origin)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": eid.Hex()})
}

func (a *APIServer) handleCardStatus(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	cardHex := r.PathValue("card")
	h, err := hex.DecodeString(cardHex)
	if err != nil || len(h) != id.Size {
		httpErr(w, http.StatusBadRequest, errors.New("bad card id"))
		return
	}
	var card id.EventID
	copy(card[:], h)
	body, err := readBody[struct {
		Title  string `json:"title"`
		Status string `json:"status"`
	}](r)
	if err != nil || body.Status == "" {
		httpErr(w, http.StatusBadRequest, errors.New("status required"))
		return
	}
	if err := a.rt.SetCardStatus(tid, card, body.Title, body.Status); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *APIServer) handleMintInvite(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		Device string `json:"device"`
		XPub   string `json:"xpub"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	dev, err := id.ParseDeviceID(body.Device)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad device id"))
		return
	}
	xb, err := hex.DecodeString(body.XPub)
	if err != nil || len(xb) != 32 {
		httpErr(w, http.StatusBadRequest, errors.New("bad xpub"))
		return
	}
	var xpub [32]byte
	copy(xpub[:], xb)
	invite, err := a.rt.MintInvite(tid, dev, xpub)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	resp := map[string]string{"invite": invite}
	// QR of the invite string (vision §6.2 step 2): scan or send, any channel.
	if png, err := qrcode.Encode(invite, qrcode.Medium, 512); err == nil {
		resp["qr_png_base64"] = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, resp)
}

type memberResp struct {
	Terminal       string   `json:"terminal"`
	Principal      string   `json:"principal"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Agency         string   `json:"agency"`
	AIPresent      bool     `json:"ai_present"`
	Autonomy       string   `json:"autonomy"`
	Model          string   `json:"model"` // "not specified" unless declared
	IOMode         string   `json:"io_mode"`
	Capabilities   []string `json:"capabilities"`
	DeclaredLabels []string `json:"declared_labels"`
	SysLabels      []string `json:"sys_labels"`
	Commandable    bool     `json:"commandable"`
	// Mine marks this node's own card, so a UI can show you your own state
	// without having to guess which member you are.
	Mine     bool `json:"mine"`
	Presence struct {
		Known   bool   `json:"known"`
		Current bool   `json:"current"`
		State   string `json:"state,omitempty"`
		AgeSec  uint64 `json:"age_seconds,omitempty"`
		// LeftSec is what the signed claim says is still to run, never a
		// local assumption about how long presence lasts.
		LeftSec uint64 `json:"remaining_seconds,omitempty"`
	} `json:"presence"`
}

func (a *APIServer) handleMembers(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	cards, err := a.rt.Members(tid)
	if err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	out := make([]memberResp, 0, len(cards))
	for _, c := range cards {
		m := memberResp{
			Terminal: c.Terminal.Hex(), Principal: c.Principal.Hex(), Name: c.Name,
			Kind: c.Kind, Agency: c.Agency,
			AIPresent: c.AIPresent, Autonomy: terminals.AutonomyLabel(c.Autonomy),
			Model: "not specified", IOMode: c.IOMode,
			Capabilities: c.Capabilities, DeclaredLabels: c.DeclaredLabels,
			SysLabels: c.SysLabels, Commandable: c.CanReceiveCommands,
			Mine: c.Principal == a.rt.PrincipalID,
		}
		m.Presence.Known = c.Presence.Known
		m.Presence.Current = c.Presence.Current
		m.Presence.State = c.Presence.State
		m.Presence.AgeSec = c.Presence.AgeSeconds
		m.Presence.LeftSec = c.Presence.RemainingSeconds
		out = append(out, m)
	}
	writeJSON(w, out)
}

func (a *APIServer) handlePresence(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		State string `json:"state"`
		TTL   uint64 `json:"ttl_seconds"`
	}](r)
	if err != nil || body.State == "" {
		httpErr(w, http.StatusBadRequest, errors.New("state required"))
		return
	}
	if body.TTL == 0 {
		body.TTL = 300
	}
	if err := a.rt.SetPresence(tid, body.State, body.TTL); err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *APIServer) handleJoin(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Invite string `json:"invite"`
	}](r)
	if err != nil || body.Invite == "" {
		httpErr(w, http.StatusBadRequest, errors.New("invite required"))
		return
	}
	// One paste box, three artifact kinds: a device invite, a Space Pass
	// (handled by the join-request flow), or a PUBLIC SPACE LINK — the
	// share container distinguishes them, so try the public route when the
	// invite route refuses.
	link := strings.TrimSpace(body.Invite)
	tid, err := a.rt.JoinInvite(link)
	if err != nil {
		if ptid, perr := a.rt.OpenPublicLink(link); perr == nil {
			writeJSON(w, map[string]string{"id": ptid.Hex(), "mode": "public"})
			return
		}
		httpErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, map[string]string{"id": tid.Hex()})
}

// ---- Space Pass (ADR-012 / UI-2) ----

// handleMintPass mints a bearer-secret Join Pass for an owned space. The
// returned link bundles the rendezvous relay with the signed pass; the bearer
// secret lives only inside that link, never in the pass registry on this node.
func (a *APIServer) handleMintPass(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	body, err := readBody[struct {
		MaxUses  uint64 `json:"max_uses"`
		TTLHours uint64 `json:"ttl_hours"`
		Relay    string `json:"relay"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("relay required"))
		return
	}
	// AN EMPTY RELAY IS THE ORDINARY CASE, NOT A MISSING ARGUMENT. In
	// automatic mode Settings.Relay is empty BY DESIGN — the node measures
	// the real paths and keeps its choice in runtime state — so a caller
	// that reads settings and finds nothing has learned nothing about
	// whether this device can reach a relay.
	//
	// This is the same defect RR-tail removed from six other call sites, and
	// it survived here because the pass flow asks the CLIENT to supply the
	// address. It should never have: resolving a relay is the node's job,
	// and the resolver is one line away.
	relay := body.Relay
	if relay == "" {
		relay = a.rt.ResolvePersonalRelay()
	}
	if relay == "" {
		httpErr(w, http.StatusBadRequest, errors.New(
			"no relay is reachable right now — a pass needs somewhere for the "+
				"entry request to wait"))
		return
	}
	info, err := a.rt.MintPass(tid, body.MaxUses, body.TTLHours, relay)
	if err != nil {
		httpErr(w, http.StatusForbidden, err)
		return
	}
	resp := map[string]any{
		"pass_id":    info.PassID,
		"link":       info.Link,
		"expires_at": info.ExpiresAt,
		"max_uses":   info.MaxUses,
	}
	if png, err := qrcode.Encode(info.Link, qrcode.Medium, 512); err == nil {
		resp["qr_png_base64"] = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, resp)
}

func (a *APIServer) handleListPasses(w http.ResponseWriter, r *http.Request) {
	tid, err := a.spaceID(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	want := tid.Hex()
	out := []map[string]any{}
	for _, p := range a.rt.ListPasses() {
		if p.Space != want {
			continue
		}
		out = append(out, map[string]any{
			"pass_id": p.PassID, "expires_at": p.ExpiresAt,
			"max_uses": p.MaxUses, "used": p.Used, "revoked": p.Revoked,
		})
	}
	writeJSON(w, map[string]any{"passes": out})
}

// handleRevokePass blocks a pass for new and pending requests. It never
// removes members already accepted (ADR-012 invariant 6: revoke ≠ remove).
func (a *APIServer) handleRevokePass(w http.ResponseWriter, r *http.Request) {
	if err := a.rt.RevokePass(r.PathValue("pass")); err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]string{"status": "revoked"})
}

// handleJoinRequest starts an async join: it sends a sealed request to the
// pass's rendezvous and returns a request id to poll. No space is opened yet —
// pending has no access until the owner's device confirms.
func (a *APIServer) handleJoinRequest(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Pass string `json:"pass"`
	}](r)
	if err != nil || strings.TrimSpace(body.Pass) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("pass required"))
		return
	}
	reqID, err := a.rt.JoinByPass(strings.TrimSpace(body.Pass))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{
		"request_id": reqID,
		"status":     string(JoinWaiting),
	})
}

func (a *APIServer) handleJoinStatus(w http.ResponseWriter, r *http.Request) {
	state, space := a.rt.JoinStatus(r.PathValue("req"))
	resp := map[string]any{"status": string(state)}
	if space != "" {
		resp["space"] = space
	}
	writeJSON(w, resp)
}

func (a *APIServer) handleRelayPush(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Addr  string `json:"addr"`
		Space string `json:"space"`
	}](r)
	if err != nil || body.Addr == "" || body.Space == "" {
		httpErr(w, http.StatusBadRequest, errors.New("addr and space required"))
		return
	}
	tid, err := id.ParseTerminalID(body.Space)
	if err != nil {
		httpErr(w, http.StatusBadRequest, errors.New("bad space id"))
		return
	}
	pushed, deadline, err := a.rt.PushToRelay(body.Addr, tid)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	// Honest wording: the relay accepted the bundle; nobody received it.
	writeJSON(w, map[string]any{
		"events_pushed":     pushed,
		"relay_holds_until": deadline,
		"status":            "accepted_by_relay",
	})
}

func (a *APIServer) handleRelayPull(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Addr string `json:"addr"`
	}](r)
	if err != nil || body.Addr == "" {
		httpErr(w, http.StatusBadRequest, errors.New("addr required"))
		return
	}
	applied, err := a.rt.PullFromRelay(body.Addr)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]int{"events_applied": applied})
}

func (a *APIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Addr string `json:"addr"`
	}](r)
	if err != nil || body.Addr == "" {
		httpErr(w, http.StatusBadRequest, errors.New("addr required"))
		return
	}
	if err := a.rt.ConnectPeer(body.Addr); err != nil {
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
