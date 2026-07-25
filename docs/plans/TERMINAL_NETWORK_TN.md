# Terminal Network wave (TN) — frozen baseline, rev 2.1

Decisions live in [ADR-015](../../adr/ADR-015-terminal-network.md).
This document freezes the gate order and per-gate acceptance.

```
Radio terminal ──LoRa──┐
Sensor ──Meshtastic────┤
                       ▼
                quiet-bridge          (blind, custodian key, per-dest-hint)
                       │
                 blind relay
            ┌──────────┴──────────┐
            ▼                     ▼
     mobile terminal        internet bot
```

## Gate order

`ADR accepted → TN-0 → TN-1 → TN-2A → TN-B → TN-2B (independent)`

TN-2B never blocks TN-B: the headline demo — internet terminal → blind
relay → quiet-bridge → Meshtastic carrier → radio terminal, with the
original signature, identity and membership intact — runs over raw and
TN-2A framing.

## TN-0 — Universal envelope enforcement (no wire changes)

- `Header.ForwardingScope()` API (CustodyAllowed / NoCustody) over the
  existing MaxForwards key; nothing ever decrements it.
- `kernel/routing/seen.go`: bounded seen-cache (16-byte EventID prefixes,
  TTL, `length‖crc32c` snapshot) + LinkID/LoopDomain split-horizon rule.
- `kernel/sync`: lane-ordered `pushMissing` (chain-internal order sacred,
  cross-chain by pending-head lane, max burst + weighted round-robin
  fairness), frames-before-blobs explicit, `Engine.OnSent` hook.
- node: emit helper records `created_local`/`queued`;
  OnSent → `handed_to_transport` + a bounded eventID→link projection.
- `PushToRelay` excludes expired and NoCustody frames from bundles (the
  relay itself stays structure-blind).

Accept: expired frames ingest and chains never stall; custody filter
proven; lane ordering + convergence regression over Lora64/128/Mesh240
sim profiles × {drop, dup, reorder}; seen-cache matrix (dup / TTL / cap /
snapshot round-trip); delivery ladder recorded without overclaim.

## TN-1 — Router library + node seam

- `kernel/routing/{registry,subscribe,queue,policy,routes}.go`:
  scheme registry (lan/mesh/relay/bundle/sim + reserved `reticulum`),
  per-link subscription sets, durable append-only segment queue (no new
  deps), delivery-class + airtime policy, route projections.
- `node/lan.go`: `adoptLink` gains optional `allow func(FrameMeta) bool`
  (nil = bit-identical current behavior). FrameMeta = {EventID,
  SourceTerminal, Destination, Schema, Priority, ExpiresAt, Size,
  IngressLink}.

Accept: queue crash-sim → reopen → drain, torn tail, compaction, caps;
TTL sweep = min(op, ExpiresAt); subscription filtering; FULL existing
node suite green with nil filter; token bucket + priority-with-aging
starvation-free.

## TN-2A — Stateless compact profile

- `transports/compact`: `Wrap(inner)` endpoint; DEFLATE-when-smaller +
  stateless reversible reduction; magic prefix + version + flags +
  reserved hop-budget key; effective MaxPayload ~2 KiB outward;
  sub-fragmentation via the existing fragment grammar.
- Real radio default RAW; `--compact` opt-in; meshhub/sim default compact.
- Corpus benchmark (text / reaction / presence / membership /
  observation / manifest / receipt): packets per event, p50/p95 bytes,
  cold / 30% loss. ≥35% fewer packets is a TARGET, not a blocker.

Accept: byte-exact round-trip → `VerifyFrame` (gate-blocking); compact
sender vs raw-only receiver fails closed (operator config restores raw;
auto-fallback is TN-2B); magic-property: the compact prefix can never be
a valid start of the raw fragment grammar; delivery classes hold; airtime
bucket holds under load.

## TN-B — quiet-bridge daemon

- `cmd/quiet-bridge` (terminal-relay shape): `--radio serial:|tcp:`,
  `--relay`, `--listen`, `--data`, `--subscriptions FILE`,
  `--airtime`, `--ttl`, `--compact`.
  **`--learn` was withdrawn in RB-0B** and now exits non-zero: RB-0A's
  mailbox contract means routing to the internet requires an
  operator-provisioned capability, which no amount of listening can
  produce. A mode that discovers routes it can never carry is worse than no
  mode, because an operator would reasonably believe it worked. Discovery
  returns as its own provisioning workflow or not at all.
- Blind by type (`routing.RoutedOpaqueEnvelope`) + import-boundary test;
  the test is what enforces it, the type is what makes it legible.
- `CustodyRecord{OpaqueEnvelope, IngressLink, IngressDomain, EnqueuedAt,
  Attempts}` — ingress origin durable (split-horizon survives restart).
- Relay poll covers ALL hint buckets within retention:
  `ceil(relayTTL/rotation)+1`, operator-capped.
- Custodian keypair; signed ACK strictly after append+fsync; node-side
  pinned `custodians: [{link_domain, public_key}]`, TOFU forbidden.
- `--learn` OFF by default; probation admission (pairing window / known
  node / bearer capability, per-source + global token buckets, tiny
  quota until operator approval); learn gates radio→relay uplink only.

Accept: two-segment loop both directions (bridge only ever sees
encrypted payloads); two bridges on one hub+relay — bounded storm;
radio-A→bridge→radio-B allowed, same-LinkID return suppressed;
ACK → immediate kill → frame still in custody; spoofed ACK rejected;
valid-signature identity flood contained by probation/buckets;
3 destinations over ONE carrier with subscriptions selecting 2;
membership epoch at 2/8/32/128 members vs the 8 KiB radio cap with the
documented-boundary DoD; memory/caps soak (Pi-ready). Live ladder:
localhost full chain with the route line («relay custody → bridge →
radio»), then real hardware (Pi + serial radio).

## TN-2B — Stateful id table + link negotiation (independent)

- `transports/compact/table.go`: 32B→2B link table; table
  epoch/generation, define ids; TABLE_ACK before short indexes; redundant
  defines on AckNone links until warm; receiver-reset detection; periodic
  redefine. `mode=auto` = narrow link-local codec-version negotiation
  (NOT general capability discovery).

Accept: lost DEFINE; receiver reboot under a warm sender; generation
mismatch; undefined-index drop + heal; warm-link savings ~90–120 B/frame
(≥35% target); mixed raw↔compact↔versions.
