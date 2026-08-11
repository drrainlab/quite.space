# Pre-beta security audit — can anything reach the user's device?

Read-only review, 2026-08-11. **Nothing was fixed** — this file is the
record. The question asked was narrow and practical: before the beta goes
to friends, can a remote party (peer, relay, mirror, bridge, radio
neighbour, or a stranger whose link somebody pasted) get something onto
the user's device that the user did not consent to, or crash/exhaust it
from the outside.

## Scope actually covered

Three classes were audited in depth. Naming what was NOT covered matters
as much as the findings, because an absent finding in an unaudited area
is not a clean result:

| audited | not audited (this pass) |
|---|---|
| untrusted wire parsing — codec, envelope, relay wire, projection, publication, pass/quicklink, radio transfer, bundle | the web UI's own XSS surface (innerHTML sinks in the JS client) |
| asset / blob / media handling — integrity, traversal, serve headers, exhaustion, decode surface | the local HTTP API's auth and CSRF posture (token in query strings, Origin checks) |
| consent & impersonation at the protocol edges — silent join, relay adoption, draining, provenance, LocalOnly, attention | the Wails/desktop shell boundary (not yet built), and dependency/supply-chain review |

## Verdict

**No remote-code-execution or memory-safety path was found**, and the
parsing layer is genuinely disciplined — one strict codec with
`MaxItemLen` checked before allocation, bounds-checked integer reads,
depth-limited skip, and signature-before-apply ordering that held on
every path traced. What the audit did find is one real disk-exhaustion
hole, one silent configuration change, and a set of smaller hardening
gaps. In severity order:

---

## H1 · A co-member can fill your disk — unfiltered blob writes from relay sync

**Confirmed. High. `node/relay.go:811-813`** (`applyRelayItems`)

```go
for _, b := range blobs {
    _, _ = r.root.PutBlob(b)
}
```

Every blob a collected bundle carries is stored unconditionally. This is
the **one** blob-ingest path missing the expected-only gate that the
other two enforce — `kernel/sync/sync.go:576` (`acceptBlob` refuses
anything not in `e.pending`) and `node/materialize.go:81-108`
(`swarmCollect` drops any hash outside `expected()` *before* `PutBlob`,
with a comment stating exactly why). The invariant is even written down
at `sync.go:575` — "expected-only, verify before PutBlob" — and this
path silently violates it.

**The attack.** `PullFromRelay` drains each space's per-recipient inbox
`CapFor(tid, self, bucket)`. Deriving that hint needs only the terminal
id plus my device id — both of which every member of a private space
already holds (member cards carry device ids), and the blind relay
accepts a `Put` from anyone. A co-member crafts a bundle for `tid` whose
`blobs[]` are arbitrary distinct random byte strings and Puts it to my
inbox. On my next auto-sync each one is written under `blobs/sha256/…`.
Nothing references them, and **there is no GC** — the tree says so in
several places (`preview.go:369` warns that blind-copied blobs are
"immortal … in a store with no GC").

**Why it matters for the beta specifically**: one malicious or
compromised member forces unbounded disk growth on every other member's
device, refilled each round (bounded per round only by the relay's
`MaxItemBytes` 1 MiB / `CollectMaxBytes` 8 MiB, which the attacker
simply repeats). On a phone that is a real denial of service.
Content-addressing preserves integrity, so this is unsolicited-write and
exhaustion — not spoofing, not execution.

---

## H2 · Pasting a public link can silently repoint your global relay

**Confirmed. Medium. `node/public.go:318-323`** (`OpenPublicLink`)

```go
if s := r.GetSettings(); s.Relay == "" {
    s.Relay = relayAddr
    _ = r.SetSettings(s)
}
```

On a node in **automatic** relay mode both `RelayMode` and `Relay` are
empty by design (`settings.go:85-87`), so this branch fires. Opening a
pasted `space:` link writes the address **embedded by whoever authored
the link** into the global setting — which flips the node out of
measured automatic selection into custom mode, pinned to a
stranger-chosen relay, for the node's *personal* mailbox polling and all
relay-based sync.

The relay stays blind (it cannot read sealed content), but it gains a
persistent metadata and availability position over the whole node: which
mailbox hints exist, when the node polls, and the power to withhold. The
user consented to "open this link", not to "reconfigure my network".
Reversible in Settings — but silent, unannounced, and most likely to hit
exactly the fresh installs a beta consists of.

Worth stating that this is *only* the deliberate-paste path: `Follow`
(`preview.go:345-360`), preview and inspect all correctly record a
link's relay as the **space's** `SourceRelay` and never touch the global
one. The boundary is right everywhere except this auto→manual flip.

---

## M1 · An asset's media type is not allowlisted, and there is no CSP

**Suspect. Low–Medium. `protocol/schemas/blocks.go:222-226`,
`node/api_blocks.go:270-291`**

`AssetRef.MediaType` is validated only for non-empty and ≤128 chars — no
format allowlist — and `serveAssetBytes` trusts it verbatim as
`Content-Type`, setting `Content-Disposition: inline` for any `image/*`,
including `image/svg+xml`. So an attacker-authored block can make the
node serve an active SVG document on the app's own origin
(`/api/spaces/{id}/assets/{asset}`).

What holds today: `X-Content-Type-Options: nosniff` is set, the frontend
embeds media only through `<img>` / `<video>` / `<audio>` / CSS
`background:url()` (none of which execute SVG script), and non-media
types get `Content-Disposition: attachment`. The residual: **there is no
Content-Security-Policy anywhere** in the node or the served UI
(confirmed absent), so a direct navigation to that asset URL — a new
tab, a copied link — renders it as an active document in the app origin.
Gated behind a manual user action, hence "suspect" rather than
confirmed, but a MediaType allowlist and a CSP would each close it
independently.

---

## M2 · `answerWants` serves any locally-held blob by hash

**Confirmed. Low. `node/relay.go:696`**

Fetches `r.root.GetBlob(h)` for a requested hash with **no**
`BlobAllowed(h, tid)` gate — unlike `kernel/sync/sync.go:556`
(`serveBlobs`), which checks it. A cross-space "does this node hold
content X" oracle for a known content address. Impact is limited: blobs
stay encrypted (the block key is still needed), and knowing the SHA-256
of a ciphertext generally implies already having it.

---

## M3 · The AI space is briefly visible before `LocalOnly` is stamped

**Suspect. Low. `node/ai.go:121-131`** (`EnsureAISpace`)

The space is created and attached to `r.spaces` — making it visible to
the relay and LAN driver loops — and only afterwards, in a *separate*
`r.mu` critical section, is `LocalOnly = true` written. A relay-sync or
LAN-announce tick that takes `r.mu` in that gap sees the AI space
without the flag and would derive and poll/announce its mailbox hint.

Bounded: content is sealed, so only the *existence* of a mailbox hint
could leak, never the conversation. The fix shape is obvious —
LocalOnly-from-birth, inside the creation critical section rather than
as a follow-up write. The negative guarantees themselves are otherwise
enforced at single chokepoints and hold (see below).

---

## M4 · Self-declared mentions can force a signal in an open community

**Suspect. Low. `node/attention.go:210`, `:318`**

`c.Mentions = e.Content.Text.Mentions` — the mention list is a
self-declared payload field, and `mentions_me` is a hard QuietRank rule.
In a public open-join community, any contributor (somebody the user
never individually approved) can put the user's principal id in
`Mentions` and force a high-priority Signals-inbox entry. It is a
*signed* claim by a real author, not a fabrication and not a UI spoof —
so this is attention-spam rather than impersonation. Noted because the
Signals inbox is cross-space and the vector is unbounded per open
community. A per-space signals toggle is already a tracked pending item
and is the natural answer.

---

## L1 · Loose pre-allocation from a bounded-but-large count

**Confirmed. Low. `protocol/manifest/manifest.go:371`** (`readTexts`)

`make([]string, 0, n)` with `n` straight from `d.ReadArray()`, validated
only against the global `MaxItemLen` (1<<20) rather than a
domain-specific bound. A ~5-byte CBOR array header declaring 2^20
elements causes an immediate ~16 MiB allocation before any element is
read; the loop then fails fast and the memory is freed. Per-call bounded
and self-correcting, but a flood of tiny malformed frames is sustained
alloc/GC pressure on a memory-constrained device.

Reachability is limited — this decode runs either inside an already
size-capped signed envelope or after `projection.Verify` — which is why
it is low. The correct pattern is one file away:
`protocol/publication/publication.go:507` checks `cnt > max` *before*
`make`.

## L2 · The projection decoder has no top-level size guard

**Confirmed. Low (latent). `protocol/projection/projection.go:209`**

Unlike `signal.Decode`, which rejects `len(frame) > MaxFrameLen` up
front, the projection decoder has no overall ceiling and relies entirely
on its callers. Not exploitable today: the only feeders receive items
over `transports/lan`, whose `readLoop` caps every packet at 1 MiB
(`lan.go:38,76`), and the internal loops all use `append`, so memory
stays proportional to input. It is a latent gap if a future caller ever
feeds projection bytes from a path without its own cap — an explicit
`MaxEnvelopeLen` would make the invariant local instead of assumed.

## L3 · The local API token travels in asset URL query strings

**Informational. `publications.js:911/916/921`, `listening.js:251`,
`preview.js:343`**

`?token=…` is placed into `<img>`/`<video>`/`<audio>` `src` and `<a>`
`href`. Same-origin localhost, so exposure is limited to referrer,
History and log surfaces — but it is the reason a future CSP and a
future desktop shell should not treat the token as the real boundary.

## L4 · `CutPoint` copies fixed arrays without length validation

**Informational. `protocol/projection/projection.go:276,284`**

`copy(c.Device[:], db)` accepts a decoded byte string of any length;
`copy` truncates or zero-pads rather than panicking, so there is no
memory-safety issue, and a tampered projection fails `Verify` anyway
(which re-encodes the fixed 32-byte arrays). Cosmetic robustness only —
noted because every other decoder in the tree length-checks explicitly.

---

## Classes checked and found clean

Each of these was traced in code, not assumed:

**Parsing**
- *Signature-before-apply ordering.* `eventlog.Ingest` decodes → matches
  the terminal → `VerifyFrame` (ed25519) **before** `apply`; `absorb`
  only ever sees verified events. Projection install verifies the space
  signature, the manifest signature, and then **each frame's own** device
  signature before absorbing. Radio frames MAC-verify before the
  post-auth check.
- *Allocation from raw counts.* Every length-prefixed binary framing
  bounds `n` before allocating — `lan.go:76`, `node/ledger.go:231`,
  `kernel/routing/queue.go:219`, `meshtastic/radio.go:721`,
  `radiotransfer/receiver.go:108`. Apart from L1, all codec reads cap at
  `MaxItemLen` before touching data.
- *Panics on malformed input.* All `binary.BigEndian` reads work on
  fixed-size buffers filled by `io.ReadFull` or guarded by explicit
  length checks. The only `panic()` calls are init-time registration and
  entropy failures — never in a decode path. No unchecked type
  assertions in the parsers.
- *Decompression bombs.* No gzip/zlib/deflate anywhere in the wire
  paths; the compact radio token expansion is ~10× bounded by the LoRa
  MTU-sized input.
- *Unbounded recursion.* `codec.skipItem` caps nesting at 32;
  `publication.decodeBlock` caps at `MaxDepth` with `MaxBlocks` /
  `MaxChildren`.
- *Integer overflow in TTL/size math.* Expiry comparisons are plain `>=`
  on `uint64` with no wrapping arithmetic; the division sites guard
  their divisors.
- *Quicklink/pass decode.* The outer map decodes through the capped
  codec, version and nonce length are checked, then AEAD-open precedes
  the inner decode — the scrypt cost is keyed on the user-typed token,
  not on attacker-controlled repetition.

**Assets**
- *Content-addressed integrity, both ways.* `PutBlob` keys by SHA-256;
  `GetBlob` **re-verifies the hash on every read** and fails closed;
  `RetrieveTo` verifies every chunk's AAD position and size plus the
  whole-plaintext digest; `LoadManifest` cross-checks the manifest
  against the ref, so a holder cannot substitute manifests.
- *Path traversal.* The blob path is `blobs/sha256/<hex[:2]>/<hex>` from
  a fixed 32-byte hash — no attacker string, no separators, no `..` or
  null possible. URL asset ids are hex-decoded and length-checked to
  exactly 16 or 32 bytes. Sealed-blob names are regex-gated with an
  explicit `..` rejection. Download filenames go through
  `NormalizeFilename`.
- *Resource caps.* `MaxAssetSize` 500 MiB, `MaxChunksPerAsset` 16384,
  `MaxManifestBytes` 1 MiB, `MaxInlinePreview` 40 KiB, preview
  `previewGlobalBudget` 160 MiB with a per-job peak preflight,
  `previewCap` 8 sessions, 10-minute TTL; upload is streamed through
  `MultipartReader` behind `MaxBytesReader`, never `ParseMultipartForm`.
  The only genuinely unbounded write is H1.
- *Server-side decode surface.* None — the Go layer only hashes,
  decrypts and hands raw bytes to the WebView. No image or audio decoder
  bomb surface on the node.
- *Caching and object URLs.* Preview serving sets `Cache-Control:
  no-store` and the asset handler preserves it; both `createObjectURL`
  sites revoke; no service worker is registered.
- *Unsolicited push on the other two channels.* The direct-peer path and
  the transient public swarm path both correctly refuse unrequested
  blobs. Only the persistent relay path (H1) does not.

**Consent and identity**
- *"Looking never subscribes" holds.* Preview and inspect run through
  `openPublicSession`, which builds a throwaway reducer state with no
  keystore handle, no `SpaceMeta`, no Navigator write and no relay
  adoption; the reference relay is dialed and never adopted. Durable
  subscription happens only through the deliberate paste and explicit
  Follow.
- *Provenance laundering is structurally prevented.* The share builder
  rebuilds attribution from the currently visible quoted text and never
  reads the source's own `ShareOrigin`, so a share-of-a-share attributes
  to the person who showed it to you, not to the original claimant.
- *Labels are claims, and typed as claims.* `AuthorLabel` / `SourceLabel`
  are documented self-declared; `quotationOf` reads the **signer**, not
  the editable `DisplayAuthors`; author-name resolution skips AI and bot
  cards. (What the JS layer *renders* was not audited — see scope.)
- *The radio CARD signing hole is closed* — a card signed by a different
  device cannot overwrite a peer's keys, pinned by test.
- *Unauthenticated draining is capability-gated.* Ingress collection
  uses the space's `IngressRoot`; reply-box collect caps never leave the
  process; personal-quicklink destructive `Collect` runs only against a
  relay that returned a definitive empty, strictly sequentially.
- *Pass auto-accept is gated.* Owner-side validation consumes a use only
  on real admission; host approval parks instead of admitting; the guest
  requires a sealed acceptance from the rendezvous. Curator
  auto-activation is driven by the **verified signed policy's** writer
  set, never by local claims.
- *Projection install refuses equivocation and rollback* — seq regress
  rejected, same-seq-different-digest refused loudly.
- *`reservedPresence` holds* — remote content cannot fabricate
  verified/system/admin presence in the UI.

---

## What to do before the beta, in order

Not scheduled here, just the honest ordering if any of it gets picked up:

1. **H1** — one `expected()`-style gate in `applyRelayItems`, matching
   the two paths that already have it. Small, and it is the only finding
   that lets a remote party consume an unbounded local resource.
2. **H2** — do not write the global relay from a pasted link; record it
   as the space's source relay, exactly as Follow already does. Or ask.
3. **M1** — a MediaType allowlist is the cheap half; a CSP is the
   thorough half and also covers L3.
4. The rest are hardening and can ride whatever wave touches those
   files.

## Follow-up passes worth running

The three unaudited areas in the scope table, most valuable first: the
**web UI's XSS surface** (innerHTML sinks over remote-authored text is
the one class that could turn a message into code in the app origin),
then the **local API auth/CSRF posture**, then dependencies.
