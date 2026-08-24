package node

// The player window's whole security argument is what it REFUSES to be:
// not a way to run an address of somebody else's choosing on this machine,
// and not a hole in the interface's own frame policy.

import (
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
	// The app's own policy must be untouched by this route existing.
	if !strings.Contains(uiPolicy, "frame-src 'none'") {
		t.Error("the interface's frame-src was relaxed -- the player window exists so it never has to be")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://www.youtube-nocookie.com/embed/7ZrcTh2-uvQ?") {
		t.Errorf("no embed for the id: %s", body)
	}
	if !strings.Contains(body, "start=90") {
		t.Errorf("the timestamp did not survive: %s", body)
	}
	if strings.Contains(body, "token") {
		t.Error("the player window carries a token")
	}
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

// The open seam takes an id, never an address: the widest thing a caller
// can ask the host to open is this node's own player.
func TestOpenPlayerBuildsItsOwnAddress(t *testing.T) {
	opened := ""
	old := hostOpener
	hostOpener = func(target string) { opened = target }
	defer func() { hostOpener = old }()

	a := &APIServer{}
	post := func(host, body string) *httptest.ResponseRecorder {
		opened = ""
		r := httptest.NewRequest("POST", "/api/player/open", strings.NewReader(body))
		r.Host = host
		rec := httptest.NewRecorder()
		a.handleOpenPlayer(rec, r)
		return rec
	}

	if rec := post("127.0.0.1:8899", `{"v":"7ZrcTh2-uvQ","t":"90"}`); rec.Code != 200 {
		t.Fatalf("a good call was refused: %d %s", rec.Code, rec.Body)
	}
	if opened != "http://127.0.0.1:8899/player?v=7ZrcTh2-uvQ&t=90" {
		t.Fatalf("opened %q", opened)
	}

	// Not an id, not an address, not somewhere else's host.
	for _, c := range []struct{ host, body string }{
		{"127.0.0.1:8899", `{"v":"https://evil.example/"}`},
		{"127.0.0.1:8899", `{"v":"../../"}`},
		{"evil.example", `{"v":"7ZrcTh2-uvQ"}`},
	} {
		if rec := post(c.host, c.body); rec.Code == 200 {
			t.Errorf("host=%s body=%s was accepted", c.host, c.body)
		}
		if opened != "" {
			t.Errorf("host=%s body=%s opened %q", c.host, c.body, opened)
		}
	}

	// A timestamp that is not a number is dropped, never spliced.
	if rec := post("localhost:8899", `{"v":"7ZrcTh2-uvQ","t":"90&x=1"}`); rec.Code != 200 {
		t.Fatalf("refused: %d", rec.Code)
	}
	if strings.Contains(opened, "x=1") {
		t.Errorf("a query rode in on the timestamp: %q", opened)
	}
}
