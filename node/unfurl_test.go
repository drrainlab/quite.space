package node

// The preview's two laws, tested where they can actually fail:
// the fetch must never reach inward, and the card must arrive as text.

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// A local test server is exactly the shape this guard exists to refuse:
// an address inside the machine, reached by a URL somebody else typed.
// If this ever passes, the seam has become a port scanner with a token.
func TestUnfurlRefusesTheLocalNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>inside</title></head></html>`))
	}))
	defer srv.Close()

	rt := &Runtime{}
	if _, err := rt.Unfurl(context.Background(), srv.URL); err == nil {
		t.Fatal("unfurl reached a loopback address")
	} else if !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("refused, but for the wrong reason: %v", err)
	}
}

func TestPublicOnly(t *testing.T) {
	refused := []string{
		"127.0.0.1:80", "[::1]:443", "10.1.2.3:80", "192.168.1.1:80",
		"172.16.9.9:80", "169.254.169.254:80", "[fd00::1]:80",
		"0.0.0.0:80", "100.64.3.4:80", "198.18.0.1:80", "224.0.0.1:80",
	}
	for _, a := range refused {
		if err := publicOnly("tcp", a, syscall.RawConn(nil)); err == nil {
			t.Errorf("%s was allowed", a)
		}
	}
	for _, a := range []string{"93.184.216.34:443", "[2606:2800:220:1::]:443", "100.128.0.1:80"} {
		if err := publicOnly("tcp", a, syscall.RawConn(nil)); err != nil {
			t.Errorf("%s was refused: %v", a, err)
		}
	}
}

func TestYouTubeID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=7ZrcTh2-uvQ":        "7ZrcTh2-uvQ",
		"https://youtu.be/7ZrcTh2-uvQ?t=42":                  "7ZrcTh2-uvQ",
		"https://www.youtube.com/shorts/7ZrcTh2-uvQ":         "7ZrcTh2-uvQ",
		"https://m.youtube.com/watch?v=7ZrcTh2-uvQ&list=xyz": "7ZrcTh2-uvQ",
		"https://www.youtube.com/embed/7ZrcTh2-uvQ":          "7ZrcTh2-uvQ",
		"https://www.youtube.com/watch?v=../../etc/passwd":   "",
		"https://example.com/watch?v=7ZrcTh2-uvQ":            "",
		"https://www.youtube.com/":                           "",
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := youtubeID(u); got != want {
			t.Errorf("%s -> %q, want %q", raw, got, want)
		}
	}
	// An id is pasted into a player address later, so its alphabet is
	// checked rather than trusted.
	u, _ := url.Parse(`https://www.youtube.com/watch?v=a"onerror=alert(1)`)
	if got := youtubeID(u); got != "" {
		t.Errorf("an id outside the alphabet survived: %q", got)
	}
}

func TestScanHeadReadsWhatAPageDeclares(t *testing.T) {
	doc := []byte(`<!doctype html><html><head>
  <meta charset="utf-8">
  <title>A quiet page &amp; its name</title>
  <meta property="og:title" content="The Open Graph title">
  <meta name='twitter:image' content='https://cdn.example/pic.jpg'>
  <meta property="og:description" content="Two   lines
  of it.">
  <meta property=og:site_name content=Example>
</head><body><title>a decoy in the body</title></body></html>`)
	m := scanHead(doc)
	if got := pick(m, "og:title"); got != "The Open Graph title" {
		t.Errorf("og:title = %q", got)
	}
	if got := pick(m, "twitter:image"); got != "https://cdn.example/pic.jpg" {
		t.Errorf("single-quoted attribute not read: %q", got)
	}
	if got := pick(m, "og:site_name"); got != "Example" {
		t.Errorf("unquoted attribute not read: %q", got)
	}
	if got := clean(pick(m, "<title>"), 300); got != "A quiet page & its name" {
		t.Errorf("title = %q -- entities must resolve", got)
	}
	if got := clean(pick(m, "og:description"), 300); got != "Two lines of it." {
		t.Errorf("description = %q -- whitespace must collapse to one line", got)
	}
	// The body's decoy must not have overwritten the head's title.
	if strings.Contains(m["<title>"], "decoy") {
		t.Error("scanning ran past </head>")
	}
}

// A card is a stranger's text that will be RENDERED, so it must arrive as
// text: no control bytes, no newline to fake a second field, nothing over
// the block format's own bound, and never a rune cut in half.
func TestCleanIsAlwaysOneReadableLine(t *testing.T) {
	got := clean("a b\r\nc\td  e", 300)
	if got != "a b c d e" {
		t.Errorf("clean = %q", got)
	}
	if got := clean("null\x00byte\x07bell", 300); got != "nullbytebell" {
		t.Errorf("control bytes survived: %q", got)
	}
	if n := len(clean(strings.Repeat("я", 400), 10)); n > 10 {
		t.Errorf("clip left %d bytes", n)
	}
	if s := clean(strings.Repeat("я", 400), 9); len(s) > 9 || !strings.HasSuffix(s, "я") {
		t.Errorf("clip split a rune: %q", s)
	}
}

// Whatever a stranger's server sends, what travels is pixels this node
// encoded -- and small enough to ride in a message.
func TestThumbIsReEncodedAndBounded(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := range 1080 {
		for x := range 1920 {
			src.Set(x, y, color.RGBA{uint8(x % 251), uint8(y % 241), uint8((x ^ y) % 233), 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, src); err != nil {
		t.Fatal(err)
	}
	small := downscale(src, 480)
	if b := small.Bounds(); b.Dx() != 480 || b.Dy() != 270 {
		t.Fatalf("downscale gave %v -- aspect must survive", b)
	}
	if got := downscale(src, 4000).Bounds().Dx(); got != 1920 {
		t.Fatalf("downscale enlarged to %d -- it must only shrink", got)
	}
}

// A real address, by hand, when something looks wrong in the product:
//
//	QP_LIVE_UNFURL=https://youtu.be/... go test ./node/ -run LiveUnfurl -v
//
// Skipped by default and never in CI: a test that needs YouTube to be up
// is a test that reports the weather.
func TestLiveUnfurl(t *testing.T) {
	raw := os.Getenv("QP_LIVE_UNFURL")
	if raw == "" {
		t.Skip("set QP_LIVE_UNFURL to try one address for real")
	}
	c, err := (&Runtime{}).Unfurl(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("kind=%s provider=%s id=%s site=%q", c.Kind, c.Provider, c.VideoID, c.Site)
	t.Logf("title=%q", c.Title)
	t.Logf("descr=%q", c.Description)
	t.Logf("thumb=%s, %d bytes on the wire", c.ThumbMIME, len(c.ThumbB64)/4*3)
}
