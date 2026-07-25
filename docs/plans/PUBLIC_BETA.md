# Public Beta checklist (PA wave)

The PA wave (public/private access) is the last gate before a public beta.
This is the operator's go-live checklist. Reference: `adr/ADR-016`.

## Shipped (code complete, tested)

- [x] **PA-0** — visibility tiers (private / unlisted / public), broadcast +
      open community, signed checkpoints (`PublicProjectionEnvelope.v1`,
      I1–I9), ingress join/publish, text + media custody, UI, live-verified.
- [x] **PA-1.1** — policy revisions (`ReviseManifest`), revision-aware
      install with anti-rollback, TRUE freeze, curator add/remove
      (device-scoped), determinism rule after revisions.
- [x] **PA-1.2** — federated catalog = broadcast space of `kind:"space"`
      cards; Discover; `qs:` share-link scheme; catalog override in Settings.
- [x] **PA-1.3** — ingress token buckets + pending caps, rejected-frame
      LRU+TTL, per-public-space monitoring in Protocol view, honest
      irrevocability warning, private-contract regression.

## Regression contract (must stay green)

- [x] `go test ./...` green (node LAN discovery test is timing-sensitive
      under full-suite parallelism — re-run `go test ./node/` alone if it
      flakes).
- [x] Private spaces, invites, and Space Pass UX byte-identical to pre-PA.
- [x] A private space writes NOTHING into any public mailbox
      (`TestPrivateSpaceNeverTouchesPublicMailboxes`).

## Operator go-live steps

1. [ ] Stand up the production relay (`cmd/terminal-relay`), note its
       `host:port`.
2. [ ] On the operator node, create the seed spaces through the normal
       wizard (all public):
   - [ ] **Welcome to Quiet Spaces** — broadcast: what it is, how to
         install, changelog, honest guarantees.
   - [ ] **Listening Room** — broadcast studio space with a listening
         session (first pine[vibes] space).
   - [ ] **Quiet Commons** — open community, the living proof of the mode.
3. [ ] Create the **Official Catalog** (public broadcast). Post one
       space-card per seed space (composer → kind "space card" → paste each
       space's share link) plus a "what is this" post.
4. [ ] Copy the Catalog's share link and set it as `defaultCatalog` in
       `clients/web-ui/assets/app.js` (currently empty → Discover falls back
       to the Settings override until then).
5. [ ] Rebuild; confirm a FRESH node's **Discover** opens the catalog and
       every card's **Open space** reaches its target.

## Live drills (done in dev, repeat on prod relay)

- [x] Discover → open catalog → read space-card → Open space → read target.
- [x] Community: reader → Join to write → post → owner materializes via
      ingress → projection returns to all readers.
- [x] Curator add via revision → the new device publishes immediately.
- [x] Freeze drill: owner's own write refused while frozen; unfreeze
      restores writes and pending delivery; content preserved.
- [x] Monitoring: `/api/relay/status.public[]` shows per-space role, seq,
      publish age, frozen, ignored counts.

## Accepted risks (documented, not blockers)

See `adr/ADR-016` R1–R6: irrevocable links, id squatting, media-fetch
device exposure, freeze-is-not-recall, single-device publisher, manual
catalog curation.

## Explicitly deferred (post-beta waves)

Open catalog submission queue; server-side search; confirm-join; automatic
device-certificate distribution + multi-device curators; mirrors + full
history; two-layer spaces; private↔public transitions.
