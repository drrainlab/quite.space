# ADR-008: Claims and honesty model

- Status: accepted
- Date: 2026-07-22
- Relates to: engineering plan §2.3–2.4, §8–§10

## Context

The system constantly handles statements of very different reliability: a
manifest's self-description, a relay's ACK, a stale presence announce, an AI
agent's authorship. Mixing these levels is how ordinary apps lie ("delivered",
"online", "verified"). The Truth Contract (plan §8) must be enforced by the
kernel, not left to UI discipline.

## Decision

### Claims

- Every non-cryptographic property is a **Claim** with an explicit origin
  from the closed v0 enum: `protocol_derived | self_declared | peer_observed
  | third_party_verified | locally_assigned | unknown` (plan §8.1), plus
  issuer, issue time, and optional expiry.
- The Trust & Claims Engine (`kernel/trust`) is the only component that
  evaluates claims. Its projection API returns `(value, origin, proof
  reference, age)`; clients render exactly that tuple. There is no API that
  returns a bare boolean like `online` or `verified`.

### Delivery ladder

- Delivery status is the ordered ladder of plan §8.2, from `created_local`
  to `acknowledged_by_human`. The kernel stores, per (event, destination),
  the **highest level for which a proof event exists**, with a reference to
  that proof:
  - transport receipts prove at most `handed_to_transport` /
    `accepted_by_relay` (ADR-007);
  - `received_by_terminal` and above require a **signed receipt Signal**
    from the destination;
  - `presented_to_human` / `acknowledged_by_human` exist only if the
    destination chose to emit them — read receipts are opt-in claims, not
    defaults.
- Status upgrades without a proof event are structurally impossible: the
  trust engine's only write path is "ingest proof event → recompute level".
  Anything unproven projects as `unknown` (invariant §2.7, fail closed).

### Presence

Presence carries `emitted_at` + `expires_at` (plan §8.3). After expiry the
projection switches to `last_known(state, age)`; a current-tense presence
state for an expired announce cannot be expressed in the API.

### Authorship and AI honesty

- Every Signal carries `authorship.produced_by` from the closed enum of plan
  §10.2 (`human | human_with_ai_assistance | ai_agent | deterministic_bot |
  sensor | imported | unknown`) plus `human_approved`.
- Undeclared model identity projects as `unknown` — inference of provider or
  model from indirect signals is prohibited (plan §10.3).
- Gateway transformation records and AI transformation chains (plan §8.4,
  §10.4) are claims attached to the derived event, linking source events.

### Enforcement in CI

The **honesty snapshot tests** (plan §26) are part of M0.5 acceptance: fixture
states where relay ACK ≠ delivered, stale ≠ online, self-declared ≠ verified,
AI ≠ human, prediction ≠ measurement — each must project the weaker value.

## Consequences

- Honest UI is the path of least resistance: the only available API already
  speaks in origins, proofs, and ages.
- Read receipts, presence, and verification are all opt-in and locally
  evaluated; no global truth service exists to be wrong.
- The closed origin enum keeps v0 tractable; new origins (e.g. quorum
  attestation) require a schema version bump (ADR-009) and their own ADR.
