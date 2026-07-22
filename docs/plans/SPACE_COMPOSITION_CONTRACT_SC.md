# Quiet Spaces — Space Composition Contract (SC-wave, revision 2 — implementation baseline)

## Context

The Human Interface wave (UI-0 Shell, UI-1 First Run, UI-2 Space Pass) has
shipped; UI-3 Living Space is mid-flight. The user has introduced a **new
wave**: a space must become an *inhabitable place* — background, atmosphere,
a Shelf of kept media, a Wall-lite composition, graffiti, pinned messages,
bot/IoT objects — **without** the appearance being tied to one web client or
stored as a screenshot.

Core invariant (the whole reason for the wave):
> A space transmits **meaning, composition, and verifiable resources** — never
> the interface itself as executable code.

A space hands a receiving client a **signed, portable composition document**;
the client interprets it with its own trusted renderers and fetches media
lazily as content-addressed bundles. AI produces a *proposal* (never publishes
directly); manual edits stay authoritative over AI. Renderers may degrade; the
*meaning* of an object may not.

Per the spec's §34 ordering, this wave lands **after** UI-3, but the contract
foundation + renderer negotiation are laid down now. This plan covers **Part A:
finish + commit UI-3** and **Part B: Gate SC-0 (Contract Foundation)**. SC-1…SC-5
are sketched as roadmap only.

### Decisions locked (user) + refinements
- **Scope of this bite:** finish UI-3, then SC-0. (SC-1 progressive renderer is a later session.)
- **Asset identity:** content-hash id, but introduced as a **versioned ref** (not a 16→32 widening in place) — see B2. `asset_id = SHA256("qs.asset.v1" ‖ exact decrypted asset bytes)`; metadata/filename/MIME are **not** in the digest; each variant (original/preview/thumbnail/lqip) is its own asset, linked in the descriptor.
- **Snapshot depth:** signed snapshot as a **materialized projection** with a per-kind chain bound to a `projected_through_clock`. **The owner node** builds it via full replay; **a receiving foreign client does NOT replay** — it verifies the signed snapshot and renders from it. The "snapshot at clock N + delta" fast-bootstrap is deferred to a later gate. (Full-replay is a *producer-side* detail, never a precondition for a visitor.)

### Reuse map (from exploration — do not reinvent)
- **Signed declarative doc pattern** = the Terminal Manifest: `protocol/manifest/manifest.go` — append-only integer wire-key table, `Revision uint64` + `Previous *id.Hash`, `Sign(terminalPriv)` where `TerminalID == pubkey`, verbatim `SignedBytes` splice via `codec.ReadRawItem`, `Hash = id.HashOf`. Chain enforced in `kernel/registry/registry.go` (`Upsert`, `CapabilityDiff`). Bump idiom: `terminals/persist.go` `Rename` (`:197`).
- **Schema registry** = `protocol/schemas/schemas.go`: `Register(id, validator)` in `init()`, id form `<domain>.<type>.v<N>`, validator = a `Decode*` wrapper; unknown ids stay opaque (ADR-009). Block family convention: **CBOR key 1 is always `fallback_text`** (`blocks.go`) — the semantic-fallback seed already exists.
- **Canonical CBOR** = `protocol/codec`: `AppendMap`/`AppendUint`/`AppendText`/`AppendBytes`/`AppendArray`, ascending integer keys, `MapReader` + `SkipItem` on decode.
- **event_clock** = `signal.Envelope.LogicalClock` (Lamport, wire key 9); boundary accessor `Space.MaxClock()` (`terminals/persist.go:225`).
- **Assets** = `kernel/assets/assets.go`: streaming chunked XChaCha20 seal, per-chunk AAD. The existing plaintext streaming/hash **path** is reused to compute a **separate, domain-separated `AssetRefV2`**; the legacy `PlaintextDigest = SHA256(plaintext)` (`:310`) remains distinct and is **not** the new id. Blobs stored ciphertext-addressed under `sha256(ciphertext)` in `storage.PutBlob` (unchanged). Scoped authorization `node/assets_index.go` (`allow`/`allowed`, `ExtractAssetRefs`). Blob exchange `kernel/sync/sync.go` (`msgBlobReq`/`msgBlobData`, expected-only `acceptBlob`, resume/dedup). Progressive-delivery precedent: `node/relay.go` `collectBlobs` + `AssetPolicy` + budget + honest `ExportReport`.
- **UI seams** (`clients/web-ui/assets/`): `renderBody` hard-coded `switch(e.kind)` at `app.js:888`, `default` branch (`.unknownblk`, `:947`) = the fallback seam. `applyTheme` (`:42`) writes appearance to global `:root`. `PROTOCOL` flag + `live_signal` renderer (`renderSignal`, reduced-motion aware, text fallback) = the capability-profile precedent. Assets already id-referenced via `/api/spaces/{id}/assets/{id}` (`assetNote`, `:867`).

---

## Part A — Finish + commit UI-3 (clean the tree first)

UI-3 is uncommitted (`glyphs.js`, `app.js`). The living-glyph/presence code
exists but its CSS is missing, and motion/a11y/mobile are unstarted.

- **Motion stays quiet-by-default (refinement 5):** **no continuous breathe or idle pulse.** Animation is event-triggered only, then rests static:
  - presence change: `180–320 ms` transition;
  - new activity / real bot·IoT signal: `300–450 ms` (the ring animates only during an actual signal, not idle);
  - idle glyph: **static**. Any residual decorative motion (if kept at all) is very slow and low-amplitude — never an ambient loop.
- **CSS (styles.css):** `.presence-summary` (calm one-line), an `.enter` transition on new rows, and event-scoped classes for presence-change / signal that self-remove after the transition. All under `@media (prefers-reduced-motion: reduce)` → static.
- **Entry motion:** in `refreshSpace()`/`renderEntry`, track rendered entry ids per space; add `.enter` (fade + short rise, ≤450 ms) only to genuinely-new rows so the 2 s refresh doesn't re-animate the whole feed. Reduced-motion disables it.
- **A11y (§19):** `aria-label` on composer icon buttons, ensure `:focus-visible` ring coverage, Esc-to-close + focus-return already native to `<dialog>`; verify tab order and ≥40 px touch targets.
- **Mobile (§20):** a `@media (max-width:600px)` block — sidebar ↔ space as swapped screens, composer pinned bottom, members as a collapsible sheet, Pass dialog full-screen.
- Live-verify in the browser (two nodes already used for UI-2), then **commit UI-3** with the established per-gate message style. Mark task #22 complete.

---

## Part B — Gate SC-0: Contract Foundation

Goal (spec §30 SC-0): a *foreign* client can safely read and render a static
composition fixture with no media. Backend defines + signs + validates the
contracts; the client parses them through a renderer registry with semantic
fallback.

### B0. Baseline + ADR
- Copy this plan to `docs/plans/SPACE_COMPOSITION_CONTRACT_SC.md` (immutable baseline, mirroring how the UI wave pinned R3).
- Write **ADR-013 Space Composition Contract** pinning invariants: renderer degrades / meaning does not; no executable payload (no HTML/CSS/JS/SVG-script/iframe/remote-URL-as-truth); content-addressed asset refs; allowlisted renderer + effect grammar; snapshot is a bootstrap optimization, not independent history; AI proposes, never publishes; manual locks are authoritative.

### B1. Contract schemas (backend, `protocol/schemas/` + new `protocol/composition/`)
Two **revisioned signed snapshots** + one **immutable content-addressed manifest** +
its **revisioned index** (refinement 3: don't mix the two models):

**Revisioned snapshots** (manifest pattern; verbatim `SignedBytes`) — each carries the
formal chain header (refinement 5):
`{ space_id, document_kind, revision, previous_snapshot_hash, projected_through_clock, projection_hash, signature }`.
Chain invariants: `revision = previous.revision + 1`; `previous_snapshot_hash = hash(previous signed snapshot)`; `document_kind` and `space_id` immutable within a chain; `projected_through_clock` never decreases. Appearance and composition are **independent chains**.
- `space.appearance.snapshot.v1` — palette tokens, background (asset_ref + treatment: blur/dim/tint/grain/vignette in strict numeric ranges), ambient, overlays, motion policy, `locks{}`. **No object placement.**
- `space.composition.snapshot.v1` — `coordinate_system: normalized-2d.v1`, zones (kind + `renderer` + `fallback_renderer`), objects (`semantic_kind`, `source_event_id`/`source_asset_id`, `zone_id`, `renderer`, `fallback_renderer`, **required fallback metadata**, normalized transform `x,y,w,h∈[0,1]`, `rotation∈[-15°,15°]`, bounded `z`), `graffiti_preview_ref`, `bundle_index_ref`.

**Immutable + index** (refinements 2 + 3):
- `space.asset.bundle.v1` — **immutable, content-addressed, and NOT self-signed.** `bundle_id = SHA256("qs.bundle.v1" ‖ canonical-CBOR immutable body)`, where the hashed body **excludes** `bundle_id`, any signature, transport hints, and local cache metadata. **No** `revision`/`previous`. Entries `{asset_id, variant, required, encryption_epoch}` — epoch **per entry** (safer for future archive/editor bundles that span epochs), plus `kind` (core|viewport|interaction|background|editor|archive), `priority`, `estimated_bytes`, `dependencies`. New content ⇒ new `bundle_id`. Integrity comes from `bundle_id`; **authenticity comes from the signed index that references it** — no second signature system, no recursive fields.
- `space.bundle.index.snapshot.v1` — revisioned, **signed** index of the space's *current* bundles (same chain header as the snapshots), binding each `bundle_id` to this space. Referenced by composition's `bundle_index_ref`.

Register all in `schemas` `init()` with `Decode*` validators; keep **key 1 = fallback** convention on objects. Golden fixtures + canonical encode/decode round-trip tests (mirror `protocol/codec` test-vector style).

### B2. Asset identity — versioned ref with dual-read (`kernel/assets`, `schemas/blocks*`, `node`, API)
Refinements 1 + 2 — **do not** widen the existing 16-byte `AssetID` in place (append-only events already carry old handles):
- **Versioned ref as an explicit tagged union (refinement 3):** the ref carries `key 1: version`, `key 2: id` — **not** a length heuristic. `version 1 → id is exactly 16 bytes` (legacy random handle); `version 2 → id is exactly 32 bytes` (content digest); unknown version → preserve opaque or reject per context. The block decoder dual-reads V1/V2; **new events emit only V2**; API + `assets_index` accept both; old Visual/Audio/File blocks keep opening. Optional local `legacy_id → digest` table filled opportunistically when plaintext is available. (A future identity type is then a new `version`, no length guessing.)
- **Digest definition (exact, domain-separated — refinement 4):** `asset_id = SHA256("qs.asset.v1" ‖ exact decrypted asset bytes)`, filename/MIME/metadata **excluded**. This is **not** equal to the existing `PlaintextDigest = SHA256(plaintext)`; reuse the streaming digest *path* but seed a **separate** V2 hasher with the domain prefix before streaming the plaintext chunks (`h := sha256.New(); h.Write([]byte("qs.asset.v1")); …plaintext chunks`). Keep the legacy raw digest only for diagnostics/migration — it is not `AssetRefV2`. Identical bytes ⇒ identical `asset_id` (reference-level dedup).
- **Variant store:** each derived version is a **separate** content-addressed asset (`original→A, preview→B, thumbnail→C, lqip→D`); the descriptor records the links + `{media_type, byte_size, w, h, duration_ms, role}`. Keep inline LQIP for first paint; move preview/thumbnail toward addressable assets.
- **ADR privacy note (refinement 2):** content-hash ids mean identical plaintext yields identical ids, so an observer seeing ids across spaces can infer content matches; encrypted blobs with random keys stay distinct. Acceptable — ids live inside encrypted blocks (co-members only).
- **Out of scope (ADR):** convergent encryption for cross-space *byte-level* blob dedup (random per-asset keys ⇒ identical plaintext still encrypts differently). Reference-level dedup only for SC-0.

### B3. Snapshot projection + API (`node`)
- **Owner side:** materialize appearance/composition/bundle-index snapshots by projecting the space's log (full replay for now — a producer-side detail), sign with the space terminal key, stamp `projected_through_clock = MaxClock()`, extend the per-kind chain. Persist alongside the space; re-project on relevant events.
- **Visitor side (refinement 1):** a foreign client **never replays**. It verifies the signed snapshot **tip** and renders from it; full historical chain verification is optional and only when prior snapshots are held:
  - *Initial bootstrap* (no prior snapshot): verify terminal signature, verify `space_id`, **recompute `projection_hash`**, validate `document_kind` + `revision`. That is sufficient to render.
  - *Subsequent update* (a prior trusted snapshot exists): verify new signature, require `revision = local + 1`, `previous_snapshot_hash = hash(local signed snapshot)`, `projected_through_clock ≥ local`.
- `GET /api/spaces/{id}/appearance`, `GET …/composition`, `GET …/bundles` return the signed snapshots + bundle index. (Editing/graffiti/AI endpoints are SC-2/3/4.)

### B4. Client foundation (`clients/web-ui/assets/`) — the §34 hooks
- **Renderer registry:** replace the `renderBody` `switch` with a `RENDERERS` map keyed by renderer id → fn, plus a **semantic fallback chain** (`renderer → fallback_renderer → generic.<kind> → title/author/meta`). Unknown renderer ⇒ never execute, use fallback, surface "Unsupported presentation" only under `PROTOCOL`. Register existing renderers (text/visual/voice/audio/file/link/live_signal) into it — zero behaviour change, just indirection.
- **Capability profile:** a local `client.renderer.capabilities.v1` object (supported renderers/versions, features like backdrop_blur/animated_glyphs/vector_graffiti/audio/video, `performance_class`, `max_single_asset_bytes`, `max_scene_objects`) that drives Full/Reduced/Minimal degradation. Reuse the `prefers-reduced-*` + `PROTOCOL` precedents.
- **Appearance adapter (scoped):** apply appearance tokens to a **scoped space container**, not global `:root`, so appearance is per-space and can degrade to Minimal (opaque surfaces, tint instead of image).
- **Shelf route stub + Keep-in-space hook:** a fourth `<main>` region toggled like a route, and a `Keep in space` affordance on entries (extension point wired now, populated in SC-2).
- Read the static composition **fixture** through this pipeline and render it with no media (SC-0 done-criterion).

### B5. Validation — two levels (refinement 4)
Split contract-time (backend, provable) from render-time (client, after asset resolution):
- **`ValidateCompositionContract` (backend):** schema version, canonical encoding, signatures, hashes, references, coordinate/rotation/blur/density limits, allowlisted renderer + effect grammar, text sanitization, no executable payload (no HTML/CSS/JS/SVG-script/iframe/remote-URL-as-truth), presence of fallback metadata, epoch access. These are the *declared* constraints the producer can be held to.
- **`ValidateRenderedSpace` (client, runtime):** contrast ≥4.5 and readability can only be checked once assets resolve on a specific client/renderer. When readability is insufficient the client **auto-strengthens** — more `dim`, opaque content surface, local contrast scrim, or drop to Minimal. The contract proposes atmosphere; **the client keeps the last word on accessibility.**
- The client never trusts a renderer implementation from a space — only allowlisted ids.

---

## Roadmap (later gates, not this bite)
- **SC-1 Progressive Renderer:** core/viewport/interaction/background bundle policy, LQIP/skeleton render, lazy fetch, content-hash cache, Full/Reduced/Minimal live; snapshot+delta fast-bootstrap.
- **SC-2 Shelf & Wall-lite:** `Keep in space`, message-relic/music/image/video-poster/link/file cards, fallback grid/list.
- **SC-3 Manual Customization:** Quick/Advanced customize, object placement, crop/focal, renderer switch, graffiti v1 (vector strokes + preview), locks, revert.
- **SC-4 AI Composition:** `space.appearance.prompt.v1` → `space.appearance.proposal.v1` (patch + generation requests + validation report + preview), locked-path respect, stale-base rebase, accept ⇒ ordinary patch events.
- **SC-5 Living & Collaborative:** richer graffiti, suggestions inbox, curator workflow, object groups, wall clusters, viewport tiles, composition history, edit-mode presence.

## Execution order (user-approved)
1. Close and **separately commit UI-3**.
2. Write **ADR-013** + baseline copy.
3. Introduce **`AssetRefV2` with dual-read** (V1 still opens).
4. Implement **appearance/composition snapshots** (chain header + projection).
5. Implement **immutable bundle + revisioned bundle index**.
6. Add **backend validators + fixtures** (`ValidateCompositionContract`).
7. Move UI to the **renderer registry** + semantic fallback + capability profile.
8. Add the **scoped appearance adapter** (+ Shelf stub, Keep-in-space hook).
9. Verify one fixture in **Full / Reduced / Minimal / Textual**.

## Verification (split by layer — refinement 6)
- **Go (backend):** canonical encode/decode round-trip for all contracts; `AssetRefV1` + `AssetRefV2` tagged-union both decode, new events emit V2; snapshot signature + recomputed `projection_hash`, tamper rejected; chain invariants (`revision = prev+1`, `previous_snapshot_hash` links, `document_kind`/`space_id` immutable, `projected_through_clock` non-decreasing); immutable `bundle_id = SHA256("qs.bundle.v1"‖body)` changes when content changes and index authenticates it; `ValidateCompositionContract` rejects invalid transform (rotation > 15°, coord ∉ [0,1]), non-allowlisted renderer, executable payload, missing fallback; content-hash asset dedup (same bytes → same id).
- **Client (web UI):** renderer registry dispatch; unknown-renderer → semantic fallback chain; capability-profile degradation (Full/Reduced/Minimal); scoped appearance adapter (no `:root` bleed); `ValidateRenderedSpace` auto-strengthens on low contrast; semantic preservation across modes.
- **Integration (live, two nodes as in UI-2):** foreign-snapshot bootstrap renders **with no replay** and **no original media** — placeholders + fallback only; console clean. Same fixture across Full desktop / Reduced mobile / Minimal / textual-terminal preserves title, meaning, importance order, author, source-open, unavailable state.

## Definition of Done (SC-0 slice of spec §33)
Appearance, composition, and bundle **index** are signed declarative contracts
with no executable code; immutable bundle manifests are content-addressed and
authenticated through the signed index. A foreign client renders a fixture
without full event history (verifies the signed snapshot tip, no replay);
unknown renderers always have a fallback; content-addressed asset ids via the
versioned tagged-union ref; Full/Reduced/Minimal profiles work on one fixture;
`ValidateCompositionContract` rejects unsafe/non-allowlisted payloads and the
client keeps the last word on accessibility. (Progressive delivery, Shelf/Wall
population, manual edit, and AI are SC-1…SC-4.)
