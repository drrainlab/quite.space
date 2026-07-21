# Architecture Decision Records

Each ADR is a short, numbered, immutable document recording one architectural
decision: context → decision → consequences. Superseded decisions are not
edited — a new ADR replaces them and links back.

File naming: `ADR-001-terminal-ontology.md`, etc.

Suggested template:

```markdown
# ADR-NNN: Title

- Status: proposed | accepted | superseded by ADR-MMM
- Date: YYYY-MM-DD

## Context
## Decision
## Consequences
```

## Series (M0.0, engineering plan §23)

- [x] [ADR-001 Terminal ontology](ADR-001-terminal-ontology.md) — one Terminal abstraction, canonical glossary, kind = UI hint only
- [x] [ADR-002 Identity and device keys](ADR-002-identity-and-device-keys.md) — Ed25519, root→device certification, revocation, recovery bundle
- [x] [ADR-003 Deterministic serialization](ADR-003-deterministic-serialization.md) — deterministic CBOR, integer keys, store bytes verbatim, no COSE in v0
- [x] [ADR-004 Event identity](ADR-004-event-identity.md) — id = SHA-256 of frame, (terminal, device) chains, Lamport ordering, fork quarantine
- [x] [ADR-005 Group encryption](ADR-005-group-encryption.md) — epoch keys, XChaCha20-Poly1305, HPKE wrap, 32-device cap, MLS path
- [x] [ADR-006 Local storage](ADR-006-local-storage.md) — append-only segments as truth, SQLite always rebuildable, encrypted keystore
- [x] [ADR-007 Transport boundary](ADR-007-transport-boundary.md) — opaque frames, capability-driven routing, sidecar adapters, conformance suite
- [x] [ADR-008 Claims and honesty model](ADR-008-claims-and-honesty-model.md) — claim origins, proof-gated delivery ladder, honesty snapshot tests
- [x] [ADR-009 Schema evolution](ADR-009-schema-evolution.md) — three version axes, must-ignore, append-only key tables, opaque unknown schemas
- [x] [ADR-010 Deletion semantics](ADR-010-deletion-semantics.md) — tombstones, prune stubs, sync exceptions, no "deleted everywhere" claim

Acceptance for M0.0: no core entity has two conflicting definitions — the
glossary in ADR-001 is the single source; other ADRs only reference it.
