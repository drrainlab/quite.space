package node

import (
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
