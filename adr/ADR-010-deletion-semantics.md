# ADR-010: Deletion semantics

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §16.2, §17.2; vision §9 (honest deletion), invariant §2.4

## Context

In a replicated, signed, append-only system, "delete everywhere" is
physically unpromisable: peers hold their own copies and may have already
decrypted content. The protocol must offer real, useful deletion mechanics —
and describe them honestly instead of simulating a global erase.

## Decision

### Deletion is a tombstone event

- Deleting content = emitting a signed **tombstone Signal**
  (`message.tombstoned`, `object.tombstoned`, …) referencing the target
  `event_id`. Reducers remove the content from all materialized views and
  Blocks. The tombstone propagates through normal sync.
- Authorization: reducers accept a tombstone from the original author or
  from a member holding the relevant moderation capability; anything else is
  ignored (fail closed).

### Local pruning

- After a tombstone (or by retention policy / user choice), a node MAY
  **prune** the stored frame: the segment record is rewritten as a prune
  stub `{event_id, terminal_id, device_id, sequence, previous, pruned_at}`.
- Chain verification treats a stub as a valid link (its `event_id` is
  trusted from the pre-prune verification, recorded at prune time).
- Pruned events are **never re-served**: they appear in the sync summary's
  `exceptions` list (plan §17.2), so peers do not request them and resumable
  sync stays correct. A peer that still wants the frame must find a node
  that kept it — the protocol neither guarantees nor prevents that.

### What deletion never claims

- No wire message means "deleted for everyone", and no API can express it.
  The projection vocabulary (ADR-008) offers exactly:
  - `removed_from_your_views` (tombstone reduced locally);
  - `pruned_on_this_device` (frame gone here);
  - `deletion_requested(age)` (tombstone emitted; other copies unverifiable).
- UI copy follows: "Deleted for you. Deletion requested from others — other
  copies cannot be verified." (invariant §2.4).

### Ephemeral material

- **Relay items**: auto-deleted at TTL expiry, unconditionally (plan §19 T3);
  deletion produces a relay receipt claim, not a delivery claim.
- **Outbox frames**: dropped at TTL with an explicit local failure state.
- **Blobs**: reference-counted via events; when the last referencing event
  is tombstoned-and-pruned, the blob is GC'd locally.
- **Retention policies** (per-Terminal, e.g. a sensor's 1-hour ring buffer,
  plan §4) are declared in the manifest and executed as automatic pruning.

### Keys

Epoch key destruction (`DestroyKey`, plan §14) after epoch rotation limits
readability of any ciphertext that later leaks from storage — deletion of
access is often stronger than deletion of bytes, and the threat model says so.

## Consequences

- Users get working deletion for the honest 99% case (remove from shared
  views, reclaim space) without the protocol ever lying about adversarial
  peers keeping copies.
- Prune stubs keep hash chains verifiable and sync resumable after deletion —
  no special cases in the sync engine beyond the exceptions list it already
  has.
- Segment rewrite on prune is O(segment); acceptable at 64 MiB segments
  (ADR-006).
