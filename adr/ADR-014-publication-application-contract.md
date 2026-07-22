# ADR-014: Publication & Application Contract

- Status: accepted
- Date: 2026-07-22
- Relates to: ADR-009 (schema evolution), ADR-013 (space composition), the
  Publications & Apps plan (docs/plans/PUBLICATIONS_APPS_WAVE.md)

## Context

A Quiet Space is a conversation with atmosphere (ADR-013). The product needs a
second central scenario: **running a publication surface** — blog, journal,
label page, research log — and, beyond static posts, **living blocks**: polls,
forms, sensor views, small applications. The naive designs both fail the same
way: a post as rendered markup couples content to one client, and an app as
shipped code has one peer executing another's program.

## Decision

Message, post, sensor, bot and mini-app are **different compositions of one
model**: signed events, content-addressed assets, declarative blocks, and
client-chosen renderers. Two contracts share the space:

> **Appearance Contract** — atmosphere and presentation (ADR-013).
> **Content/Application Contract** — publications, blocks, data, behavior.

```
PublicationDocument   ordered bounded block tree · stable document_id ·
                      signed revisions · optimistic concurrency
AppDefinition         declarative logic · REQUESTED capabilities · immutable per revision
AppInstance           unique instance_id · PINNED definition revision ·
                      props + scope · GRANTED capabilities · own state partition
AppStateEvent         an ordinary domain event, partitioned by instance_id
SpaceObject           a visual reference to a document/app; never owns their state
```

### Invariants (binding on all implementations)

1. **A block describes meaning, not markup.** Bounded, allowlisted block types
   with typed props; no HTML/CSS/JS travels in a document. The client picks
   the renderer; unknown presentations degrade, meaning survives.
2. **A document is a bounded ordered block tree** (unique block ids, bounded
   depth/children/count/text/size; asset refs must resolve in the space).
   Graph IR may appear later as an internal compiled form — it is not the
   authoring or wire model.
3. **Forward compatibility by opaque blocks.** The *authoring* validator
   rejects unknown block types; the *decoder* preserves them (type + raw
   props/children) so one newer block never makes a document unreadable.
4. **Revisions are signed events; the author is the signer.** A revision event
   carries the full document plus `previous_revision_event_id`. The API
   enforces optimistic concurrency via `base_revision_event_id` (conflict →
   409, draft preserved); the reducer converges raced events by deterministic
   LWW `(LogicalClock, EventID)`. The document's authors field is display
   metadata only.
5. **Drafts are local** and sealed at rest; nothing enters the log until the
   user publishes.
6. **Archive is reversible only explicitly**: a stale revision never
   resurrects an archived document; a later `publication.restored.v1`
   referencing the archived state does.
7. **Comments are thread-shaped** ({comment_id, parent_comment_id?}) and
   **reaction targets are stable ids** (`publication:{document_id}`,
   `comment:{comment_id}`) — never revision event ids.
8. **Publication ≠ Space Object.** Placing a post on a Wall/Shelf is a
   reference; moving it never edits the publication.
9. **Capabilities are default-deny and requested ≠ granted.**
   `effective = requested ∩ granted ∩ memberPermissions ∩ nodePolicy`; grants
   live in the instance event; the node enforces the intersection on every
   input read and action emit. No free network, no raw log access.
10. **App state is ordinary domain events partitioned by `instance_id`.**
    Apps emit only registered schemas declared in their effective
    capabilities, through the node's action endpoint (which injects
    `instance_id`); there is no client-side free emit and no general
    schema-wide query — inputs are served per instance with node-applied
    scoping filters.
11. **Definitions referenced by publications are pinned** to an exact
    revision; a published post never changes behavior because its app's
    definition moved.
12. **Ephemeral streams never have to hit the log.** Live Signal remains the
    transport primitive under future live blocks; durable results are explicit
    events.
13. **AI proposes, the user publishes** — same as ADR-013 invariant 7.

## Consequences

- One document renders as feed card, article, shelf item, terminal view, or
  (PUB-2) a public page — because it stores meaning, not layout.
- A poll embedded in two posts, or twice in one post, can never mix votes.
- A malicious definition requesting broad capabilities gains nothing without a
  grant; a compromised client cannot emit outside the manifest because the
  node is the emit path.
- Cost: full-document revisions (no diffs) and per-instance queries are
  heavier than raw access — accepted for provenance and isolation. Public
  gateway exposure, live ephemeral runtime, WASM capsules, and third-party
  block registries are explicitly later waves.
