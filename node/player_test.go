package node

// The player window's whole security argument is what it REFUSES to be:
// not a way to run an address of somebody else's choosing on this machine,
// and not a hole in the interface's own frame policy.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPlayerPageFramesOnlyTheOneHost(t *testing.T) {
	a := &APIServer{}
	rec := httptest.NewRecorder()
	a.handlePlayer(rec, httptest.NewRequest("GET", "/player?v=7ZrcTh2-uvQ&t=90", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'",
		"frame-src https://www.youtube-nocookie.com",
		"script-src 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("the player's policy lost %q: %s", want, csp)
		}
	}
	// The nesting, stated as two assertions because it is the whole
	// design: the conversation may frame OUR page and nothing else, and
	// this page is the only one allowed to name the embed's host.
	//
	// Every source the interface may frame must be OURS: 'self', or the
	// loopback origin the node lends a shell that has no http identity of
	// its own. An external host here would put a third party's script
	// beside the session token, which is the one thing uiPolicy exists to
	// prevent -- so this asserts on the whole source list rather than on
	// the presence of one word.
	srcs := strings.Fields(afterFrameSrc(uiPolicy))
	if len(srcs) == 0 {
		t.Fatal("the interface has no frame-src at all")
	}
	if srcs[0] != "'self'" {
		t.Errorf("the interface's frame-src does not start at 'self': %q", srcs)
	}
	for _, src := range srcs[1:] {
		if !strings.HasPrefix(src, "http://127.0.0.1:") && !strings.HasPrefix(src, "http://localhost:") {
			t.Errorf("the interface's frame-src names %q -- only this node's own origins belong here", src)
		}
	}
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Error("the player refuses to be framed by the conversation")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://www.youtube-nocookie.com/embed/7ZrcTh2-uvQ?") {
		t.Errorf("no embed for the id: %s", body)
	}
	if !strings.Contains(body, "start=90") {
		t.Errorf("the timestamp did not survive: %s", body)
	}
	if strings.Contains(body, "token") {
		t.Error("the player carries a token")
	}
	// Permissions are delegated one level at a time; a set that stops here
	// leaves the embed silently unable to start.
	if !strings.Contains(body, `allow="autoplay;`) {
		t.Errorf("the embed was not granted autoplay: %s", body)
	}
}

// afterFrameSrc returns what the policy's frame-src directive says, so a
// host named in some OTHER directive (img-src, media-src) cannot make the
// assertion above fail for the wrong reason.
func afterFrameSrc(policy string) string {
	i := strings.Index(policy, "frame-src ")
	if i < 0 {
		return ""
	}
	rest := policy[i+len("frame-src "):]
	if j := strings.Index(rest, ";"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// An id is spliced into a URL and into the document, so anything outside
// the alphabet must never reach either.
func TestPlayerRefusesAnythingButAnID(t *testing.T) {
	a := &APIServer{}
	for _, bad := range []string{
		"", "../../etc/passwd", `a" onload="alert(1)`, "<script>",
		"https://evil.example/x", "abc", strings.Repeat("a", 40),
	} {
		rec := httptest.NewRecorder()
		a.handlePlayer(rec, httptest.NewRequest("GET", "/player?v="+url.QueryEscape(bad), nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q was accepted (%d)", bad, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "youtube-nocookie") {
			t.Errorf("%q reached an embed", bad)
		}
	}
}

// The loopback listener exists so a shell with no http origin can still
// lend the embed an identity. What matters is that it serves the PLAYER
// and nothing else — it has no token, so anything else behind it would be
// an unauthenticated door into the node.
func TestListenPlayerServesOnlyThePlayer(t *testing.T) {
	a := &APIServer{}
	origin, err := a.ListenPlayer()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(origin, "http://127.0.0.1:") {
		t.Fatalf("origin %q is not loopback http", origin)
	}
	if a.playerOrigin != origin {
		t.Errorf("status would report %q, not %q", a.playerOrigin, origin)
	}
	res, err := http.Get(origin + "/player?v=7ZrcTh2-uvQ")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "youtube-nocookie") {
		t.Fatalf("the player did not answer: %d %s", res.StatusCode, body)
	}
	// Every other route the node has must be absent here, including the
	// ones that would otherwise answer without a token.
	for _, path := range []string{"/", "/api/status", "/api/spaces", "/api/unfurl"} {
		r, err := http.Get(origin + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d on the player listener -- it must serve one route", path, r.StatusCode)
		}
	}
}
