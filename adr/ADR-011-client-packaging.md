# ADR-011: Client packaging and distribution

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §21 (Desktop), §22 (cmd/), vision §3.1; ADR-007

## Context

The client of a decentralized network cannot be a thin frontend to someone
else's server: the Go kernel must run on the user's machine, because the
client *is* the node. That raises the packaging question: how do a Go kernel
and a web UI ship as one installable thing, on desktops and on a Raspberry
Pi, without giving up "install the client — become a node"?

## Decision

### One Go binary, UI embedded

- The desktop client is a **single Go binary**: the kernel with the web UI's
  built static assets embedded via `go:embed`.
- At `terminal ui`, the kernel serves the assets and its **local typed API**
  (HTTP + WebSocket) on `127.0.0.1` with an ephemeral port and a per-session
  auth token, and opens the system browser or webview. The UI contains no
  protocol logic (plan §21) and talks only to the local API.
- The same binary runs headless: `terminal node` on a Raspberry Pi is the
  identical artifact minus the UI flag. CLI, desktop, and headless nodes
  cannot drift apart.

### Build constraints that protect this

- **`CGO_ENABLED=0` is mandatory** for the main binary: the whole platform
  matrix (darwin/linux/windows, amd64/arm64) cross-compiles from any machine.
- Therefore SQLite (M1.0) uses a **pure-Go driver** (`modernc.org/sqlite`),
  never CGO bindings. Any dependency that demands CGO needs its own ADR.
- Reproducible builds (`-trimpath`, pinned toolchain) are the Phase 7 goal;
  releases publish checksums.

### What stays outside the binary

- **Sidecar adapters** (Reticulum first, ADR-007) are separate optional
  processes; the base client never grows mandatory Python or radio deps.
- **Native shells** (Wails/Tauri) are a later, optional packaging of the same
  kernel behind the same local API — permitted precisely because the UI
  boundary is the API, not Go function calls. Adopting one would revise this
  ADR, not the kernel.

## Consequences

- Distribution is a file copy: no installer, no app store, no runtime deps,
  no 200 MB browser bundle. Fits solar-powered and festival nodes (Phase 6).
- Third-party clients (Phase 7) target the documented local API and can be
  written in anything.
- Trade-off accepted: a browser-tab UI feels less native than an Electron
  app. For a protocol whose promise is "your devices are the network," one
  honest portable binary wins.
