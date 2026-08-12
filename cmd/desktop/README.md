# Quite Space — the desktop application (DS-3)

One process holding one node, with a window in front of it. The node is the
same one `terminal ui` runs — same runtime, same API, same embedded interface
— mounted inside the Wails AssetServer instead of behind a loopback port.

    cd cmd/desktop && go run .            # a window, on your real data
    go run . --data /tmp/qs-bob           # a second node, to talk to yourself
    go run . --debug                      # web inspector + one line per request

`--debug` is the only way to see inside a WebView from outside it. It opens the
inspector (right-click → Inspect Element) and prints a `REQ` line per request
with the method, path, request Content-Type, body length and status. The most
useful reading is often the ABSENCE of a line: it separates *the page never
sent it* — a throw before `fetch`, a CSP refusal — from *it arrived empty*,
which is the shape of the WKWebView blob-body defect, from *it arrived and the
node refused it*, which the status names.

This is a **NESTED Go module**, and the nesting is the point: Wails needs CGO,
ADR-011 mandates `CGO_ENABLED=0` for the main binary, and Go excludes
directories with their own `go.mod` from `./...`. The root build, vet and CI
never see this directory. Bump the Wails pin deliberately, and re-run
`cmd/wails-probe`'s checklist on the same version before shipping from it.

## What the shell is, and what it is not

It hosts the local HTTP API and the interface. It does not replace them with
Go bindings — there is not one domain-shaped binding in this binary, and
`boundary_test.go` fails the build if a file in `package main` reaches for
`terminals`, `protocol`, `kernel`, `transports`, or Wails itself (which is
named in `internal/wailsx` and nowhere else — that is what an upgrade costs).

The window is not the node. Closing it hides it; the node keeps syncing. Only
the tray's **Quit**, or the OS, ends the process.

## The handler swap

The node has no locked state, so the shell serves a different handler
depending on where it is:

| state     | serves                                            |
|-----------|---------------------------------------------------|
| `locked`  | the lock gate; `503 {"error":"locked"}` on `/api/*` |
| `opening` | a page that waits and reloads                     |
| `open`    | `api.Handler()`, unwrapped                        |
| `failed`  | one sentence about why                            |

The window never learns about any of this. It is pointed at `/` once and the
pages navigate themselves: the gate hands out the session token in the reply
that let somebody in, its page goes to `/?token=…`, and the opening page
reloads that URL until the API answers.

`opening` is not decoration. The gate's page navigates about 700 ms after a
correct answer and `node.Open` — scrypt, then a replay of every space's log —
can take longer. Without it that arrival lands back on the gate, which by then
reports the directory as *in use* because we are the ones holding the lock,
and the app accuses itself of being open somewhere else while opening.

## Building the artifacts

Both are **EXPERIMENTAL and unsigned by any authority**. This beta has no
Apple Developer membership and no Debian repository, and the scripts say so
rather than papering over it.

    ARCH=universal VERSION=0.1.0 ./bundle-macos.sh   → dist/quite-space-macos-universal.dmg
    ARCH=amd64     VERSION=0.1.0 ./bundle-linux.sh   → dist/quite-space-linux-amd64.deb

**macOS.** The `.app` is signed **ad-hoc** (`codesign -s -`), which is free and
not optional: on Apple Silicon the kernel refuses to execute an arm64 binary
with no signature at all, so a truly unsigned build would not start. Ad-hoc
does not satisfy Gatekeeper — a `.dmg` that arrived through a browser carries
`com.apple.quarantine` and macOS will say it cannot verify the developer,
which is true. The remedy is deliberate and belongs on the download page:

1. open the `.dmg`, drag **Quite Space** to Applications
2. launch it once — macOS refuses
3. System Settings → Privacy & Security → **Open Anyway**

or `xattr -dr com.apple.quarantine "/Applications/Quite Space.app"`.

The bundle identifier `space.quite.desktop` is **frozen from the first
release**: macOS keys preferences, keychain items and the TCC permission
grants on it, and changing it later silently orphans all of them.

**Linux.** Built with `-tags gtk3` on purpose. This Wails alpha's default path
is GTK4 + webkitgtk-6.0 and needs GTK ≥ 4.10, newer than Debian 12 ships; the
GTK3 + webkit2gtk-4.1 variant reaches **Debian 12+ and Ubuntu 22.04+**. On a
Mac the script rents a Linux userland from Docker; on Linux it builds where it
stands. Install with `apt`, not `dpkg -i` — apt resolves the GTK and WebKit
runtime dependencies:

    sudo apt install ./quite-space-linux-amd64.deb

## Tests

    go test ./...

All of it is `httptest`: no window, no display, no Wails runtime. That split is
deliberate — an alpha framework is the part most likely to change under us and
the part least worth writing tests against.
