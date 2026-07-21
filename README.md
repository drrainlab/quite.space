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

**Pre-M0 — protocol seed in design.** No code yet; the repository currently
holds the founding documents and the directory skeleton. The kernel stack is
Go (engineering plan §21).

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
