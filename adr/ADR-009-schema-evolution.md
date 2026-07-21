# ADR-009: Schema evolution

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §12–§13, §17.1

## Context

The protocol will outlive its first release across heterogeneous, rarely
updated nodes (Raspberry Pi in a greenhouse, a phone, a BBS). Old nodes must
survive new events; new nodes must read old logs forever. Signed bytes make
silent migration impossible, so evolution rules must be explicit from day one.

## Decision

### Versioning layers

Three independent version axes, never conflated:

1. **`protocol_version`** — the envelope/sync/handshake layer. Negotiated at
   handshake (plan §17.1); a node advertises a supported window
   `[min, max]`. v0 is the current and only version.
2. **Payload schema ids** — `<domain>.<type>.v<N>` strings (e.g.
   `observation.temperature.v1`), carried in every envelope. The Schema
   Registry maps each id to its integer key table and validator.
3. **Reducer versions** — internal; a reducer change that alters materialized
   output requires a state rebuild (ADR-006), never a log change.

### Compatibility rules

- Within a schema version `vN`: **additive optional fields only.** Removing,
  renaming, retyping a field, or changing semantics requires a new schema id
  `v(N+1)`. Both versions may coexist in a log indefinitely.
- Readers apply **must-ignore**: unknown fields in a known schema are
  skipped. Because nodes store and forward received bytes verbatim
  (ADR-003), unknown fields survive relaying by old nodes intact.
- Integer key tables are **append-only**: a key, once assigned, is never
  reused or repurposed — including keys of retired fields.

### Unknown schemas

An envelope with a valid signature and chain but an unknown schema id is
**stored, synced, and forwarded as opaque** — it occupies its chain slot and
survives until the node learns the schema. It is not reduced; Blocks show an
honest "unsupported event" placeholder (invariant §2.6), never an error and
never nothing.

### Discipline

- Every schema and key table lives in `protocol/schemas` with its golden
  test vectors; CI diffs key tables against the previous revision and fails
  on any non-append change.
- Breaking `protocol_version` bumps are a last resort and require their own
  ADR plus a migration note in `specs/`.

## Consequences

- A 2026 node and a 2030 node can share a Terminal: the old one carries
  opaque events it cannot render, the new one reads everything.
- Schema proliferation is bounded by review: new versions are cheap on the
  wire but each adds a validator and vectors, keeping additions deliberate.
- Must-ignore + verbatim storage means forward compatibility costs zero
  code in transports and storage — it is a property of the encoding rules.
