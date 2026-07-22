# ADR-012: Space Pass = Join Pass

- Status: accepted
- Date: 2026-07-22
- Relates to: ADR-005 (group encryption), ADR-008 (honesty), the Human
  Interface & Space Pass plan (docs/plans/HUMAN_INTERFACE_SPACE_PASS_R3.md)

## Context

The capability invite (ADR-005 / M1.A) is minted for a *known* device: the
owner must already hold the invitee's device id and X25519 key. The product
needs a shareable **Space Pass** — a link or QR handed to someone whose
device is not yet known. A naive design embeds the space's epoch keys in the
pass so the holder can read immediately; but then opening a pass already
grants standing access, which contradicts the user-facing metaphor ("Alice
invites you, the owner confirms your entry") and leaks history to anyone who
merely holds an unexpired, unrevoked, possibly-shared token.

## Decision

A Space Pass is **authority to request entry**, not an access key.

> The pass carries authority to request entry → the owner's device validates
> and consumes it → membership changes in the shared event log → the epoch
> rotates → the newcomer receives only the new epoch → history starts at
> acceptance.

### Invariants (binding on all implementations)

1. **A pass contains no epoch keys.** Opening a pass decrypts nothing.
2. **`pending` has no access.** Until acceptance, the requester cannot read
   any space content — this is enforced by cryptography, not UI.
3. **Confirmation is automatic**, performed by the owner's device against
   the pass rules (a future `owner_confirmation` mode is separate).
4. **One authoritative acceptor** (v1): the controller replica that holds
   the terminal seed. Multi-owner-device acceptance is a documented v1
   limitation.
5. **History follows the space's declared memory policy** (amended
   2026-07-23, LR-4). The pass itself still carries no keys and `pending`
   still reads nothing — but the SEALED ACCEPTANCE (owner-confirmed,
   encrypted to the newcomer's device) wraps past epoch keys when the
   space's memory is `everything` or `manual`, because those spaces
   publicly promise "this place remembers everything that happens in it"
   and a memory the newcomers cannot see is not a memory. Under
   `private_history` the original rule holds bit-for-bit: only the epoch
   minted during acceptance is granted; the past stays with those who
   lived it, enforced by cryptography.
6. **Revoking a pass never removes a member.** Revoke blocks new and
   still-pending requests; removing a member is a separate operation that
   rotates the epoch and emits its own event.
7. **Acceptance is at-least-once delivery, crash-safe and idempotent.** A
   per-`request_id` saga journal survives restarts; reprocessing returns the
   stored result.
8. **One request consumes at most one use and causes at most one rotation.**

### Shape (details in the plan, not normative here)

- Pass = bearer-secret (`pass_id = SHA256("qs.pass.id.v1" ‖ bearer_secret)`),
  `space_id` + `space_signing_pub` (with mandatory
  `SHA256(space_signing_pub)==space_id` bootstrap check), acceptor device
  HPKE key, random `rendezvous_token`, expiry, `max_uses` (1..10 in v1),
  `membership_profile="member.v1"`, domain-separated signature. No
  epoch keys, no manifest frame. QR budget ≤900 B comfortable, ≤2 KB hard.
- Three distinct canonical schemas: `membership.join.requested.v1` (sealed
  request to the owner), `membership.join.accepted.v1` (sealed response to
  the newcomer, carrying the manifest and the wrapped new epoch), and
  `membership.member.added.v1` (the canonical in-log event through which all
  participants learn of the new member).

## Consequences

- The user metaphor and the cryptography agree: a pass lets you knock; the
  owner's device admits your device into the space's cryptographic circle.
- Revoked, expired, or replayed passes never read anything.
- `max_uses` is real membership control, not just future-access scoping.
- Cost: entry is asynchronous — the owner's device must come online to
  confirm. This is an honest property of a zero-custody, no-server design,
  surfaced plainly in the UI, not hidden.
- Instant Pass (epochs pre-wrapped, "grants access immediately") may return
  later as an explicitly-labeled advanced mode; it is out of v1.
