# Relay Status API — specification

Status: **draft spec, not yet implemented.** This document is the contract
for a small read-only HTTP endpoint on `terminal-relay`, intended for the
quite.space website (and any operator dashboard) to show per-relay status,
load and traffic. It is written against the relay as it exists today
(RR-3 probe, RR-7 metrics), so every field below is either already
computable or named as a counter to be added.

## What exists today, and why none of it serves a website

| surface | where | reachable by a browser? |
|---|---|---|
| `msgProbe` → `msgProbeOK` (wire 12/13): proto range, load class, accepting, wall clock | relay TLS wire protocol | no — binary protocol over self-signed pinned TLS |
| `metrics items=N bytes=N conns=N` every 5 min | terminal-relay stdout | no — a log line |
| `GET /api/relay/diagnostics` | the node, localhost, bearer token | no — local and private by design |

The website needs plain HTTPS + JSON + CORS. Hence this endpoint.

## The endpoint

```
GET /status        → 200 application/json
GET /healthz       → 200 "ok" (load balancer / uptime checks; no body contract)
```

Served by an **optional second listener**, plain HTTP, **off by default**:

```
terminal-relay --listen :7411 --status-listen 127.0.0.1:7412
```

It is deliberately a separate listener rather than a new wire message:
the relay protocol stays exactly as blind and as small as it is, and the
status surface can be firewalled, proxied or disabled independently of
the transport.

### Response shape

```json
{
  "relay":      "staging-1",
  "label":      "Shared test relay",
  "protocol":   { "min": 1, "max": 1 },
  "accepting":  true,
  "load":       "normal",
  "uptime_seconds": 184220,
  "now_ms":     1765432100000,

  "connections": 12,

  "store": {
    "items":     340,
    "kib":       1206,
    "fill":      0.03
  },

  "traffic": {
    "window_seconds":   60,
    "puts_total":       48211,
    "replaces_total":   1200,
    "fetches_total":    901233,
    "collects_total":   455102,
    "probes_total":     8122,
    "rate_limited_total": 310,
    "kib_stored_total": 88211,
    "kib_served_total": 512400
  }
}
```

Field semantics:

- `relay`, `label` — operator-set strings (`--status-name`, `--status-label`),
  both optional. They exist so the website does not have to map IP → name;
  they carry no authority (the SPKI pin is the identity, and the status
  endpoint never replaces it).
- `protocol`, `accepting`, `load`, `now_ms` — **exactly the fields
  `msgProbeOK` already answers**, same vocabulary. `load` is the existing
  `loadClass()`: `normal` | `busy` | `overloaded`, derived from store fill
  (`>0.7` busy, `>0.9` overloaded). One source of truth: the status handler
  calls the same functions the probe handler calls.
- `connections` — `Server.Conns()`, current open TLS connections.
- `store.items` / `store.kib` — `Pending()` / `PendingBytes()` (rounded, see
  privacy). `fill` — `FillRatio()`, rounded to 2 decimals.
- `traffic.*_total` — **monotonic counters since process start** (these are
  the one new thing: atomic counters incremented in the server's message
  switch, one per verb, plus two byte totals). Monotonic-since-start is
  deliberate: the website computes rates from deltas between its own polls,
  and the relay keeps no windows, no histories, no time series — nothing
  that would grow retention semantics inside a zero-retention process.
  A counter reset (process restart) is detected by `uptime_seconds`
  decreasing; the site discards one delta and moves on.

### Privacy rules — the blind relay stays blind

These are contract, not implementation detail, and they are what makes
this endpoint compatible with ADR-016's three properties:

1. **Aggregates only.** No hints, no per-hint distributions, no item
   sizes, no connection addresses, no timestamps of individual
   operations. The response must never let an observer ask "did hint X
   receive anything".
2. **The response is a snapshot cached for 60 seconds.** The handler
   computes at most one snapshot per minute and serves the cached copy to
   every caller in between (`Cache-Control: public, max-age=60` says so
   honestly). Without this, a public counter endpoint on a low-traffic
   relay is a real-time activity oracle: poll it at 1 Hz and you learn
   the second somebody's message arrived. With a 60 s snapshot the
   endpoint discloses less timing information than the wire probe already
   does.
3. **Bytes are rounded to KiB, item counts served as-is.** Byte-exact
   totals on a quiet relay can leak individual item sizes across one
   delta.
4. **No historical data.** The endpoint answers "now" (as of the last
   snapshot). Graphs live on the website's side from its own polling, or
   nowhere.

### Serving it to a browser — the mixed-content problem

`https://quite.space` cannot `fetch()` a plain-HTTP endpoint (mixed
content, blocked), and the relay's own TLS is a self-signed pinned key
browsers will refuse. Two honest deployments:

**A. Reverse proxy with a real certificate (recommended).** On the VPS,
Caddy or nginx with a Let's Encrypt cert on a subdomain, proxying to the
localhost status listener:

```
status.quite.space  →  caddy (LE cert)  →  127.0.0.1:7412
```

Caddyfile, complete:

```
status.quite.space {
    reverse_proxy 127.0.0.1:7412
}
```

The relay binary never touches ACME, domains or certificates — that
complexity stays in the proxy, where it belongs.

**B. Website-side fetch.** If the site has any server-side surface (an
Astro endpoint, an edge function), it polls the relay's plain-HTTP
status over the open internet (or via A) and re-serves it same-origin.
Adds a cache layer for free; costs a moving part.

Static-site-with-no-backend forces option A.

### CORS and caching headers

```
Access-Control-Allow-Origin: *
Cache-Control: public, max-age=60
```

GET/HEAD only; anything else is 405. The data is public and read-only,
so a wildcard origin is correct — there is nothing to protect with a
narrower one.

## Website integration contract

- The site holds a static registry of relays (the same shape as
  `BuiltinRelayRegistry`: id, label, region, endpoint, status URL). The
  status endpoint never lists other relays — no federation, no
  discovery, per ADR-016.
- Poll each relay's `/status` every 60–120 s. **Unreachable is a normal
  state, not an error state**: render "no answer", never a stack trace,
  and keep showing the last seen values with their age.
- Status chip: `accepting` false or unreachable → down/red;
  `load=overloaded` → amber; else green with the load word.
- Traffic: `rate = (counter_now − counter_prev) / poll_interval`. Guard:
  if `uptime_seconds` went down or any counter went down, skip this
  delta (restart). Show ops/min and KiB/min, not totals — totals since
  an arbitrary restart mean nothing to a reader.
- Never render the SPKI pin as if the status endpoint attested it. If
  the site shows pins (useful for `terminal relay trust`), they come
  from the registry, stated as "expected identity".

## Implementation sketch (one small commit on the relay side)

- `transports/relay/server.go`: a `stats` struct of `atomic.Uint64`
  counters, bumped in the existing message switch (one line per verb,
  two for byte totals, one for rate-limit refusals);
  `Server.StatusSnapshot()` returning the value struct above, reusing
  `loadClass()`, `Conns()`, `Pending()`, `PendingBytes()`, `FillRatio()`.
- `cmd/terminal-relay/main.go`: `--status-listen` (default empty = off),
  `--status-name`, `--status-label`; a `net/http` handler over the
  snapshot with the 60 s cache; the existing 5-minute stdout metrics
  line stays untouched.
- Tests: snapshot fields agree with probe fields (one source of truth);
  the cache really holds for 60 s (two immediate calls, one snapshot
  computation); counters are monotonic under concurrent load; the
  status listener refuses non-GET; and the response contains no key
  that is not in this spec (shape pinned, so a future field is a
  deliberate act).
