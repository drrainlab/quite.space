# Linux: Blob-backed fetch() bodies SIGSEGV in webkit_uri_scheme_request_get_http_body (both GTK3 and GTK4 builds)

## Description

On Linux, a `fetch()` to the AssetServer whose body is **blob-backed** — a
`Blob` body, or a `FormData` containing a `Blob`/`File` — crashes the process
with SIGSEGV. Plain-string bodies and raw `Uint8Array`/`ArrayBuffer` bodies
(tested up to 2 MiB) round-trip fine. Bisection, one body kind per run:

| body | result |
|---|---|
| plain string | arrives intact |
| raw `Uint8Array`, 2 MiB | arrives intact |
| `Blob` (15 bytes) | **SIGSEGV** |
| `FormData` containing a `Blob` | **SIGSEGV** |

Crash header (identical across every run, only the pointer differs):

```
SIGSEGV: segmentation violation
PC=... m=0 sigcode=1 addr=0x28
signal arrived during cgo execution

goroutine 1 gp=... m=0 [syscall, locked to thread]:
runtime.cgocall(...)
github.com/wailsapp/wails/v3/internal/assetserver/webview._Cfunc_webkit_uri_scheme_request_get_http_body(0x...)
	_cgo_gotypes.go:438
github.com/wailsapp/wails/v3/internal/assetserver/webview.webkit_uri_scheme_request_get_http_body.func1.1(...)
	internal/assetserver/webview/webkit_linux_gtk3.go:111   (same via webkit_linux.go on the GTK4 build)
```

Note the call is already hopped to the GTK main thread via `invokeOnMainSync`
(the #5631 fix) — the fault is *inside* the WebKit C function, at a constant
near-null offset (`addr=0x28`), when the body stream is blob-backed.

This looks like the Linux twin of the known WKWebView limitation: on macOS
the same blob-backed bodies arrive **empty** (WKURLSchemeTask does not deliver
blob-backed HTTPBodyStreams); on WebKitGTK they crash instead.

## Reproduction matrix

Reproduced on **v3.0.0-alpha2.119** (latest published alpha at the time of
writing):

| build | WebKitGTK | distro | arch | result |
|---|---|---|---|---|
| `-tags gtk3` | webkit2gtk-4.1 (2.48) | Debian 12 | amd64 | SIGSEGV addr=0x28 |
| `-tags gtk3` | webkit2gtk-4.1 (2.48) | Debian 12 | arm64 | SIGSEGV addr=0x28 |
| default (GTK4) | webkitgtk-6.0 (2.52.5), GTK 4.18.6 | Debian 13 | arm64 | SIGSEGV addr=0x28 |

GETs are unaffected — a full SPA loads through the same handler, including
Range/206 media responses. Collected under X11 (xvfb and normal sessions
behave the same).

## Minimal reproduction (single file)

```go
// go.mod: require github.com/wailsapp/wails/v3 v3.0.0-alpha2.119
package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const page = `<!doctype html><html><body><script>
// A plain-string body works. Swap it for the Blob and the process dies
// before the request ever reaches the Go handler.
fetch('/echo', {method:'POST', body: new Blob(['blob body bytes'])})
  .then(r => r.text()).then(t => console.log('roundtrip ok:', t))
  .catch(e => console.log('fetch error:', e));
</script>POSTing a Blob body…</body></html>`

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, page)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Println("SERVER GOT BODY:", len(b), "bytes")
		w.Write(b)
	})
	app := application.New(application.Options{
		Name:   "blob-body-crash-repro",
		Assets: application.AssetOptions{Handler: mux},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "repro", URL: "/"})
	if err := app.Run(); err != nil {
		fmt.Println("run error:", err)
	}
}
```

Expected: the handler receives 15 bytes and echoes them back (or, failing
that, an empty body as on macOS — degraded but survivable).
Actual: SIGSEGV in `webkit_uri_scheme_request_get_http_body`;
`SERVER GOT BODY` never prints.

`body: 'plain string'` and `body: new Uint8Array(2*1024*1024)` in the same
app both round-trip correctly — the trigger is specifically a blob-backed
body, not bodies or body size in general.

## Impact / why it may have gone unnoticed

Apps that drive logic through bindings rarely POST to the AssetServer. Any
app that mounts an HTTP API as the AssetServer handler hits this the first
time a user uploads a file (`FormData.append('file', file)` — a `File` is a
`Blob`). Our workaround (reading the file via `arrayBuffer()` and hand-framing
the multipart body as raw bytes) avoids the crash entirely, so a fix that
merely delivers blob bodies *empty* — matching macOS — would already make the
failure survivable; delivering the bytes would of course be better on both
platforms.

## Environment

- Wails: v3.0.0-alpha2.119 (exact pin)
- Go: 1.25.6
- Debian 12 (bookworm) / webkit2gtk-4.1 2.48 (`-tags gtk3`, amd64 + arm64)
  and Debian 13 (trixie) / webkitgtk-6.0 2.52.5, GTK 4.18.6 (default build,
  arm64)
- X11

Happy to test patches — this reproduction lives in a permanent compatibility
canary we re-run against every version bump.
