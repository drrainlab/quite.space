# Quiet Spaces — Publications & Application Blocks (PUB/APP-wave, revision 2 — implementation baseline)

## Context

The SC + PUI waves made a space an inhabitable, themable place. This wave
elevates **running a publication surface** to a central scenario and adds a
second fundamental contract next to Appearance:

> **Appearance Contract** — атмосфера и представление.
> **Content/Application Contract** — публикации, блоки, данные и поведение.

One space = three projections of one event log: **Conversation**,
**Publication**, **Application Surface**. A post is a **versioned ordered
block tree** (not markdown-with-attachments, and *not* a graph in M1); a block
describes *meaning, data, capabilities and behavior* — the client picks the
renderer. Apps are **declarative** (Block IR), **capability-scoped,
default-deny**; shared state is built from ordinary signed domain events;
Live Signal remains the low-level transport under future live blocks.

### Decisions locked (user)
- **Scope: PUB-0 + PUB-1 + APP-0 + APP-1** (each gate = commit + live check).
- **Standalone public web page — deferred to PUB-2.** This wave delivers an
  **in-space publication surface, ready for public gateway exposure in PUB-2**
  — the DoD is worded exactly that way. Visibility field is `visibility_intent`
  with values `space | public-intent` so the UI never claims internet reach.
- **AI post composition — in**, as a proposal (PUI-4 pipeline); never
  auto-publish.
- Deferred: WASM capsules, third-party registry, live ephemeral runtime
  (APP-2), moderation/collections/series-nav, import/export, co-author
  workflow, drag-and-drop reorder, bot-interaction & generative-canvas blocks,
  real video assets.

### Core model (revision-2 corrections folded in)

```
PublicationDocument   ordered bounded block tree · stable document_id ·
                      signed revisions · optimistic concurrency · renderer-independent
AppDefinition         declarative logic · REQUESTED capabilities ·
                      versioned, immutable per revision (a log event)
AppInstance           unique instance_id · PINNED definition revision ·
                      props + scope · GRANTED capabilities · independent state partition
AppStateEvent         ordinary domain event (poll.vote…), partitioned by instance_id
SpaceObject           visual reference to a document/app; never owns their state
```

### Architecture calls (settled)
- **Documents are ordinary device-signed events** through `Self.Emit` →
  eventlog → reducers. Revision events carry the full document.
- **Authorship comes from the signature**: the author is the principal of the
  signing envelope of `publication.published.v1`. The document's authors field
  is display metadata only, never the source of truth. (Co-authors later via a
  consent event `publication.authorship.accepted.v1` — reserved, not built.)
- **Optimistic concurrency (correction 7):** publish/revise carries
  `base_revision_event_id`; the node compares it to the current projection tip
  and returns a **conflict** (draft preserved) on mismatch. The revision event
  also stores `previous_revision_event_id`. The reducer still converges by
  deterministic LWW `(LogicalClock, EventID)` for events that raced offline —
  but the normal API path can no longer silently fork.
- **Archive is reversible (correction 8):** reserve
  `publication.restored.v1` now. Reducer: a stale revision after archive never
  resurrects; a *later explicit restore* referencing the archived revision
  does.
- **Unknown blocks: two levels (correction 6).** The *authoring* validator
  rejects unknown block types. The *protocol decoder* preserves an unknown
  type as an **opaque block** (type + raw props + raw children) so a document
  with one newer block still opens everywhere; the renderer shows the fallback
  node. Known block codecs interpret contents after generic decode.
- **Drafts are local & sealed:** `drafts/{space_id}/{document_id}.sealed`
  (atomic tmp+rename, max draft size, safe path construction, index rebuilt by
  scan), sealed with the keystore machinery via new generic
  `storage.Root.SaveSealed/LoadSealed/ListSealed`.
- **Publication ≠ Space Object** (hook only; SC-2 owns placement).
- **Comments are thread-shaped from day one (correction 9):**
  `{comment_id, document_id, parent_comment_id?, text}` — flat rendering in
  v1, but no future migration. `publication.comment.edited.v1` / `.removed.v1`
  reserved (not built). **Reaction targets are stable ids** —
  `publication:{document_id}` / `comment:{comment_id}` — never revision event
  ids, so reactions survive edits.
- **`video` → `video-link` (correction 12):** poster asset + external
  URL/reference; a real `video` block waits for a video asset pipeline.
- **Forms honesty:** APP-1 form responses are encrypted to the space, so they
  are readable by all members. No `author_only` promise until selective
  encryption exists — the form block says so in its UI copy.

### Reuse map
- Canonical CBOR + registry + "key 1 = fallback_text": `protocol/codec`,
  `protocol/schemas`. Bounded-grammar validation style:
  `protocol/composition/validate.go` (mirrored, no import cycle).
- AssetRefV2 public ids + lazy fetch: `schemas.AssetRef.PublicIDHex`,
  `/assets/{id}`, `assetNote`. Entries reducer + LWW + tombstones + reactions:
  `kernel/reducers`, `schemas.ReactionBlock`.
- Renderer registry + fallback chain: `renderers.js`; Apple kit + sheets
  (PUI-1). LLM proposal pipeline + stub-provider tests: `node/llm`,
  `node/aicompose.go`. Emit path: `Runtime.EmitBlock`, `node/api_blocks.go`.

---

## Gate PUB-0 — Document Foundation (backend)

- **ADR-014 Publication & Application Contract** pinning: block = meaning not
  markup; document = bounded ordered block **tree** (graph IR may appear later
  as an internal compiled form — explicitly out of scope now); drafts local
  until publish; author = signer; optimistic concurrency at the API; archive
  reversible via explicit restore; opaque-block forward compatibility;
  publication ≠ space object; capability default-deny with
  requested ∩ granted; state = domain events partitioned by instance_id; AI
  proposes, user publishes. Baseline copy → `docs/plans/PUBLICATIONS_APPS_WAVE.md`.
- **`protocol/publication`**: `Document` {document_id [16]B, kind, title,
  summary, cover ref, display_authors, tags, layout preset, discussion mode,
  `visibility_intent (space|public-intent)`, `Blocks []Block`}.
  `Block` = {id, type, props, children} decoded generically (raw props/children
  kept for unknown types). Allowlisted types: content (heading, text, quote,
  image, gallery, audio, **video-link**, file, link, code, callout, separator,
  credits) + composition (stack, columns, hero, section) + **`app` (reserved
  now — always renders fallback until APP-1**, so the grammar never changes
  silently between gates).
  **Validator invariants:** unique block ids; depth ≤ 6; direct children ≤ 64;
  total blocks ≤ 512; per-field AND total text budget; total serialized size
  cap; asset refs hex-valid and **resolvable in this space's asset index**.
- **Schemas**: `publication.published.v1` (payload = full document; key 1
  fallback = title; carries `base_revision_event_id`/`previous_revision_event_id`
  where applicable), `publication.revised.v1`, `publication.archived.v1`,
  `publication.restored.v1` (reserved+registered), `publication.comment.v1`
  {comment_id, document_id, parent_comment_id?, text}.
- **Reducer**: `Publications()` — latest revision per document_id by
  `(clock, eid)`; archive tombstone semantics with explicit-restore
  resurrection; per-document threaded comment list; author from envelope.
- **Drafts** (node): sealed per-document files as above;
  `SaveDraft/ListDrafts/DeleteDraft`.
- **API**: `GET /api/spaces/{id}/publications`, `GET …/publications/{doc}`,
  `POST …/publications` {document, base_revision_event_id?} → 409 conflict on
  stale base (draft kept), `POST …/publications/{doc}/archive` + `/restore`,
  `GET/POST/DELETE …/drafts`, `POST …/publications/{doc}/comments`.
- **Tests**: round-trip incl. opaque unknown block survival; validator
  rejections (unknown type at authoring, dup ids, over-depth/children/size,
  foreign asset ref); revision LWW + archive/restore semantics; **conflict on
  stale base_revision_event_id, draft preserved**; sealed drafts (no cleartext,
  restart-safe, path-safety); threaded comment projection; author-from-signature.

## Gate PUB-1 — Authoring & Rendering (client + AI)

- **Posts view**: `Chat | Posts` segmented switch in the conversation header;
  feed cards (cover, title, summary, author-from-signature, time, comment
  count) → full article (block renderers) + threaded comments (flat render v1)
  + reactions targeting `publication:{document_id}`.
- **Block renderers** in the `RENDERERS` registry, textContent-only for user
  strings; media via `assetNote` lazy fetch; unknown/opaque block → fallback
  ("Unsupported presentation" under PROTOCOL only). Document-level renderers:
  feed card + full article.
- **Composer**: full-height sheet — title/summary/cover, add-block menu,
  per-block editors, **reorder through up/down controls (drag-and-drop
  deferred)**, Save draft / Preview (same registry) / Publish (sends
  `base_revision_event_id`; a 409 surfaces "the post changed elsewhere — your
  draft is safe" with a reload-and-merge-by-hand path).
- **AI draft proposal**: constrained system prompt enumerating the block
  grammar → JSON only → parse → `publication.Validate` → **draft**; "✨ Draft
  with AI" fills the composer; user edits and publishes. Stub-provider tests.
- **Verify** (Go + live two nodes over relay): compose→publish→sync→render;
  comment round-trip; edit conflict path; unknown-block doc degrades without
  discarding the document; AI draft valid→draft / violation→rejected; console
  clean.

## Gate APP-0 — Declarative Runtime (definition + instance + grants)

- **`protocol/appdef`** — TWO artifacts (correction 1):
  - `app.definition.v1` (log event; immutable per revision): `inputs`
    (schema + **`where` equality filter on an indexed field whose value comes
    from instance scope** + since-cursor + server limit), `state` (allowlisted
    reducers `latest | ring(size) | count | list(limit) | sum(field)`),
    `view` (block tree + `metric-row`, `chart`, `event-list`,
    `action-button`), `actions` (emit of a registered schema with a bounded
    data template — **instance_id auto-injected**), **`requested_capabilities`**,
    `fallback`. Expressions = `$state.path` / `$instance.props.x` /
    `$instance.id` only — no code.
  - `app.instance.created.v1` (log event): `{instance_id [16]B,
    definition_ref: {app_id, revision_event_id} — PINNED (correction 2; a
    revisionPolicy field is reserved but only "pinned" is legal in v1),
    scope {space, document_id?, block_id?}, props,
    granted_capabilities}`.
- **Capability model (correction 3):** requested ≠ granted.
  `effective = requested ∩ granted ∩ memberPermissions ∩ nodePolicy`.
  M1 rules: built-in trusted templates get fixed grants; creating a custom
  app/instance requires the space owner (manage_apps analog = controller);
  read/write schemas checked against a node-side allowlist; grants recorded in
  the instance event; the node enforces the intersection on every input read
  and action emit.
- **State partition (correction 1):** every app state event carries
  `instance_id`; projections fold per instance. Two instances of one poll
  definition never share votes.
- **Node**: NO general events endpoint (correction 4). Instead:
  `GET /api/spaces/{id}/apps/{instance}/inputs/{input}` — the node loads the
  pinned definition + instance grants, applies the `where` filter and limit
  itself; the client cannot substitute schema or scope.
  `POST …/apps/{instance}/actions/{name}` — the only emit path: node
  re-validates action ⊆ requested ∩ granted, fills the template, injects
  `instance_id`, emits. `POST …/apps` (create instance, owner-gated),
  `GET …/apps` via a new **`Apps()` reducer projection** (definition +
  instance revision/tombstone semantics mirroring Publications).
- **Tests**: definition/instance round-trip; validator (undeclared capability,
  action outside requested, unknown reducer, where-field not allowlisted →
  reject); **grant enforcement** (requested-but-not-granted refused);
  **partition isolation** (two instances of one definition, events fold
  separately); pinned-revision resolution; deterministic reducer folds;
  non-owner instance creation refused.

## Gate APP-1 — Interactive Blocks

- **Schemas**: `poll.vote.cast.v1` {instance_id, option} — LWW per principal
  (revote replaces); `form.response.submitted.v1` {instance_id, fields
  (bounded)}; sensor demos reuse `observation.temperature.v1` with
  `where: sensor_id` scoping (correction 4 applies to sensors too).
- **Built-in trusted templates** (expressible in the APP-0 grammar, fixed
  grants): **poll**, **form** (with the members-can-read honesty note),
  **dynamic event list**, **sensor value + chart**. Local UI state (selected
  option, chart window) stays in-memory, never synced.
- **Embedding**: the reserved `app` block now renders: it references
  `instance_id`; publishing a post with a poll = create instance (pinned to
  the definition revision) + embed its id.
- **Verify** (Go + live two nodes): Alice publishes a post with an embedded
  poll → Bob votes → Alice's counts update via sync; revote replaces; a second
  poll instance of the same definition stays isolated; form submit lands as a
  signed event; hand-POSTing a foreign schema through the action route is
  refused; console clean.

## Verification (whole wave)
- Full Go suite green (existing packages + `protocol/publication`,
  `protocol/appdef`).
- Live, two nodes over the relay at every gate: publish → sync → read →
  comment → vote loop; conflict path; AI draft via local stub; opaque-block
  forward-compat; mobile + presets unaffected; console clean.

## Definition of Done (this wave's slice)
A space owner can run an **in-space publication surface, ready for public
gateway exposure in PUB-2**: compose a bounded block-tree post (by hand or
from an AI proposal), publish it as a signed revision with optimistic
concurrency, have members read it as feed card/article, comment (threaded
model) and react against stable targets; define **pinned, capability-granted**
app instances (poll/form/list/sensor) whose shared state is ordinary signed
events partitioned by instance; nothing executes foreign code, nothing
publishes without the user, unknown blocks degrade without discarding
documents, and every payload passes a bounded validator.
