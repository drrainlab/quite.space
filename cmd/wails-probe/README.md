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
| tray                         | pass | system tray + menu created |
| close-is-hide                | manual | hook installed; verify by closing the window |
| mic-record                   | manual | button (permission prompt) |
| bundles                      | pending | DS-4 territory |

### Linux (WebKitGTK) — pending. Windows (WebView2) — pending.

CI runs both (`.github/workflows/wails-probe.yml`): the BUILD is the hard
gate on all three platforms; the RUN is best-effort under an unattended
session (xvfb on Linux) with the `PROBE` log uploaded as an artifact.

Expectation to check first on both: whether blob-backed bodies survive their
scheme handlers (finding №1 may be WebKit-specific).

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
