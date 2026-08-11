# This package straddles the licence boundary

**Read before the first public push.** This is the one place in the tree
where the two-licence split described in [`/LICENSING.md`](../../LICENSING.md)
cannot be applied as things stand.

The package holds three files, and they do not belong on the same side:

| file | what it is | intended licence |
|---|---|---|
| `wire.go` | the wire protocol, hint and capability derivation | **Apache-2.0** — a client needs it, and so does anyone writing an independent relay |
| `relay.go` | `Store`, the in-memory item store | **AGPL-3.0-only** — server |
| `server.go` | `Server`, `StartServer`, `ServerLimits`, `connState` — **and also** `Client`, `DialClient`, `Put`, `Collect`, `Fetch`, `Probe` | both, in one file |

A Go package compiles as a unit, so whichever licence goes on the
directory goes on everything a consumer links:

- **Apache** → the relay *server* is Apache, so a modified closed public
  relay is permitted, and the AGPL side of the split is decorative.
- **AGPL** → `node/` becomes an AGPL derivative, and with it the desktop
  and Android clients — the opposite of what the split is for.

## The seam is clean

Measured over non-test code, the two halves have no overlapping
consumers:

```
server half   Server · StartServer · StartServerWithIdentity
              ServerLimits · DefaultLimits · NewStore · Item
  used by     cmd/terminal-relay/main.go
              cmd/terminal/demo.go

client half   Client · DialClient · DialClientPinned
  used by     node/{relay,relaypool,relaytrust,relayprobe,pass,public,
                    quicklink,mirror,entry,materialize,preview_fetch}.go
              transports/bridge/bridge.go
              cmd/relay-load/main.go
```

So the fix is a package split, not a redesign. `Store`, `Server`,
`ServerLimits`, `connState`, `serve` and `handle` move to a package of
their own; `wire.go` and the `Client` half stay here. Two non-test files
change an import. Tests that stand up a relay follow the server half.

Nothing about the wire format, the trust model or the behaviour changes.

## Why it has not been done here

These files are being edited in another working session (the relay
topology work), and a package split landing mid-flight would collide with
it. This note exists so the constraint is not rediscovered — or worse,
missed — at publication time.

**Until the split lands, this package should not be published under
either licence.** Apache-2.0 grants are perpetual and irrevocable, so
publishing `server.go` as Apache once makes the relay server permanently
forkable into a closed service, and that cannot be undone by a later
commit.
