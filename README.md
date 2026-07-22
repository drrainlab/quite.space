# Terminal Network

*Working titles: Terminal Network, Quiet Spaces. Core: **Terminal Mesh Kernel**.*

A decentralized, local-first social environment where the basic entity is not a
chat, channel, or profile, but a **Terminal** — a cryptographically addressable
entity that can be a person, a shared living space, a bot, an AI agent, a
sensor, an actuator, a gateway, a relay, or an archive.

> Your social space lives on your devices, not in our database.
> One social space. Any transport — from broadband internet to LoRa radio.
> Install the client — become a node.

## The formula

```text
Cryptographic Principal
+ addressable Terminal
+ signed Manifest
+ explicit Capabilities
+ typed Signals
+ provenance
+ append-only Event Log
+ deterministic State
+ transport-independent Sync
+ honest Receipts
+ optional Blind Relays
```

Guiding principle:

> A Terminal must never appear smarter, more reliable, more human, safer, or
> more available than it can prove.

## Status

**M0 — Protocol Seed: complete.** ADR-001–010 accepted; Go kernel implements
the deterministic CBOR codec with golden test vectors, identity (device
certificates, revocation, recovery bundles), terminal registry with
capability enforcement, the append-only event log (hash chains, fork
quarantine, crash-safe segments), the truth & claims engine with honesty
snapshot tests, sync v0 with fragmentation down to 64-byte MTUs, transports
T0/T1/T3/T4 (loopback, bundle, blind relay, low-bandwidth simulator) with a
conformance suite, and six headless terminals.

Try the Phase 0 proof (no server, no accounts):

```sh
go run ./cmd/terminal demo
```

**M1 in progress.** Landed: group encryption per ADR-005 (epoch keys, HPKE
key wrap, XChaCha20-Poly1305 payloads), private spaces with signed capability
invites, epoch rotation on membership change — the demo's blind courier is
now cryptographically blind, not just architecturally. Next: LAN transport
(M1.1), encrypted local database + SQLite (M1.0), Reticulum sidecar (M1.6),
desktop shell per ADR-011.

## Documents

- [VISION_AND_ROADMAP.md](VISION_AND_ROADMAP.md) — concept, MVP specification,
  global roadmap (Phase 0–7).
- [ENGINEERING_PLAN_M0_M1.md](ENGINEERING_PLAN_M0_M1.md) — Terminal ontology,
  architectural invariants, Truth Contract, Signal Envelope v0, crypto profile,
  transports T0–T6, milestones M0.0–M0.8 and M1.0–M1.7, demos A–E, testing
  strategy, definition of done.
- [adr/](adr/README.md) — architecture decision records (ADR-001–010 planned).

## Layout

Directory structure follows engineering plan §22: `cmd/`, `protocol/`,
`kernel/`, `transports/`, `terminals/`, `blocks/`, `clients/`, `specs/`,
`adr/`, `testvectors/`, `simulations/`, `examples/`, `docs/`. Each directory
has a one-line README describing its future contents.

## Next step

First engineering cycle (engineering plan §28): write ADR-001–010, fix Go
types without networking, implement the deterministic codec, publish test
vectors — then identity, event log, sync over loopback.

> Two headless nodes and a source-only Sensor must work before any pretty UI.
