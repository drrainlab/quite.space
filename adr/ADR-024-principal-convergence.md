# ADR-024 — Principal convergence: membership belongs to the person

Status: accepted (2026-08-20) · Stream 1B of BETA_AUDIT_2026-08-20 rev 2
Companion doctrine: ADR-023 (inability is never success, never silence)

## The invariant (owner's formulation, the release gate of v0.1.5)

> **Certified devices of one principal eventually converge on the
> principal's relationships and cryptographic space access, regardless
> of which device performed the action.**

A person establishes relationships AS A PERSON: "I added Bob" — never
"my Android added Bob, now introduce my Mac to him." Pairing is not a
one-time snapshot; it makes two certified devices replicas of
principal-scoped state.

The boundary of what converges:

    PRINCIPAL-SCOPED                      DEVICE-LOCAL
    relationships / spaces                route health, backoff, cadence
    membership & admission knowledge      hardware transports, USB/radio
    certified sibling set                 connection handles
    space grants                          local counters
    epoch access (current AND future)     temporary diagnostics
    proofs needed to inhabit spaces       (each device's own SelfIngress)

Stream 1A already proved the first clause of this doctrine for routes
("route knowledge belongs to the person" — the freight amendment); this
ADR takes the same thought to its end.

## The fork this note exists to decide

**Question: does space membership belong to a principal or to a device
key?** The code today answers both at once, and the split is the bug:

- *Cryptographically* membership is per-DEVICE: epochs wrap to device
  X25519 keys (`kernel/crypto.WrapEpoch`, `terminals/private.go`
  `members map[id.DeviceID][32]byte`); no principal appears in any wrap.
- *At admission* membership is per-PRINCIPAL: a frame is admitted on a
  cert chain to a known principal; private spaces check nothing else —
  holding the key IS membership.

The measured consequence (CODE ABSENCE, the latent blocker): an owner's
members map never learns a sibling device — nothing calls `AddMember`
on seeing `DeviceCertified` — so the owner's next rotation deafens
every paired sibling in the room: `Undecryptable++`, silently.

## Decision

**Membership belongs to the PRINCIPAL. The device wrap list is derived
state, recomputed by the owner from certificates already in the log.**

Two mechanisms, one per half of the problem:

### 1. Epoch expansion at the owner (future epochs, revocation)

Before every rotation, the owner expands its member set:

    for each member device D:
        P  = principal named by D's certificate      (already in the log)
        Ds = P's currently certified, unrevoked devices (already in the log)
        AddMember every D' in Ds (X25519 from its certificate)
    then RotateEpoch as today

No new protocol message: device certificates and revocations already
travel plaintext in the space log precisely so admission can see them
(`publishCertLocked`; `terminals.go:364-366`), and
`identity.Certificate.X25519Pub` has carried the wrap key since MD-0 —
the code's own comment anticipates this: "a controller holding one has
everything AddMember needs" (`identity_admit.go:200-206`).

Revocation falls out for free, exactly as the owner's review hoped: a
revoked device simply stops appearing in the expansion, so it stops
receiving new epochs everywhere the moment each owner next rotates. Old
epochs are knowledge of the past and are not retracted (doctrine).

### 2. SpaceGrant over the identity plane (spaces joined after pairing)

A membership acquired on one device reaches the siblings WITHOUT
passing through the new space (the bootstrap circle is real: a device
cannot receive an event inside a space it does not know exists).

    Phone joins Bob's space X
        ↓ principal state changed
    Phone builds Grant(X) = freightSpace(X)          (same encoder family:
        title, visibility, manifest, current epochs,  the freight is the
        local title)                                  proven precedent)
        ↓ sealed with HPKE to each certified, unrevoked sibling's
          X25519 (from its certificate), signed by the granting DEVICE
        ↓ put into the sibling's IDENTITY MAILBOX on the sibling's own
          relay (route book knows it — 1A's freight carries routes)
    Mac drains its identity mailbox in the ordinary pull
        ↓ verifies: signer is a certified, unrevoked device of MY OWN
          principal (cert chain; never a cross-principal grant)
        ↓ installs the space + keys; normal space sync begins;
          publishes its own cert/manifest into X (as first Open does)

The identity mailbox is one new hint namespace,
`H("qp-identity-plane-v0:" ‖ device ‖ bucket)`, drained alongside the
per-space caps in the ordinary pull. It is the continuation of the
freight by other means — sibling-to-sibling, sealed, signed, bounded.

**Delivery is HELD until OBSERVED (ADR-023).** The granting device
keeps `pendingGrants[space][sibling]` and re-offers each cycle (the
relay dedups identical bytes — 1A's re-offer idiom) until it OBSERVES
the sibling inside the space: the sibling's certificate or manifest
frame appearing in X's log is the acknowledgement, no ack message
needed. A sibling offline past the mailbox TTL loses nothing: the
grant re-offers until the observation happens. Revoked siblings are
skipped at send AND refused at receive (a grant signed by a device
revoked at its clock does not install).

### What deliberately does NOT converge

Device-local state (the right column above). And a sibling never
FORWARDS grants it received — grants originate only on the device that
performed the join, or the graph becomes a gossip protocol nobody
audits. If the granting device dies before any sibling observed the
space, the person still holds the space on the device that joined it;
re-pairing carries it in the freight as always — degraded, never lost.

## Security notes

- A grant is sealed to a certificate's key and signed by a certificate's
  key; both chains must terminate in the SAME principal's root. A relay
  operator sees opaque bytes in an opaque box.
- The identity mailbox accepts only what decrypts and verifies; junk is
  a recorded refusal (`refused_bad_grant`), not silence.
- Epoch expansion happens at owners, from certificates their own log
  admitted — a forged sibling would need a root signature, which is the
  same bar every frame already clears.
- Grants carry epoch KEYS a sibling is entitled to by the model
  (admission is per-principal; the freight ships the same keys today).
  The one thing this changes is honesty: today's freight hands them
  over unsigned and unattributable; a grant is both.

## Release gate (owner's, verbatim in the plan)

Phone joins/adds → Mac converges, and mirrored · the sibling may be
OFFLINE during the join and catches up after returning · both devices
read the current epoch · after the owner's RotateEpoch both read the
new epoch · a message from either sibling reaches the counterparty as
the SAME principal · after revoking one device it stops receiving new
grants and epochs while the other continues.

v0.1.5 ships 1A+1B together; the 1A commits are frozen as the proven
layer beneath this one.
