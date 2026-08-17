# ADR-022: Ingress custody and admission verdicts

Status: accepted (2026-08-17, MD-0b/MD-0c)

## Context

Enforcing device certification (ADR-002) at the log's door exposed a missing
stage between transport and admission. The relay's `Collect` is destructive:
once it yields an item, the relay has forgotten it while the sender counts it
delivered — so a frame refused for a prerequisite that had not arrived yet
(its author's certificate, one round behind) was not delayed but destroyed.
The same shape then surfaced twice more where nobody had named it: a radio
receiver remembers a completed transfer and never re-delivers it upward, and
an owner-side rejection cache remembered temporary refusals as permanent
verdicts. Destructiveness is a semantic property of a path, not of one API.

## Decision

**The custody law.** `transport custody → LOCAL DURABLE CUSTODY → journal
custody`. A node MUST NOT destructively take custody of more ingress than it
can durably retain until admission reaches a terminal state. For every
destructive response, ALL returned items are persisted before any is judged.
The hold is released only when the next layer durably owns the bytes
(`Log.Has(EventID)`, never a nil error) or the refusal is terminal.

**Verdicts, not errors.** Admission answers `ADMIT / HOLD / REJECT`, and the
criterion outranks any list: REJECT means no subsequent valid control event
can make these exact signed bytes admissible; HOLD means such an event can.
Certificate lag, policy lag, membership lag and a frozen space are all HOLD —
each measured, not assumed. Chain forks, forged signatures, principal
mismatches and revocation are REJECT. `revoked` is never HOLD.

> **A temporary admission failure must never be converted by transport,
> deduplication, or caching into permanent refusal.**

Cacheability follows from the verdict, never from the caller's judgement:
only terminal rejections may enter a negative cache, and the cache API
enforces this itself.

**Reconsideration.** `admission prerequisite changed → reconsider held
ingress`, hooked to successfully APPLIED state (never to the command that
attempted the change), coalesced, with a mandatory startup pass after all
durable state is reconstructed.

**The bootstrap seam (decision C).** A device certificate is one root-signed
object in two roles: an ADMISSION PROOF — plaintext beside the epoch
precedent of ADR-005, learned at the log's door and in a pre-pass over each
received batch, free of chain ordering — and a LOG RECORD
(`identity.device_certified.v1`), applied in ordinary order for convergence
and audit. Chain position is canonical tidiness, never what the security
model hangs from. The proof is visible to the intended peer's admission
before semantic admission and requires nothing from a relay: it adds no
identity linkage that envelope headers did not already show.

**Named limits.** Capacity is admission control BEFORE destructive custody
(a pre-collect threshold, not an on-disk maximum; overshoot is reported,
never trimmed; age is a diagnostic, never a deletion). If a destructive
transfer succeeds and local durable storage then fails, that is
`ErrIngressCustodyLost` — a catastrophic local failure that halts further
destructive collection; it prevents additional loss and cannot recover the
item, and it must never be presented as recoverable.

## Consequences

- The relay stays blind (ADR-016): no ACKs, no re-custody, no protocol
  awareness — the fix is local by construction.
- Transports need no common retry machinery; the kernel only needs to know
  when upstream custody has factually ended.
- Policy admission remains projection-relative (deferred): later policy
  state may make previously refused signed bytes admissible. Event-time
  authorization is its own future decision.
- The gate (`identityGate`) is on. Enforcement shipped in the same change
  that made it satisfiable on every path.
