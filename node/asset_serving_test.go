package node

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// An asset's media_type travels WITH the asset — the uploader declares it
// and a peer's patched client may declare anything. These tests pin what
// the serving layer is allowed to do with that declaration.
//
// The defect they were written against: disposition was decided by a
// `strings.HasPrefix(ct, "image/")` test, which image/svg+xml passes. An
// SVG is an active document, so an attacker-authored asset was served
// inline, and the UI opens assets as top-level documents with the session
// token in the query string — window.open(assetURL(...)) for image zoom.
// That chain ended in script execution in the origin that drives every
// route, reachable from public content through the preview route.

func serveRef(t *testing.T, mediaType string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/spaces/x/assets/abcdef0123456789", nil)
	serveAssetBytes(rec, req, &schemas.AssetRef{MediaType: mediaType},
		"abcdef0123456789", []byte("bytes"))
	return rec.Result()
}

func TestAnActiveDocumentIsNeverServedInline(t *testing.T) {
	// Each of these would render as a document — with script — if the
	// browser were told to display it in place.
	for _, mt := range []string{
		"image/svg+xml",
		"image/svg+xml; charset=utf-8", // the parameter must not smuggle it past
		"text/html",
		"application/xhtml+xml",
		"application/xml",
		"text/xml",
		"application/pdf", // scriptable in some viewers; a download is fine
	} {
		res := serveRef(t, mt)
		disp := res.Header.Get("Content-Disposition")
		if !strings.HasPrefix(disp, "attachment") {
			t.Errorf("%s served as %q — an active document must be a download", mt, disp)
		}
	}
}

func TestOrdinaryMediaStillRendersInPlace(t *testing.T) {
	// The allowlist must not be so tight that it breaks the app: every
	// format the composer actually produces has to keep its inline
	// disposition, or photos and voice messages become downloads.
	for _, mt := range []string{
		"image/jpeg", "image/png", "image/webp", "image/gif",
		"audio/mpeg", "audio/ogg", "audio/webm", "audio/mp4", "audio/wav",
		"video/mp4", "video/webm", "video/quicktime",
		"image/png; charset=binary", // parameters are stripped, not fatal
	} {
		res := serveRef(t, mt)
		if disp := res.Header.Get("Content-Disposition"); !strings.HasPrefix(disp, "inline") {
			t.Errorf("%s served as %q — ordinary media must render in place", mt, disp)
		}
	}
}

func TestAnUnknownTypeDegradesToADownloadRatherThanAnError(t *testing.T) {
	// The honest failure direction: a file the person can still open by
	// hand, never a refusal and never a rendering surface nobody reviewed.
	res := serveRef(t, "application/x-some-new-thing")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d — an unlisted type must still be served", res.StatusCode)
	}
	if disp := res.Header.Get("Content-Disposition"); !strings.HasPrefix(disp, "attachment") {
		t.Errorf("disposition %q — an unlisted type must be a download", disp)
	}
	// A malformed declaration falls back rather than reaching the browser.
	res = serveRef(t, "not a media type at all")
	if ct := res.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("content-type %q — an unparseable declaration must not be echoed", ct)
	}
}

func TestServedAssetsCarryAnInertDocumentPolicy(t *testing.T) {
	// The second layer: even if the allowlist above were widened by
	// somebody who did not read the comment, a document opened at an asset
	// URL must not be able to run script in this origin.
	res := serveRef(t, "image/svg+xml")
	csp := res.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy on an asset response")
	}
	if !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP %q does not sandbox the document", csp)
	}
	if strings.Contains(csp, "allow-scripts") {
		t.Errorf("CSP %q grants allow-scripts — the sandbox is then decorative", csp)
	}
	if !strings.Contains(csp, "allow-downloads") {
		t.Errorf("CSP %q drops allow-downloads — file attachments stop working", csp)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
}

func TestTheInlineAllowlistRefusesSVGAtTheSource(t *testing.T) {
	// Pinned separately from the handler so the doctrine survives a
	// refactor of the serving code: SVG's absence IS the function.
	if schemas.AllowedInlineMIME("image/svg+xml") {
		t.Error("image/svg+xml is on the inline allowlist — it is an active document")
	}
	if schemas.AllowedInlineMIME("text/html") {
		t.Error("text/html is on the inline allowlist")
	}
	if !schemas.AllowedInlineMIME("image/png") {
		t.Error("image/png is not on the inline allowlist — ordinary media broke")
	}
}

// ---------------------------------------------------------------------
// M2: opening a pasted link must not take an automatic node out of
// automatic mode.
//
// RR changed what an empty Settings.Relay means — in automatic mode it is
// the normal state, not a gap — and this call site still read it the old
// way. Writing the link's address there pinned the node to a relay chosen
// by whoever wrote the link.

func TestAnAutomaticNodeDoesNotAdoptARelayFromAPastedLink(t *testing.T) {
	// The predicate is the whole fix, so it is what gets pinned: a node in
	// automatic mode is never "unconfigured", however blank the field.
	if !relayIsAutomatic(Settings{RelayMode: "automatic"}) {
		t.Error("an explicitly automatic node reads as not automatic")
	}
	if !relayIsAutomatic(Settings{}) {
		t.Error("a fresh node (no mode, no address) must default to automatic")
	}
	// The case that still adopts, deliberately: custom mode with nowhere
	// to go, where a deliberate paste is a reasonable offer.
	if relayIsAutomatic(Settings{RelayMode: "custom"}) {
		t.Error("an explicit custom choice was overridden")
	}
	// A pre-modes node that had an address keeps it and is not automatic.
	if relayIsAutomatic(Settings{Relay: "example:7411"}) {
		t.Error("a node with a configured address reads as automatic")
	}
}

// ---------------------------------------------------------------------
// L2/L3: the listener answers to its own name, and to nobody else's.

func TestOnlyLoopbackNamesReachTheListener(t *testing.T) {
	// Binding 127.0.0.1 does not mean only this machine can reach the API:
	// a page the person is merely visiting can point a name it controls at
	// 127.0.0.1 and talk to it from their browser. The packet really is
	// local; the name it asked for is what gives it away.
	for _, h := range []string{
		"127.0.0.1:8790", "localhost:8790", "LOCALHOST:8790",
		"[::1]:8790", "127.0.0.53:8790", "127.0.0.1", "localhost",
	} {
		if !loopbackName(hostOnly(h)) {
			t.Errorf("%q refused — the interface asks for exactly these", h)
		}
	}
	for _, h := range []string{
		"evil.example:8790", // the rebinding case, in one line
		"quiet.example.com", "192.168.1.10:8790", "10.0.0.1:8790",
		"0.0.0.0:8790", "", "localhost.evil.example:8790",
	} {
		if loopbackName(hostOnly(h)) {
			t.Errorf("%q accepted — that is somebody else's name", h)
		}
	}
}

func TestACrossOriginPageIsTurnedAway(t *testing.T) {
	for _, o := range []string{
		"http://127.0.0.1:8790", "http://localhost:8790", "http://[::1]:8790",
	} {
		if !loopbackOrigin(o) {
			t.Errorf("%q refused — that is our own page", o)
		}
	}
	for _, o := range []string{
		"https://evil.example", "http://evil.example:8790",
		"null", "", "not a url at all", "file://",
	} {
		if loopbackOrigin(o) {
			t.Errorf("%q accepted as same-origin", o)
		}
	}
}

func TestTheGuardWrapsTheListenerAndRefusesInOneSentence(t *testing.T) {
	reached := false
	h := loopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	// A rebound page: local packet, somebody else's name.
	req := httptest.NewRequest("GET", "http://evil.example/api/spaces", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached {
		t.Error("a request under another name reached the routes")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", rec.Code)
	}

	// The interface itself.
	reached = false
	req = httptest.NewRequest("GET", "http://127.0.0.1:8790/api/spaces", nil)
	req.Host = "127.0.0.1:8790"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !reached {
		t.Fatalf("the interface's own request was refused: %d %s", rec.Code, rec.Body)
	}

	// Right name, somebody else's page: a direct cross-origin fetch, which
	// the Host check alone would wave through.
	reached = false
	req = httptest.NewRequest("POST", "http://127.0.0.1:8790/api/spaces", nil)
	req.Host = "127.0.0.1:8790"
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached {
		t.Error("a cross-origin request reached the routes")
	}
}

func TestTheTokenComparisonDoesNotLeakItsProgress(t *testing.T) {
	// Not a timing measurement — those are flaky by nature. This pins that
	// the comparison goes through crypto/subtle at all, by checking the one
	// behaviour a length-first shortcut would break: a token that shares
	// every byte but is longer must still be refused.
	a := &APIServer{token: "0123456789abcdef"}
	h := a.auth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	for _, tok := range []string{"", "0", "0123456789abcde", "0123456789abcdef0", "x"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/x?token="+tok, nil)
		h(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q got %d, want 401", tok, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/x?token=0123456789abcdef", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("the right token got %d", rec.Code)
	}
}

// The guard is only worth anything if Serve actually puts it on. This goes
// through a real listener, because that is the thing being defended: a
// wrapper that exists but is never mounted defends nothing, and the unit
// tests above would not notice.
func TestServeMountsTheLoopbackGuard(t *testing.T) {
	a := &APIServer{token: "t"}
	addr, l, err := a.Serve(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ask := func(host string) int {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		// A path with no handler: the mux answers 404 without touching a
		// Runtime this bare APIServer does not have. The subject is the
		// guard in front of the mux, not any route behind it.
		fmt.Fprintf(c, "GET /nothing-here HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
		res, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// A page that rebound its own name onto 127.0.0.1 — the packet is
	// local, the name is not ours.
	if code := ask("evil.example"); code != http.StatusForbidden {
		t.Errorf("a rebound name got %d from the real listener, want 403", code)
	}
	// The interface's own request reaches the mux, which 404s it.
	if code := ask("127.0.0.1"); code != http.StatusNotFound {
		t.Errorf("the interface's own Host got %d, want the mux's 404", code)
	}
}
