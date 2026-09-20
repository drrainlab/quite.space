# DS-5 — Windows: the third column of the same machine

*Status: proposed (2026-08-30). Origin: owner decision to plan the Windows
release first among the store/platform ladder (Windows → macOS notarization →
Play/F-Droid → TestFlight).*

## Why this is small, and where it is not

The desktop app is already the right shape for this port. DS-3 built one
process holding one node with a window in front of it; the shell is pure
`net/http` with the handler swap (`locked → opening → open → failed`),
`boundary_test.go` keeps Wails confined to `internal/wailsx`, and every test
runs without a display. **None of that is platform code.** Wails targets
Windows first-class through WebView2. What remains is exactly three things:
the platform seams of the *node* on Windows, the packaging, and the honest
words on the download page.

The freeze helps twice: ADR-033 means this is pure client work (no protocol
decision can hide in it), and the same rule that froze the macOS bundle id
(`space.quite.desktop` — "changing it later silently orphans everything")
applies to every Windows identity choice below. **Pick once, in this plan,
not in a build script.**

## Frozen-on-first-release identities (decide at grooming, then never)

| identity | proposal | keyed on it |
|---|---|---|
| data dir | `%LOCALAPPDATA%\Quite Space` | the node's whole world; DS-1 data-dir lock |
| app id / shortcut name | `Quite Space` (AppUserModelID `space.quite.desktop`) | toasts, taskbar pinning, startup entry |
| installer product GUID | minted once, stored in the bundle script | upgrades vs. duplicate installs |
| asset name | `quite-space-windows-amd64.exe` | the site's `releases/latest` row, SHA256SUMS |

## The ladder

**W0 — the node on Windows, proven not assumed.** The root module is
`CGO_ENABLED=0` by ADR-011, so it cross-compiles today — which proves
nothing. Run the actual test suite on `windows-latest` in CI (a
non-release workflow job is enough) and expect the seams to show:
path separators, the data-dir lock (flock → `LockFileEx`), file-mode
assumptions (0600 is a no-op on NTFS — the honest note is that Windows
ACLs are the protection, not the mode bits), `%LOCALAPPDATA%` resolution,
time-source behavior through sleep (the clock pair that certifies
`suspended` gaps). Exit: root suite green on Windows, seams fixed or
honestly documented.

**W1 — the shell opens a window.** `cmd/desktop` on Windows via the same
exact-pinned Wails: re-run `cmd/wails-probe`'s checklist on Windows on the
SAME pin before trusting it (the README's ritual, third platform). Wails on
Windows drives WebView2 without cgo — verify with the probe rather than
believe it. Decide tray semantics (same law: closing hides, only Quit ends),
single-instance via the existing data-dir lock. **The firewall moment is the
UX cliff of this slice**: the first LAN listener triggers Windows Defender's
dialog — the app cannot avoid it, so it must *precede* it: the interface
says what is about to happen and why (same voice as the map-tiles switch:
"LAN sync listens on your local network; Windows will ask").

**W2 — packaging.** One artifact for the site: an NSIS installer
(`quite-space-windows-amd64.exe`) that checks for the WebView2 Evergreen
runtime and bootstraps it when absent (Win11 and updated Win10 already carry
it; the installer says so rather than failing strangely). Portable zip is a
non-goal for v1 — two artifacts is two support surfaces. **Unsigned, and the
page says so**: SmartScreen will interpose "Windows protected your PC"; the
remedy line for the download page is `More info → Run anyway` — the exact
analog of the macOS `xattr` paragraph, same honesty, same placement.

**W3 — CI and the release train.** A `windows` job in `release.yml` beside
`macos` and `linux`: windows runner, bundle script (PowerShell twin of
`bundle-macos.sh`), artifact into the release and into `SHA256SUMS`. From
that tag on, the site's Windows row flips from `transmission pending` to a
live `download ↓` — the row, the instructions slot and the `latest` link
mechanics already exist and need only the meta string.

## Windows-specific seams to expect (named now, so they are found cheap)

- **Serial/RNode**: COM-port naming and enumeration differ; the serial lib
  supports Windows but the *probe path* (`serial:COM5` vs `/dev/tty…`) needs
  its Windows sentence in the radio docs.
- **Toasts**: notification delivery goes through AppUserModelID — the frozen
  app id above; Quiet Chimes' "one instrument, two mouths" needs its Windows
  mouth verified, not assumed.
- **Autostart**: out of scope for v1; if ever, it is a visible switch, never
  a default.
- **Doze-analog**: modern standby suspends timers differently; the sweep's
  `suspended` gap reasoning is Android-only today and stays that way — the
  desktop records no sweeps.

## Non-goals (v1)

Code-signing certificate (EV/OV — later, money and identity questions);
ARM64 Windows; MS Store; winget manifest (cheap and worth doing, but only
after one release proves the installer stable); portable zip.

## Site side (quite.space — ready, waiting)

The downloads row for Windows exists with `transmission pending`; on the
first tag with the asset it gets: meta `exe · <version> · <size>`, href via
`releases/latest/download/quite-space-windows-amd64.exe`, and an
instructions block: Win10/11 x64, WebView2 note, the SmartScreen
`More info → Run anyway` remedy with the same one-command honesty the macOS
row has.
