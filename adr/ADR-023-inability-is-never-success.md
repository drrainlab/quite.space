# ADR-023 — Inability is never success, and never silence

Status: accepted (2026-08-20)
Owner's formulation, verbatim, which this ADR elevates from a week's
lesson to doctrine:

> **Quiet Spaces may be temporarily unable to deliver something. It must
> never confuse inability with success or silence.**

## Context

One week of public beta produced a set of bugs that looked unrelated —
a relay that never hung up on its clients, a gateway terminal that
mailed a letter to itself and called it delivered, a zero-knowledge
bootstrap that recorded its own guess as the peer's stated route and
advanced the delivery cursor, a media answer dropped by a bare `return`
when no route was known. Every one of them was the same shape: **a path
that stayed silent or reported success where every neighbouring path
speaks.** Each survived precisely because the system's own diagnostics
read healthy while it failed; the reporter's screenshot was the first
instrument to see it.

The forensic record is `docs/plans/BETA_AUDIT_2026-08-20.md` and the
commits of the `stream-1-media-routing` branch. The measured convictions:

- `relayserver`: stopping closed the listener and abandoned live
  connections to the GC — every in-flight request sat out its full
  deadline over a socket that was merely quiet. Measured at 5.000s for
  a probe; fixed by hanging up (057b9d8).
- `deliverSpace`'s legacy bootstrap: transport acceptance at the
  sender's OWN relay was recorded as delivery to the intended
  recipient, and the guess written into the route book outlived the
  mistake (25ea86e).
- `answerWantsRouted`: a holder that WANTED to answer and had no route
  said nothing — no counter, no held state, no log line (41a8a08).

## Decision

1. **Three verdicts, never two.** Every operation that moves something
   on behalf of a person resolves to exactly one of:
   - **DONE** — the intended effect is known to have happened;
   - **HELD** — the system is *currently unable* and still intends:
     the state is recorded, visible in diagnostics, and retried by
     construction (a re-offer loop, a re-riding want, an invalidation
     hook) rather than by hope;
   - **REFUSED** — terminal, with a named reason (`not_found`,
     `not_authorized`, `revoked`, `unsupported`, …); waiting changes
     nothing and nothing will retry.
   Conflating HELD with REFUSED ships silent drops; conflating HELD
   with DONE ships false success. Both are architecture bugs, not UX
   polish.

2. **Acceptance is not delivery.** No acknowledgement from a transport,
   relay, or store may advance an accounting cursor unless the endpoint
   is one the recipient has STATED. A guess may be attempted — putting
   a copy where the recipient *might* look is often free and sometimes
   right — but a guess is HELD until knowledge confirms it, and a guess
   is never recorded as the peer's knowledge.

3. **Stronger knowledge invalidates weaker delivery.** When a claim the
   system acted on is displaced by a stronger one (routeRank, cert over
   allowlist, statement over guess), work concluded on the weaker basis
   becomes eligible again — by re-offering from a held or reset cursor,
   idempotent by construction (EventID/content dedup), never by
   compensating arithmetic.

4. **Every held state has a face.** If the node knows why something has
   not happened, that reason exists as data a screen can repeat
   honestly: `Held` reasons on spaces, `WantHolds` on media answers,
   ingress refusals, custody holds. "The relay light is green and the
   post went nowhere" must be unconstructible.

## Applications (existing and this wave)

relay custody receipts (accepted_by_relay ≠ delivered, ADR-007) ·
ingress HOLD vs refusal (ADR-022: temporary failure is never converted
into permanent refusal) · RT-0's held-not-reaimed routing (ADR-020) ·
`heldNoRecipient` / `heldNoRoute` / `heldTentative` · `WantHold`
(`held_no_route`, transient) · legacy-basis re-offer on route
displacement · the relay hanging up on stop · offline/radio scenarios
to come, where inability is the ordinary state and must remain a
first-class, visible, retryable fact.

## Consequences

New code paths that can fail to move something must name their held
state before they merge; a bare `return` on an inability is a review
blocker. Diagnostics grow monotonically richer, and some screens now
show honest waits where they used to show nothing — that is the point.
The cost is a little accounting; the alternative was measured, twice,
in a beta week.
