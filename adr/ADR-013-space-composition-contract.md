# ADR-013: Space Composition Contract

- Status: accepted
- Date: 2026-07-22
- Relates to: ADR-003 (deterministic CBOR), ADR-005 (group encryption),
  ADR-006 (local storage / full replay), ADR-008 (honesty), ADR-009 (schema
  evolution), the Space Composition Contract plan
  (docs/plans/SPACE_COMPOSITION_CONTRACT_SC.md)

## Context

After the Human Interface wave a space is a conversation with atmosphere. The
next step is an *inhabitable place*: background, Shelf of kept media, a
Wall-lite composition, graffiti, pinned messages, bot/IoT objects. The naive
design ships a rendered screen (HTML/CSS, or a screenshot) from one space to
another. That couples appearance to a single web client, cannot travel to
mobile / mesh / textual terminals, and — fatally — would have one peer execute
UI authored by another.

## Decision

A space transmits **meaning, composition, and verifiable resources — never the
interface itself as executable code.** A space hands a receiving client a
**signed, portable declarative document**; the client interprets it with its
own trusted renderers and fetches media lazily as content-addressed bundles.

> Block event = meaning · Asset = data · Renderer = presentation ·
> Composition = arrangement · Bundle = delivery unit · Client = trusted
> interpreter.

### Invariants (binding on all implementations)

1. **Renderer may degrade; meaning may not.** Every object carries
   `semantic_kind`, a `fallback_renderer`, and fallback metadata, so an unknown
   or unsupported renderer still shows title/author/type — never a blank.
2. **No executable payload, ever.** The contract carries no HTML, CSS, JS,
   script-bearing SVG, iframe, remote URL as source-of-truth, dynamic import,
   shader, or unbounded animation. Only allowlisted renderer ids + effect ids
   with bounded numeric parameters, content-addressed asset refs, sanitized
   text, and normalized transforms. A client never trusts a renderer
   *implementation* from a space — only allowlisted ids it already ships.
3. **Assets are content-addressed.** `asset_id = SHA256("qs.asset.v1" ‖ exact
   decrypted asset bytes)` (filename/MIME/metadata excluded). Each derived
   variant (original/preview/thumbnail/lqip) is its own asset. Refs are a
   versioned tagged union: `version 1` = legacy 16-byte random handle,
   `version 2` = 32-byte content digest; unknown version is preserved opaque
   or rejected per context — never disambiguated by length.
   - *Privacy trade-off (accepted):* identical plaintext yields identical
     `asset_id`, so an observer who sees ids across spaces can infer content
     matches. Ids live only inside encrypted blocks (co-members), and the
     stored blobs — encrypted under random per-asset keys — remain distinct.
   - *Out of scope:* convergent encryption for cross-space *byte-level* blob
     dedup. SC-0 dedups references only.
4. **A snapshot is a bootstrap optimization, not independent history.** The
   append-only event log stays the source of truth. The **owner** node
   materializes a snapshot by projecting its log (full replay is a
   producer-side detail). A **receiving foreign client never replays** — it
   verifies the signed snapshot *tip* and renders from it. Full historical
   chain verification is optional, only when prior snapshots are held.
5. **Two-level validation.** `ValidateCompositionContract` (backend, provable:
   schema/signature/hashes/limits/allowlists/no-executable/fallback-present) is
   distinct from `ValidateRenderedSpace` (client, runtime: contrast and
   readability after assets resolve). The contract proposes atmosphere; **the
   client keeps the last word on accessibility** and may auto-strengthen dim /
   opacity / scrim or drop to Minimal.
6. **Appearance/composition are revisioned signed chains; bundles are
   immutable.** Snapshots carry
   `{space_id, document_kind, revision, previous_snapshot_hash,
   projected_through_clock, projection_hash, signature}` with invariants
   `revision = previous.revision + 1`, `previous_snapshot_hash = hash(previous
   signed snapshot)`, `document_kind`/`space_id` immutable within a chain,
   `projected_through_clock` non-decreasing. An `space.asset.bundle.v1`
   manifest is **immutable, content-addressed, and NOT self-signed**:
   `bundle_id = SHA256("qs.bundle.v1" ‖ canonical body)` (excluding
   `bundle_id`, signature, transport hints, cache metadata); integrity from the
   id, **authenticity from the signed `space.bundle.index.snapshot.v1`** that
   references it — no second signature system.
7. **AI proposes, never publishes.** AI produces a validated *proposal*; only a
   user-accepted proposal becomes ordinary patch events. Manual edits and
   locked paths are authoritative over AI output. (SC-4.)

### Reuse (not normative)

The contract documents follow the existing **Terminal Manifest** pattern
(`protocol/manifest`): canonical CBOR with an append-only integer wire-key
table, `Revision`/`Previous`-style chaining, terminal-key `Sign` with verbatim
`SignedBytes` splicing, registered in `protocol/schemas`. `event_clock` is the
Lamport `signal.Envelope.LogicalClock` (`Space.MaxClock()` boundary). Delivery
reuses the content-addressed blob store, scoped `BlobAllowed`, and the
`collectBlobs`/`AssetPolicy`/budget progressive precedent.

## Consequences

- A visitor can render a foreign space as a safe *semantic document* even when
  media is absent, the renderer is unknown, or the client is too weak for the
  original composition.
- One fixture renders across Full desktop / Reduced mobile / Minimal / textual
  terminal while preserving title, meaning, importance order, author,
  source-open, and unavailable state.
- Cost: an owner-side projection step and a new family of signed documents;
  cross-space byte-level asset dedup is deferred. These are accepted for the
  portability and safety the contract buys.
