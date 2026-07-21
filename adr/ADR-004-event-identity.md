# ADR-004: Event identity

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §12, §16, §17; vision §5.1

## Context

Events need identities that are content-derived (dedup, integrity), chainable
(tamper evidence, gap detection), and computable offline by any node. Wall
clocks on devices are untrusted and often wrong.

## Decision

### Event ID

- `event_id` = **SHA-256 over the full canonical envelope bytes**, signature
  included — i.e. the hash of exactly the frame that is stored and forwarded
  (ADR-003).
- The ID is **derived, never transmitted**: receivers recompute it. The `id`
  field shown in plan §12 is a diagnostic projection, not a wire field.
- Ed25519 signatures are deterministic (RFC 8032), so identical logical
  events produce identical bytes and identical IDs.

### Author chains

- Chain scope is **(terminal_id, device_id)**: each device maintains one
  strictly increasing `sequence` per Terminal, starting at 1.
- `previous` = the `event_id` of the same chain's preceding event; absent for
  `sequence` 1.
- Uniqueness rule: one valid event per (terminal, device, sequence). Two
  different events claiming the same slot constitute a **chain fork**: both
  events and all their descendants are quarantined, the device is flagged
  locally (`observed.chain_fork` label), and the fork is surfaced as a claim —
  never silently resolved (invariant §2.7 fail closed).

### Ordering

- `logical_clock` is a Lamport clock per Terminal: on emit,
  `max(own, max_seen) + 1`. It is the only ordering input reducers may use
  across authors; ties break by `event_id` bytewise.
- `created_at` (wall clock) is advisory display data and MUST NOT influence
  reduction order, validity, or sync.

### Replay and dedup

- Dedup is by `event_id`: a re-received frame is a no-op (sync idempotency,
  plan §17.3).
- Replay of an old frame is therefore harmless by construction; replay
  *semantics* for commands get an additional nonce + expiry at the schema
  level (plan §11), checked by reducers.

## Consequences

- Deleting materialized state and replaying the log reproduces identical
  state on any node holding the same frames (M0.4 acceptance): all inputs to
  reduction are content-addressed and totally ordered.
- Sync summaries stay compact: `contiguous_until` + exception list per chain
  (plan §17.2) works because sequences are dense integers.
- Quarantine-on-fork means a malicious device can disrupt its own history's
  availability but cannot silently rewrite it.
- Hash agility (migrating off SHA-256) would be a breaking protocol version
  change; accepted for v0 simplicity.
