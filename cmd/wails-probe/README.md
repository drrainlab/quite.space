# wails-probe — the permanent DS-0 compatibility canary

A minimal Wails v3 application that mounts the REAL `api.Handler()` (full
node runtime: scrypt unlock, log replay, all routes, embedded web UI) inside
the AssetServer and walks the shell-viability gates. It is a canary, not a
product: run it before every Wails version bump, when testing a fork, to
build minimal upstream reproductions, and in CI on macOS, Linux and Windows.

    cd cmd/wails-probe && go build -o /tmp/wails-probe . && /tmp/wails-probe

Verdicts print to stdout as `PROBE gate=<name> verdict=pass|FAIL detail=…`
and render in the window. Automatic gates run on load; buttoned gates (mic
permission, audible seek) need a human.

This is a NESTED Go module on purpose: Wails needs CGO, ADR-011 mandates
`CGO_ENABLED=0` for the main binary, and `./...` from the root never sees a
directory with its own `go.mod`. The Wails version is an EXACT pin — bump it
only deliberately, and re-run the checklist on every bump.

## Gate results

### macOS (darwin/arm64, macOS 26.0, Wails v3.0.0-alpha2.119, 2026-07-30)

| gate | verdict | detail |
|---|---|---|
| api (real handler, no TCP)   | pass | fetch `/api/status` through the AssetServer |
| range-206                    | pass | `http.ServeContent` → 206 + Content-Range |
| media-seekable               | pass | `<audio>` reports seekable 0..2s |
| multipart (FormData+Blob)    | **FAIL** | body arrives EMPTY — see finding №1 |
| multipart-workaround         | pass | `arrayBuffer()` + hand-framed multipart, byte-exact |
| recorder-types               | pass | `audio/webm;codecs=opus` AND `audio/mp4` |
| canvas-export                | pass | JPEG (webp unavailable on WebKit — expected) |
| dialog-css                   | pass | `<dialog>` + backdrop-filter |
| tray                         | pass | created; items must Show() windows EXPLICITLY (app.Show() does not unhide) |
| close-is-hide                | pass | hand-verified: close hides, tray restores, tray Quit exits |
| mic-record                   | pass | hand-verified: permission prompt shown, 15584 bytes captured |
| voice-in-app                 | pass | hand-verified: a voice message records inside the shell |
| uploads-in-app               | pass | hand-verified after the postMultipart fix: cover image, atmosphere audio, chat attachment |
| bundles                      | pending | DS-4 territory (local unsigned .app works via bundle-macos.sh) |

macOS hand-run, 2026-07-30: every gate above is green. One recorded
observation: MediaRecorder DEFAULTS to `audio/mp4` (AAC) despite claiming
webm/opus support — the app requests webm/opus explicitly (pickVoiceMIME)
and that path hand-verified working, so the claim is honest when asked.

### Linux (WebKitGTK) — safe gates green from the .deb; blob bodies crash (finding №2, remedied app-side). Windows (WebView2) — pending.

Headless container run of the arm64 .deb (Debian 12, xvfb, 2026-07-29):
api, range-206, body-raw-1k/64k/2m, **multipart-workaround** (the app's
actual upload path, byte-exact), canvas-export (JPEG), dialog-css and
tray all PASS; recorder-types fails only because the container has no
GStreamer codecs (retest on a real desktop); media-seekable needs the
same; then the hazard batch hits finding №2 and the run ends — by
design, after every safe verdict has been posted. The upstream issue
text with the minimal reproduction lives in
`upstream-issue-blob-body.md` beside this file.

Two DS-0 findings from the first Linux build (2026-07-29, via
`bundle-debian.sh`):

- This alpha's DEFAULT Linux path is **GTK4 + webkitgtk-6.0** — not the
  GTK3 + webkit2gtk-4.1 the original CI dependency list assumed. The
  complete GTK3 variant lives behind `-tags gtk3`.
- The GTK4 path requires **GTK >= 4.10** (`GtkFileDialog` in
  linux_cgo.h) — newer than Debian 12's 4.8. So: default build → Debian
  13 / Ubuntu 23.10+; `-tags gtk3` build → Debian 12+ / Ubuntu 22.04+.

`bundle-debian.sh` builds the gtk3 variant into
`dist/quiet-spaces-probe_<ver>_<arch>.deb` (Docker, `ARCH=amd64` default;
install with `sudo apt install ./<pkg>.deb`). CI (ubuntu-24.04, GTK 4.14)
builds the default GTK4 path, so both variants stay watched.

Expectation to check first on Windows runs: whether request bodies
survive the WebView2 scheme handler. Linux is answered — see finding №2.

## Finding №2 — BLOB-backed request bodies SIGSEGV the process (Linux)

The Linux twin of finding №1, isolated by a standalone bisection (a
40-line app posting one body kind at a time):

    plain-string body                      arrives intact
    raw Uint8Array body (2 MiB)            arrives intact
    Blob body (15 bytes)                   SIGSEGV
    FormData containing a Blob             SIGSEGV

The crash is `addr=0x28` inside `C.webkit_uri_scheme_request_get_http_body`
(already on the GTK main thread — this is on top of the #5631 fix), and
it reproduces identically on v3.0.0-alpha2.119 across gtk3/webkit2gtk-4.1
2.48 (Debian 12, amd64 + arm64) and default gtk4/webkitgtk-6.0 2.52.5
(Debian 13, arm64). So the platform split for the SAME poison is: macOS
delivers blob-backed bodies EMPTY, Linux dies. Upstream issue material —
matrix + minimal repro drafted; focused issue, then a generic fix PR per
the contribution policy.

**The app is NOT blocked**: the finding-№1 remedy already routes every
upload through `postMultipart` (arrayBuffer → hand-framed raw bytes),
and raw bytes are exactly what Linux handles fine. The probe crashed
only because its own G3/G3c gates deliberately exercise the broken path
— those now run LAST (hazard batch), after every safe verdict has been
posted, so a Linux run yields the full safe checklist and then records
the fatal gate as the run's end.

Container-lab caveats for whoever reruns this (real desktops unaffected):
WebKit's bubblewrap sandbox needs namespace privileges Docker denies by
default (plain container → bwrap/dbus-proxy SIGTRAP before any page
load; `--privileged` lets it run), and headless runs need xvfb + xauth +
dbus-x11.

## Finding №1 — blob-backed request bodies are dropped (macOS)

The deciding matrix, measured here:

    raw Uint8Array bodies (1k / 64k / 2 MiB)   arrive intact
    FormData of plain strings (512 KiB)        arrives intact
    FormData containing a 5-BYTE Blob          arrives EMPTY
    a 1 KiB Blob as the whole body             arrives EMPTY

So: not multipart, not size — any request body BACKED BY A BLOB is lost
between WKWebView's custom-scheme handler and our mux. This is the known
WKWebView limitation (`WKURLSchemeTask` does not deliver blob-backed
`HTTPBodyStream`s), now confirmed against our exact stack.

Consequence for DS-3: every upload in the app is `FormData.append('file',
file)` and a `File` IS a `Blob` — unchanged, uploads in the shell would
silently send nothing. The remedy is proven by the `multipart-workaround`
gate: read the file first (`await file.arrayBuffer()`), frame the multipart
body by hand, send raw bytes. Cost: the file is held in memory, bounded by
MaxAsset (64 MiB). The client's `uploadAssetFile` grows a shell path behind
a capability check when DS-3 lands.

Per the contribution policy: this reproduction is the basis for a focused
upstream issue (attach the gate matrix above); do not fork over it — the
workaround is entirely on our side.

## Notes for the shell (DS-3)

- Window pattern in this alpha: `app.Window.NewWithOptions(...)` — the
  package-level `application.NewWindow` produced a window that never
  navigated (silently: zero asset requests). Managers hang off `App`
  (`Window`, `SystemTray`, `Dialog`, `Event`, …).
- `Options.IOS` / `Options.Android` exist in this alpha — relevant to the
  AR track's shell conversation later.
- macOS link step warns about objects built for 13.3 vs a default target of
  11.0 — cosmetic here; DS-4 should set `MACOSX_DEPLOYMENT_TARGET`.
