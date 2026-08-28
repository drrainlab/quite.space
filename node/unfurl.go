package node

// LINK PREVIEWS — the card is made by the person who SENDS it.
//
// This is the one rule the feature exists to keep, and it is not a
// preference. Every other messenger answers "who fetches the preview?"
// with either a central unfurl service (there is none here, and there will
// not be one) or the READER's client — and the reader's client is the
// wrong answer in a way that is easy to miss:
//
//	an <img> pointing at a stranger's host is a beacon. Opening a room
//	would tell whoever wrote the link that this device, at this address,
//	at this minute, read it. Send somebody a link to a server you own and
//	you have a read receipt they never agreed to, plus their IP.
//
// So the protocol already said it (`block.link.v1`: "the preview is
// attached by the SENDER"), markdown already said it (no `![](url)`, for
// the same reason, in markdown.js), and this file is where it becomes
// true: the sender's own node fetches the page ONCE, at compose time,
// keeps a small re-encoded thumbnail, and the card travels through the log
// as ordinary sealed content. A reader renders it from bytes that are
// already theirs and touches nothing.
//
// The URL, by contrast, is arbitrary text — often typed by somebody else
// and pasted here. That makes the fetch itself a request-forgery surface,
// and unlike node/llm's provider URL (which a person configured, and which
// legitimately points at localhost), there is no honest reason for this
// one to reach a private address. It is refused at the SOCKET, on the
// resolved IP, so a name that resolves inward — or a redirect that turns
// inward on the third hop — dies at the same check as a literal.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/drrainlab/quiet_places/protocol/schemas"
)

// Unfurl bounds. Small on purpose: this is a card, not a mirror.
const (
	unfurlTimeout   = 8 * time.Second
	unfurlMaxHTML   = 256 << 10 // enough for any <head> worth reading
	unfurlMaxImage  = 4 << 20   // a source thumbnail before re-encoding
	unfurlMaxRedirs = 3
	// unfurlThumbBudget is what may ride in the event. The block format
	// permits 40 KiB and the UI aims at 8; a card is re-encoded down until
	// it fits this, and if it will not, it travels without a picture. A
	// preview is never worth making a message too heavy to deliver.
	unfurlThumbBudget = 16 << 10
	// unfurlUA is what this node calls itself. Naming the app rather than
	// impersonating a browser is the honest choice AND the useful one:
	// a host that dislikes it can say so.
	unfurlUA = "QuietSpaces/0.1 (+link preview; fetched once by the sender)"
)

// LinkCard is what the sender saw when they pasted the address. Every
// field is the REMOTE SITE's claim about itself, taken at one moment by
// one device — exactly the standing of a quotation (SHARE-1), and the
// renderer gives it a stranger's voice rather than the product's.
type LinkCard struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Site is who the page says it is, or failing that its host.
	Site string `json:"site,omitempty"`
	// Kind is "video" when the address is a playable thing, else "page".
	// It decides whether the card wears a play control, nothing more.
	Kind string `json:"kind"`
	// Provider and VideoID are set only for addresses this node knows how
	// to recognise (YouTube today). They exist so a player can be opened
	// without re-parsing the URL in three languages.
	Provider  string `json:"provider,omitempty"`
	VideoID   string `json:"video_id,omitempty"`
	ThumbMIME string `json:"thumb_mime,omitempty"`
	ThumbB64  string `json:"thumb_b64,omitempty"`
}

// ---- the guarded fetch ----

// publicOnly refuses a connection to any address that is not somewhere on
// the public internet.
//
// It runs as the dialer's Control hook, which means it sees the address
// the socket is ACTUALLY about to connect to, after resolution. That is
// what makes it a check and not a gesture: node/llm's comment is right
// that resolving a name in a validator is TOCTOU theatre, because the dial
// resolves again. Here there is no second resolution to disagree with.
func publicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("unfurl: unroutable address")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("unfurl: unresolved address")
	}
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsUnspecified(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return fmt.Errorf("unfurl: %s is not a public address", host)
	}
	// 100.64/10 (carrier NAT) and 198.18/15 (benchmarking) are neither
	// private nor public in the stdlib's sense, and both sit in front of
	// things on somebody's own network.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1]&0xC0 == 64 {
			return fmt.Errorf("unfurl: %s is a carrier-local address", host)
		}
		if v4[0] == 198 && v4[1]&0xFE == 18 {
			return fmt.Errorf("unfurl: %s is a benchmarking address", host)
		}
	}
	return nil
}

func unfurlClient() *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: publicOnly}
	return &http.Client{
		Timeout: unfurlTimeout,
		// No cookie jar, on purpose and permanently: a preview must never
		// become a way to carry this person's session to a third party.
		Jar: nil,
		Transport: &http.Transport{
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			DisableKeepAlives:     true,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= unfurlMaxRedirs {
				return errors.New("unfurl: too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("unfurl: redirect to %q refused", req.URL.Scheme)
			}
			return nil
		},
	}
}

// get reads at most max bytes from one address, and reports the content
// type it actually got.
func unfurlGet(ctx context.Context, raw, accept string, max int64) ([]byte, string, error) {
	if err := schemas.ValidateHTTPURL(raw); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", unfurlUA)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en;q=0.8, *;q=0.5")
	resp, err := unfurlClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("unfurl: the site answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max))
	if err != nil && len(body) == 0 {
		return nil, "", err
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// ---- the card ----

// Unfurl asks one address what it is, and comes back with what will
// travel. Failure is ordinary: a site that refuses, times out or says
// nothing about itself yields an error, and the caller sends the plain
// address — which was always readable on its own.
func (r *Runtime) Unfurl(ctx context.Context, raw string) (*LinkCard, error) {
	raw = strings.TrimSpace(raw)
	if err := schemas.ValidateHTTPURL(raw); err != nil {
		return nil, err
	}
	// THE GATE RUNS BEFORE THE SOCKET OPENS. This call predates
	// internetGate() by several waves and used to dial out regardless of
	// the connectivity policy: a device set to radio-only — or to offline
	// entirely — still reached a stranger's web server the moment somebody
	// pasted a link. That is the exact shape the policy exists to refuse,
	// and the refusal is typed (ErrTransportBlocked), so a client can say
	// "you told me not to" rather than showing a preview that failed.
	if err := r.internetGate(); err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("unfurl: that address does not parse")
	}
	ctx, cancel := context.WithTimeout(ctx, unfurlTimeout*2)
	defer cancel()

	card := &LinkCard{URL: raw, Kind: "page", Site: strings.TrimPrefix(u.Hostname(), "www.")}
	thumbURL := ""

	if vid := youtubeID(u); vid != "" {
		// YouTube by its own oEmbed rather than by scraping the watch
		// page: a few hundred bytes of declared answer instead of a
		// megabyte of markup that only means something once script has
		// run. It also needs no key and no account.
		card.Kind, card.Provider, card.VideoID = "video", "youtube", vid
		card.Site = "YouTube"
		if o, err := youtubeOEmbed(ctx, vid); err == nil {
			card.Title = o.Title
			if o.Author != "" {
				card.Description = o.Author
			}
			thumbURL = o.Thumb
		}
		if thumbURL == "" {
			thumbURL = "https://i.ytimg.com/vi/" + vid + "/hqdefault.jpg"
		}
	} else {
		body, ctype, err := unfurlGet(ctx, raw, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1", unfurlMaxHTML)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(ctype), "html") {
			return nil, errors.New("unfurl: that address is not a page")
		}
		m := scanHead(body)
		card.Title = pick(m, "og:title", "twitter:title", "<title>")
		card.Description = pick(m, "og:description", "twitter:description", "description")
		if s := pick(m, "og:site_name"); s != "" {
			card.Site = s
		}
		if k := pick(m, "og:type"); strings.HasPrefix(k, "video") {
			card.Kind = "video"
		}
		thumbURL = pick(m, "og:image:secure_url", "og:image", "twitter:image")
		if thumbURL != "" {
			if abs, e := u.Parse(thumbURL); e == nil {
				thumbURL = abs.String()
			}
		}
	}

	card.Title = clean(card.Title, schemas.MaxTitleLen)
	card.Description = clean(card.Description, 300)
	card.Site = clean(card.Site, 120)
	if card.Title == "" && card.Description == "" && thumbURL == "" {
		return nil, errors.New("unfurl: that page says nothing about itself")
	}
	if thumbURL != "" {
		if jpg, err := fetchThumb(ctx, thumbURL); err == nil {
			card.ThumbMIME = "image/jpeg"
			card.ThumbB64 = base64.StdEncoding.EncodeToString(jpg)
		}
		// A thumbnail that will not come, will not decode or will not fit
		// is not an error: the card is the title and the address, and the
		// picture was always the part that could be missing.
	}
	return card, nil
}

// ---- YouTube ----

// youtubeID recognises the addresses people actually paste. Anything it
// does not recognise falls through to the ordinary page path, which is
// the right failure: an unknown YouTube URL shape becomes a normal card,
// never a wrong one.
func youtubeID(u *url.URL) string {
	h := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	path := strings.Trim(u.Path, "/")
	switch h {
	case "youtu.be":
		return validVideoID(firstSegment(path))
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		if path == "watch" {
			return validVideoID(u.Query().Get("v"))
		}
		for _, p := range []string{"shorts/", "embed/", "live/", "v/"} {
			if strings.HasPrefix(path, p) {
				return validVideoID(firstSegment(strings.TrimPrefix(path, p)))
			}
		}
	}
	return ""
}

func firstSegment(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// validVideoID keeps the id to the alphabet YouTube uses, so it can be
// pasted into a player URL later without becoming an injection.
func validVideoID(s string) string {
	if len(s) < 6 || len(s) > 24 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return ""
		}
	}
	return s
}

type oEmbed struct {
	Title  string `json:"title"`
	Author string `json:"author_name"`
	Thumb  string `json:"thumbnail_url"`
}

func youtubeOEmbed(ctx context.Context, vid string) (*oEmbed, error) {
	q := url.Values{
		"url":    {"https://www.youtube.com/watch?v=" + vid},
		"format": {"json"},
	}
	body, _, err := unfurlGet(ctx, "https://www.youtube.com/oembed?"+q.Encode(), "application/json", 64<<10)
	if err != nil {
		return nil, err
	}
	var o oEmbed
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ---- the thumbnail ----

// fetchThumb takes a picture off a stranger's server and turns it into
// something safe to put in a signed event: decoded, resized, and RE-ENCODED
// as JPEG. Re-encoding is the load-bearing step — what travels is pixels
// this node produced, not a file it was handed, so nothing rides along
// inside it and the size is a number this node chose.
func fetchThumb(ctx context.Context, raw string) ([]byte, error) {
	body, _, err := unfurlGet(ctx, raw, "image/jpeg,image/png;q=0.9,*/*;q=0.1", unfurlMaxImage)
	if err != nil {
		return nil, err
	}
	// Only the two formats the standard library decodes. WebP and AVIF
	// would each be a dependency, and a card without a picture is a
	// perfectly good card.
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for _, w := range []int{480, 360, 280} {
		small := downscale(src, w)
		for _, q := range []int{74, 60, 46} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: q}); err != nil {
				return nil, err
			}
			if buf.Len() <= unfurlThumbBudget {
				return buf.Bytes(), nil
			}
		}
	}
	return nil, errors.New("unfurl: preview will not fit in a message")
}

// downscale is a box filter: for each destination pixel, the average of
// the source pixels it covers. Enough for a card, and it needs nothing
// outside the standard library.
func downscale(src image.Image, maxW int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return src
	}
	if sw <= maxW {
		maxW = sw
	}
	dw := maxW
	dh := sh * dw / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var rs, gs, bs, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, bl, _ := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					rs += uint64(r >> 8)
					gs += uint64(g >> 8)
					bs += uint64(bl >> 8)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(rs / n)
			dst.Pix[i+1] = uint8(gs / n)
			dst.Pix[i+2] = uint8(bs / n)
			dst.Pix[i+3] = 0xFF
		}
	}
	return dst
}

// ---- reading a page's head ----

// scanHead collects <meta> declarations and <title> from the beginning of
// a document. It is a scanner, not a parser: it never builds a tree, never
// backtracks, and every loop is bounded by the input it was handed — which
// is itself capped before it gets here. Whatever it finds is TEXT that
// will be clipped and shown; nothing it returns is ever executed.
func scanHead(b []byte) map[string]string {
	out := map[string]string{}
	lower := bytes.ToLower(b)
	// Stop at </head> when there is one: everything below it is the
	// document body, which has nothing to declare and plenty to scan.
	if i := bytes.Index(lower, []byte("</head")); i > 0 {
		b, lower = b[:i], lower[:i]
	}
	for i := 0; i < len(lower); {
		j := bytes.IndexByte(lower[i:], '<')
		if j < 0 {
			break
		}
		i += j
		switch {
		case bytes.HasPrefix(lower[i:], []byte("<meta")):
			end := bytes.IndexByte(lower[i:], '>')
			if end < 0 {
				return out
			}
			a := attrs(b[i+5 : i+end])
			key := a["property"]
			if key == "" {
				key = a["name"]
			}
			if key != "" && a["content"] != "" {
				if _, dup := out[strings.ToLower(key)]; !dup {
					out[strings.ToLower(key)] = a["content"]
				}
			}
			i += end + 1
		case bytes.HasPrefix(lower[i:], []byte("<title")):
			open := bytes.IndexByte(lower[i:], '>')
			if open < 0 {
				return out
			}
			start := i + open + 1
			shut := bytes.Index(lower[start:], []byte("</title"))
			if shut < 0 {
				return out
			}
			if _, dup := out["<title>"]; !dup {
				out["<title>"] = string(b[start : start+shut])
			}
			i = start + shut
		default:
			i++
		}
	}
	return out
}

// attrs reads name="value" pairs out of one tag's interior.
func attrs(tag []byte) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(tag) {
		for i < len(tag) && isSpaceByte(tag[i]) {
			i++
		}
		start := i
		for i < len(tag) && !isSpaceByte(tag[i]) && tag[i] != '=' {
			i++
		}
		if i == start {
			i++
			continue
		}
		name := strings.ToLower(string(tag[start:i]))
		for i < len(tag) && isSpaceByte(tag[i]) {
			i++
		}
		if i >= len(tag) || tag[i] != '=' {
			out[name] = ""
			continue
		}
		i++
		for i < len(tag) && isSpaceByte(tag[i]) {
			i++
		}
		if i >= len(tag) {
			break
		}
		var val string
		if q := tag[i]; q == '"' || q == '\'' {
			i++
			s := i
			for i < len(tag) && tag[i] != q {
				i++
			}
			val = string(tag[s:i])
			i++
		} else {
			s := i
			for i < len(tag) && !isSpaceByte(tag[i]) {
				i++
			}
			val = string(tag[s:i])
		}
		out[name] = val
	}
	return out
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func pick(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}

// clean turns whatever a page declared into one line of readable text:
// entities resolved, whitespace collapsed, invalid encoding dropped, and
// clipped to a length the block format accepts.
var (
	// A markup fragment inside a page's self-description is noise, not
	// meaning: og:description is supposed to be text, and sites that put
	// "<a href=…>" in it clipped it themselves. The second pattern is a
	// tag the SITE truncated mid-way ("… in this ad <a"), which would
	// otherwise survive as a dangling bracket.
	tagRe     = regexp.MustCompile(`<[^<>]{0,200}>`)
	openTagRe = regexp.MustCompile(`<[^<>]{0,200}$`)
)

func clean(s string, max int) string {
	// Twice, because the wild double-escapes: a page that writes
	// "&amp;nbsp;" in its own description meant a space, and one pass
	// leaves "&nbsp;" on screen as seven characters of noise. A site that
	// genuinely meant to DISPLAY "&amp;" loses it — a smaller lie than
	// the entity soup real pages produce.
	s = html.UnescapeString(html.UnescapeString(s))
	s = tagRe.ReplaceAllString(s, " ")
	s = openTagRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7F {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if len(s) > max {
		s = s[:max]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

// ---- the seam ----

// handleUnfurl is deliberately a verb the PERSON invokes, not something
// the node does on its own behalf. It goes out to the network, so the
// interface asks for it once, when somebody has pasted an address and is
// about to send it — and it shows the answer before the send, because
// what a card says about a stranger's page is worth looking at before it
// becomes part of a signed message.
func (a *APIServer) handleUnfurl(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		URL string `json:"url"`
	}](r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	card, err := a.rt.Unfurl(r.Context(), body.URL)
	if err != nil {
		// A site that will not answer is not a failure of this node, and
		// the sentence says which of the two it was.
		httpErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, card)
}
