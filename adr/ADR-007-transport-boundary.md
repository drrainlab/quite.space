# ADR-007: Transport boundary

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §18–§20; vision §3.3–3.4, §4.6

## Context

The same Terminal must sync over LAN, files, blind relays, Reticulum, and
Meshtastic without the kernel or UI knowing which (invariant §2.5). The
boundary must make it impossible for a transport to read payloads or to
overstate what it delivered.

## Decision

### The contract

- The kernel↔transport contract is exactly the Go interface in plan §18
  (`Start/Stop/Capabilities/Discover/Send/Receive/PeerState`), defined once
  in `protocol/` and consumed by `kernel/routing`.
- Transports carry **opaque frames**: the canonical envelope bytes plus a
  small routing header (destination hint, priority, TTL). Transports MUST NOT
  parse, decrypt, re-serialize, or reorder-within-priority the frames they
  carry. Gateways (plan §5.7) are Terminals, not transports — translation
  happens above the boundary, with visible losses.

### Capabilities drive behavior

- `TransportCapabilities` declares: bandwidth class, max_payload, realtime,
  broadcast, ack level (`none | transport | end_to_end`), file support.
- The kernel's Routing Policy chooses lanes, fragmentation, and low-bandwidth
  projection from declared capabilities only — never from the transport's
  name (invariant §2.2 applied to transports).
- A transport may report only receipts it can prove: a `TransferReceipt`
  maps to at most `handed_to_transport` or `accepted_by_relay` in the
  delivery ladder (ADR-008). End-to-end levels require signed receipts from
  the destination, which transports cannot forge because they cannot sign.

### Adapter processes

- In-process Go adapters for loopback, bundle, LAN, relay, simulator.
- Out-of-process **sidecar adapters** (first: Reticulum over its Python
  reference implementation) speak a length-prefixed CBOR frame protocol over
  a local Unix socket or stdio: `hello` (capabilities), `send`, `recv`,
  `peer_state`, `receipt`. The sidecar protocol is versioned and documented
  in `specs/`, so third-party transports need no Go (Phase 7 groundwork).

### Conformance

- M0.7 ships an **adapter conformance suite**: the same sync scenario
  (two nodes, partition, reconnect, duplicates, reorder) must pass over
  loopback, bundle, and simulator with zero kernel changes — this is the
  acceptance test for the boundary itself.
- The simulator (T4) enforces 64/128/240-byte MTU profiles, loss, and delay;
  passing it is a prerequisite for writing the Reticulum/Meshtastic adapters
  (plan §19).

## Consequences

- Replacing a transport can never require UI or kernel changes (vision MVP
  criterion), because nothing above the boundary can observe transport
  identity except as diagnostics.
- Blind relays remain blind structurally: they receive only what transports
  receive — opaque frames.
- Sidecar hop adds latency for Reticulum; accepted for M1, revisit with a
  native implementation only if profiling demands it.
