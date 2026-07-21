# ADR-003: Deterministic serialization

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §13, §21

## Context

Signatures and content-derived event IDs require byte-stable encoding: the
same logical structure must always serialize to the same bytes, on every
implementation. The wire format must also stay compact enough for 240-byte
low-bandwidth frames (plan §19 T4) and be diagnosable by humans.

## Decision

### Wire format

- **Deterministic CBOR** per RFC 8949 §4.2.1 (Core Deterministic Encoding):
  shortest-form integers, definite-length strings/arrays/maps only,
  bytewise-lexicographic map key ordering.
- Top-level protocol structures (envelope, manifest, sync frames) use
  **integer map keys** assigned by published key tables in `protocol/codec`.
  Key tables are append-only (ADR-009).
- Floating-point values are forbidden in signed structures; quantities are
  encoded as scaled integers with a unit field (e.g. centi-degrees).
- Timestamps are integers (Unix seconds for wall-clock advisory fields,
  plain integers for logical clocks).

### Signing and hashing rule

- The signature is computed over the canonical CBOR encoding of the structure
  **with the signature field absent** (not null — absent).
- Received canonical bytes are the artifact: nodes store and forward the
  original bytes verbatim and MUST NOT re-serialize a received structure for
  storage, hashing, or forwarding. Re-encoding happens only in diagnostics.
  This makes unknown-field preservation automatic (ADR-009) and keeps
  signatures verifiable forever.

### Diagnostic view

- A lossless JSON diagnostic representation (integer keys mapped to names via
  the same key tables, byte strings as hex) is produced by
  `cmd/terminal-inspect`. YAML appears only in docs and fixtures. Neither is
  ever signed or hashed.

### COSE

COSE (RFC 9052) was evaluated as the signing/encryption container. Decision:
**not adopted for envelope v0.** The envelope defined in plan §12 already
carries authorship, chaining, and routing fields that COSE would wrap
redundantly, and single-signer Ed25519 over canonical bytes does not need
COSE's algorithm agility. Revisit before protocol freeze (Phase 7) if
interoperability with COSE ecosystems becomes a goal; the codec package
isolates this choice.

## Consequences

- Golden byte-level test vectors for every top-level structure live in
  `testvectors/` and are normative; an implementation that produces different
  bytes is wrong (M0.1 acceptance).
- The store-bytes-verbatim rule shapes local storage (ADR-006): the event
  store persists received frames, and materialized views are derived data.
- Integer keys make wire frames compact but unreadable without key tables —
  acceptable, since `terminal-inspect` ships from M0.
