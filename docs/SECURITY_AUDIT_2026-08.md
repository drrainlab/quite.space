# Security audit — pre-beta, 2026-08-11

> **Status, 2026-08-11 (same day): H1–H4, M1, M2 and L7's sibling are
> FIXED — see the two commits following this file. What remains open is
> listed under "Still open" at the end. The findings below are kept in
> their original wording, because a register that quietly rewrites itself
> to match the code stops being evidence.**

This began as a findings register, written before the public beta so that
the cheap mistakes are known rather than discovered by a stranger. Threat model: **something reaching the user's device or gaining
control it should not have.** Six read-only passes — local HTTP API,
untrusted wire parsing, web-UI DOM sinks, asset/blob handling, the
executable-payload boundary (ADR-013), and the consent/impersonation model.

Every finding below marked CONFIRMED was re-verified by hand against the
cited lines after the audit passes, not taken on a reviewer's word. Where a
claim rests on a step that was not traced end to end, it says SUSPECT and
names the untraced step.

## The headline

The protocol layer is in genuinely good shape. The **parsing/verification
half found nothing above informational**: the codec bounds every allocation
before making it, signature-before-apply holds on every path, there is no
decompression surface, recursion is depth-capped, and the trust/consent
model at the protocol edges is defended with explicit rationale nearly
everywhere. ADR-013's "no executable payload" invariant holds for the whole
generative surface — scenes, effects, renderers, app instances — with no
`eval`, no `new Function`, no template engine, and unknown ids degrading to
fallback.

**Every serious finding is in the client and its immediate serving layer**,
and they share one root cause and one missing backstop:

- **Root cause:** validation that exists on the *authoring* path is not
  re-applied on the *inbound* path. A signature proves who wrote a frame,
  never that the frame is well-formed — and several renderers treat signed
  content as if it were validated content.
- **Missing backstop:** there is **no Content-Security-Policy anywhere** —
  not a header in `node/api.go`, not a `<meta>` in `index.html`. A CSP with
  `script-src 'self'` would neutralise findings 1–4 outright.

The consequence is uniform, so it is stated once here instead of five
times: the UI origin holds the local API bearer token, and the token is the
*only* gate on all ~57 routes. Script execution in that origin therefore
means read every space, mint invites, exfiltrate the log, change settings.
Every HIGH below ends there.

---

## HIGH

### H1 — Presence vocabulary breaks out of an inline `onclick` → script execution
**CONFIRMED.** `clients/web-ui/assets/app.js:2244`

```js
menu.innerHTML = states.map(s =>
  `<button type="button" role="menuitem" onclick="setPresence('${esc(s)}')">${esc(s)}</button>`
```

`esc()` (`app.js:2946`) is an **HTML-entity** escaper; here its output lands
in a **JS string inside an HTML attribute**. The HTML parser decodes
`&#39;` back to `'` *before* the handler is compiled as JavaScript, so the
quote closes the string literal and the rest is code. `esc()` is the wrong
escaper for this position, not a missing one.

The vocabulary is remote: `char.presence` (`app.js:1738`) comes from the
space's Character, parsed out of the manifest's declared labels by
`ParseCharacter` (`terminals/terminals.go:142`) — and **that inbound path
never calls `Character.Validate()`**, which is the only thing that would
have run `ValidatePresenceState`. Label *content* is unconstrained too:
`manifest.Validate` bounds only the label count (`MaxLabels = 32`,
`protocol/manifest/manifest.go:292`), never the characters. So a space
owner ships arbitrary text here.

One honest constraint on the payload: presence states are joined and split
on `,` (`terminals/character.go:118`, `:163`), so the injected code cannot
contain a comma. This narrows the payload; it does not prevent it.

**Scenario:** a space you join publishes a presence state of
`');fetch('/api/spaces'...)//`. You open the presence picker and click the
item — whose visible label the attacker also chose. Script runs in the app
origin with the token.

### H2 — Same `esc()`-in-`onclick` pattern, four more sinks
**CONFIRMED as a pattern; reachability SUSPECT per site** (depends on how
much of each field the remote party controls, which was not traced to the
wire for the mesh cases).

- `clients/web-ui/assets/gateway.js:174,175` — `gwUnpin('${esc(gw.link)}')`,
  `gwPin('${esc(gw.fingerprint)}')`, fed by gateway announcements heard over
  the radio mesh
- `clients/web-ui/assets/radiomeet.js:138,149,234` — `rmOpenSpace`,
  `acceptRadioOffer` with `o.id` / `o.space` / `o.from` from radio meet offers
- `clients/web-ui/assets/quicklink.js:114` — `qlWithdraw('${esc(q.hint)}')`
  (locally generated, so likely not attacker-controlled; listed because the
  sink is identical and should not be left as a template for the next one)

### H3 — `javascript:` URI in link and video-link blocks
**CONFIRMED.** `clients/web-ui/assets/publications.js:529` (`a.href = p.text`),
`:573` (`a.href = p.asset`), and `clients/web-ui/assets/app.js:3852`
(`renderLink`: `a.href = e.url`).

The scheme allowlist exists — `checkURL` permits only `http`/`https`/`qs:`
(`protocol/publication/validate.go:301`) — but lives inside
`publication.Validate`, which runs **only when authoring**
(`node/publications.go:482`). The inbound path is
`applyPublicationRevision` → `publication.Decode`
(`kernel/reducers/publications.go:100`), which enforces size and structure
and **never calls `Validate`**. A peer running a patched client emits a
signed revision whose link block is `javascript:…`.

`markdown.js` does this correctly — `SAFE_SCHEME` gates every link
(`markdown.js:102`) — but these three block renderers bypass markdown and
assign `href` directly. `rel="noopener noreferrer"` does nothing against a
`javascript:` scheme. One user click on an ordinary-looking link.

### H4 — Attacker-chosen asset `Content-Type` + token in the URL → SVG script execution
**CONFIRMED for the mechanism; the click is a required user step.**
`node/publications.go:960` (upload), `node/api_blocks.go:271-282` (serve),
`clients/web-ui/assets/publications.js:625` (`window.open`),
`clients/web-ui/assets/app.js:3288` (`assetURL`).

`AssetRef.MediaType` is attacker-supplied at upload and validated only for
non-empty and length ≤ 128 (`protocol/schemas/blocks.go:222`) — **no format
allowlist.** `serveAssetBytes` sets it verbatim as `Content-Type` and marks
anything `image/*` as `Content-Disposition: inline`, which `image/svg+xml`
matches. `X-Content-Type-Options: nosniff` is set and does not help: it
stops sniffing, not an *explicitly declared* SVG.

The strict `AllowedPreviewMIME` allowlist that correctly rejects SVG
(`protocol/schemas/blocks.go:478`) is applied **only to inline previews**
(`node/api_blocks.go:117`) — never to the asset itself.

The chain closes because the UI opens assets as **top-level documents**:
image zoom is `window.open(assetURL(p.asset))`, and `assetURL` returns
`/api/spaces/${current}/assets/${id}?token=${token}`. A top-level SVG
document executes its scripts in the app origin, where
`location.search` hands it the token. Reachable from untrusted **public**
content too — `node/preview.go:508` reuses the same serving path, so
previewing a post from a directory you never joined is enough to be served
the bytes.

---

## MEDIUM

### M1 — Relay-carried blobs are written to disk with no want-set check
**CONFIRMED.** `node/relay.go:811`

```go
for _, b := range blobs {
    _, _ = r.root.PutBlob(b)
}
```

Every blob a collected bundle carries is stored unconditionally. The other
two ingest paths do gate: `kernel/sync/sync.go:576` refuses anything not in
`e.pending`, and `node/materialize.go:95` drops any hash not in `expected()`
*before* `PutBlob`, with a comment explaining precisely why. This path
diverges from an invariant the codebase states elsewhere in prose.

Integrity is intact — content-addressing means the bytes are what their
hash says — so this is **unsolicited write and disk exhaustion**, not
spoofing. There is no GC of the blob store (the code notes this repeatedly),
so the bytes are permanent. Per round it is bounded by the relay's caps
(1 MiB/item, 8 MiB/collect); the attacker simply refills. A co-member who
can derive the inbox hint forces unbounded disk growth on every other
member's device — which matters most on the phone this is heading toward.

### M2 — Opening a pasted public link silently pins the node's relay
**CONFIRMED, and it is a stale assumption rather than a careless one.**
`node/public.go:319`

```go
if s := r.GetSettings(); s.Relay == "" {
    s.Relay = relayAddr
    _ = r.SetSettings(s)
}
```

The comment above it defends this as the deliberate-paste path, and that
argument was sound when written. The RR wave changed the meaning of the
empty string underneath it: `relayIsAutomatic` is
`RelayMode == "automatic" || (RelayMode == "" && Relay == "")`
(`node/settings.go:85`), so on a node in **automatic mode `Relay` is empty
by design** — it is the normal state, not an unconfigured one. Writing a
non-empty value therefore does not "fill a gap"; it **flips the node out of
measured selection into custom mode, pinned to a relay chosen by whoever
wrote the link.**

This is the same class as the six call sites the RR tail already fixed
(`71b8776`) — code that reads `Settings.Relay` assuming empty means
unconfigured. This one was missed.

The relay stays blind to content, so the cost is metadata position and the
power to withhold, not disclosure. Reversible in Settings, but silent.

---

## LOW / informational

- **L1 — Bearer token travels in URL query strings.** `auth()`
  (`node/api.go:220`) accepts `?token=`, and the UI puts it in `<img>`,
  `<audio>`, `<video>`, CSS `url()` and `window.open` targets
  (`app.js:3288` and others). It lands in history and the disk cache, and
  it is what H1–H4 actually steal. Credit where due: outbound links carry
  `rel="noopener noreferrer nofollow"`, so the Referer path is closed, and
  no server-side logging middleware captures the query string.
- **L2 — No Origin/Host validation on any route.** No CORS policy, no
  `Sec-Fetch` check. The token is the sole defence against a DNS-rebinding
  page driving the API from the user's own browser. There is no WebSocket
  in this build, so there is no upgrade handshake to check.
- **L3 — Token compared with `!=`** rather than
  `crypto/subtle.ConstantTimeCompare` (`node/api.go:226`). Not practically
  exploitable over loopback with a 128-bit token; noted for blast radius.
- **L4 — SSRF primitives behind the token:** relay push/pull addresses
  (`node/api.go:1103`, `:1130`), LAN `ConnectPeer` (`:1146`), Meshtastic
  `tcp:` target (`:386`), and the LLM `base_url` (`node/llm/llm.go:65`).
  Responses are parsed as a specific wire format and capped at 1 MiB, so
  blind exfiltration is limited, but the connect itself is a scan primitive.
- **L5 — `serial:/dev/<path>` opens an arbitrary local device**
  (`node/api.go:386` → `StartMeshtastic`). Token-gated; the downstream open
  was not traced. SUSPECT.
- **L6 — `answerWants` serves any locally held blob by hash**
  (`node/relay.go:696`) without the `BlobAllowed(h, tid)` scope check that
  `kernel/sync/sync.go:556` applies. A cross-space "do you hold X" oracle;
  blobs stay encrypted, and knowing a ciphertext's hash generally implies
  already having it.
- **L7 — `LocalOnly` is stamped after the AI space is already attached.**
  `node/ai.go:121-131`: `CreateSpaceWithOptions` attaches the space to
  `r.spaces` and only a *separate* `r.mu` section later sets
  `meta.LocalOnly = true`. A sync/announce tick landing in that window sees
  the AI space without the flag. Bounded — sealed content, so at most the
  existence of a mailbox hint leaks — but the guarantee should be
  LocalOnly-from-birth. SUSPECT (race not reproduced).
- **L8 — Self-declared `Mentions` force a `mentions_me` signal.**
  `node/attention.go:210`, `:318`. In an open community any contributor can
  put your principal id in the list and land in your cross-space Signals
  inbox. Signed spam by a real author, not impersonation. Relates to the
  already-tracked per-space signals toggle (task #143).
- **L9 — `readTexts` preallocates from an unbounded count.**
  `protocol/manifest/manifest.go:371`: `make([]string, 0, n)` where `n` is
  checked only against the global `MaxItemLen` (1 MiB). A 5-byte header
  claiming 2²⁰ elements allocates ~16 MiB before reading an element, then
  fails on truncation and frees it. Per-call bounded and self-correcting;
  gated behind size-capped, signature-checked content. The correct pattern
  is two files away (`protocol/publication/publication.go:507` checks
  `cnt > max` before `make`).
- **L10 — `projection.Decode` has no top-level size guard**
  (`protocol/projection/projection.go:209`), unlike `signal.Decode`. Not
  currently exploitable: the only feeders come through
  `transports/lan/lan.go:76`, which caps packets at 1 MiB. A latent gap if a
  future caller lacks its own cap.
- **L11 — `CutPoint` fixed-array `copy` without length check**
  (`protocol/projection/projection.go:276`, `:284`). `copy` truncates rather
  than panicking, and a tampered projection fails `Verify` anyway.
  Robustness only.
- **L12 — CSS-value injection via `autoMediaBg`** (`app.js:3334`) builds
  `url("${base}")` from an asset ref. If a ref could contain `"` it could
  add a second background layer pointing off-origin (a tracking beacon).
  Assignment is to `.backgroundImage`, not `cssText`, so no new declarations
  and no script. Depends on ref validation, which was not confirmed. SUSPECT.
- **L13 — `navigator.js:230`** interpolates `sp.undecryptable` into
  `innerHTML` without `esc()`, alone among its neighbours. Expected to be a
  local numeric count; defensive only.

---

## Checked and clean

Recorded so the next audit does not re-derive it.

**Parsing / memory safety.** Every length-prefixed framing bounds its count
before allocating (`lan.go:76`, `node/ledger.go:231`,
`kernel/routing/queue.go:219`, `meshtastic/radio.go:721`). Codec reads cap at
`MaxItemLen` before touching data; multi-byte integer reads are bounds-checked;
canonical/shortest-form is enforced. No `panic()` in any decode path — the
only ones are init-time registration failures. No gzip/zlib/deflate anywhere
on the wire. Recursion is depth-capped at 32 (`codec.go:253`) and by
`MaxDepth`/`MaxBlocks`/`MaxChildren` for documents. No integer overflow found
in TTL/size arithmetic.

**Verify-before-apply.** `kernel/eventlog/log.go:122` decodes, matches the
terminal, then `VerifyFrame`s **before** `apply`. Projection install verifies
the space signature, the manifest signature, and then **each frame's own
device signature** (`terminals/projection.go:324`) before absorbing. Radio
frames MAC-verify before the post-auth check. Projection install refuses seq
regression and same-seq-different-digest equivocation (`node/public.go:161`).

**ADR-013 / executable payload.** No `eval`, `new Function`, `Function()`,
`setTimeout(string)`, template engine, or data-driven code path in any
renderer. Scene ids resolve through `SCENES` and return `null` when unknown;
effect ids through the `EFFECTS` allowlist; renderers through
`OBJECT_RENDERERS` with a generic fallback; app instances through a fixed
`switch` and a fixed reducer map. Params are re-clamped client-side
(0–1000 permille) independently of the wire, canvas backing is capped at
`MAX_BACKING_PIXELS = 256000`, frame rate at 30 fps. The photosensitivity
floor is enforced by **per-frame pixel readback** (`brush.js:439`), not by
model prediction, so no param or palette can defeat it; reduced-motion and
hidden-tab floors are read from the client's own `matchMedia`. Scenes get a
five-verb surface, never a canvas context. Inline SVG exists only in
`glyphs.js`, built from numeric id-seeded geometry with no content text.

**Path traversal.** Blob paths are `blobs/sha256/<hex[:2]>/<hex>` from a
fixed 32-byte hash — no attacker string reaches a path separator
(`kernel/storage/storage.go:934`). URL asset ids are hex-decoded and
length-checked to exactly 16 or 32 bytes. Sealed blob names are regex-gated
with explicit `..` rejection. Backup restore `filepath.Clean`s and rejects
`..`/absolute/symlink entries (`node/backup.go:273`) — and is not exposed
through the API at all. The UI is served from an `embed.FS` with no
request-derived path.

**Blob integrity.** `PutBlob` keys by SHA-256; `GetBlob` **re-verifies on
every read** and fails closed (`storage.go:901`); `RetrieveTo` checks each
chunk's AAD position/size plus the whole-plaintext digest;
`LoadManifest` cross-checks the manifest against the ref so a holder cannot
substitute one. No server-side image or audio decoding of untrusted bytes
anywhere — the Go layer hashes and decrypts, the WebView does the rest.

**"Looking never subscribes" holds.** Preview and inspect build a throwaway
`reducers.State` with no keystore handle, no `SpaceMeta`, no Navigator write
and no relay adoption; the reference relay is dialled and never adopted
(`node/preview.go:193`). Durable subscription happens only through
`OpenPublicSpace`, reached from the deliberate paste and explicit Follow.
(M2 is about what the paste path *additionally* does, not about this.)

**Provenance and impersonation, Go side.** A share-of-a-share rebuilds
attribution from the currently visible quoted text and never reads the inner
`ShareOrigin` (`node/share.go:262`), so a claim cannot launder itself into a
fact across hops. `quotationOf` reads the **signer** (`pub.Author`),
explicitly not the editable `DisplayAuthors` (`share.go:222`). The radio CARD
signing hole is closed and pinned by test (`node/radiopeer_test.go:189`).
`reservedPresence` blocks presence states impersonating system properties
(`terminals/character.go:69`) — though note H1: that check is not run on the
inbound path at all.

**Capability gating.** Ingress collection derives from the space's
`IngressRoot`; reply-box collect caps never leave the process; unsolicited
reply-box blobs are dropped before `PutBlob` and before spending budget
(`node/materialize.go:95`). Personal-quicklink destructive `Collect` runs only
against a relay that returned a definitive empty, strictly sequentially.
Pass admission validates revoked/expired/capacity and spends a use only on
real admission.

**Auth coverage.** Every one of the ~57 `/api/*` routes is `a.auth`-wrapped;
the only unauthenticated handler is the static file server. No token-free
mutating or network-triggering route. No `exec`/shell reached from any HTTP
handler with request data. Preview media correctly sets `no-store`, and
`serveAssetBytes` was fixed not to clobber it.

---

## What was done, and what it cost

Worked in the order below. Two corrections to my own recommendations came
out of doing it, and both are worth keeping.

**A CSP was NOT one header.** The claim above — "one header, and H1–H4
stop being code execution" — assumed a strict `script-src 'self'`, and
`index.html` declares ~215 inline `on*` handlers that such a policy
breaks. They are all first-party, literal, and unreachable by remote
content, so they are not the vulnerability; they are just what a strict
policy would take down with it. So the CSP shipped in two layers and one
honest compromise:

- **the interface** gets everything strict except `script-src`, which
  carries `'unsafe-inline'` with the reason and the price written at
  `node/api.go`. `connect-src 'self'` is the part that earns its keep: it
  closes the silent exfiltration channels, so even a successful injection
  cannot post the log or the token anywhere. Top-level navigation is not
  covered and the comment says so rather than implying otherwise.
- **asset responses** get `sandbox` with no `allow-scripts`, which is
  strict and costs nothing, because an asset is never an app page. That
  is what closes H4 at the second layer.
- **`script-src 'self'` remains unearned.** Migrating the 215 handlers to
  `addEventListener` is what buys it, and it is the one change that would
  make an injected `<script>` or `javascript:` URI inert on its own.
  Recorded under "Still open" rather than quietly dropped.

**Re-validating inbound publications was considered and declined.** The
audit proposed `publication.Validate` after inbound `Decode`. Doing it
would make a signed-but-malformed post *disappear*, and on a read path in
a local-first app that is the wrong failure direction — the same argument
the codebase already made for `qp.kind` ("an unknown value is ignored,
never fatal", CAT-0b gate 1). The fix belongs where the danger is: the
client refuses to make a bad URL clickable (`MD.safeHref`), and the node
keeps serving the author's bytes unaltered. Where re-validation WAS right
is the case the audit did not name — see below.

Fixed, in order:

1. **CSP, two layers** — `node/api.go` (`uiPolicy`, `uiSecurityHeaders`),
   `node/api_blocks.go` (`sandbox` on every asset response).
2. **The six `onclick` sites** — presence menu rebuilt as nodes with a
   real listener; gateway rows on one delegated listener bound once (a
   per-repaint bind would have fired once per poll tick); radio meet and
   quicklink moved to `data-` attributes. Plus a seventh the audit missed
   — `gwAttach`/`gwAttachRNode` — found by the mechanical grep now in
   `scripts/webui/injection.cjs`, which is the argument for having it.
3. **The three `href` sites** — one exported predicate, `MD.safeHref`,
   shared with the markdown renderer that already had it right. It tests
   the address with control characters stripped, so an embedded newline
   cannot walk around the scheme check.
4. **A different inbound re-validation than the one proposed**, and a
   better one: `ParseCharacter` never ran `Character.Validate`, so the
   `reservedPresence` rule — no presence state may impersonate a protocol
   fact — was enforced on write and **not on read**. A space could declare
   a presence state of `verified`, `system` or `admin` and every reader
   would render it beside the honest ones. `admissiblePresence` now drops
   inadmissible states (and caps the count) while keeping the space, which
   is the tolerant-on-read shape this codebase already uses.
5. **The inline-render allowlist** — `schemas.AllowedInlineMIME`.
   Deliberately not an upload allowlist: what an unlisted type loses is
   the right to be *rendered*, never the right to be shared.
6. **The want-set gate** — `node/relay.go` now refuses a relay-carried
   blob this node never asked for, matching `acceptBlob` and
   `swarmCollect`.
7. **M2** — `OpenPublicLink` no longer adopts a link's relay on a node in
   automatic mode, where an empty `Settings.Relay` is the normal state and
   not a gap.

## Still open

Recorded, not scheduled. None is a known exploit; each is a place where a
future mistake would cost more than it should.

- **`script-src 'self'`**, gated on migrating ~215 first-party inline
  handlers (see above). The single highest-value remaining item.
- **L1/L2** — the token in query strings, and no Origin/Host validation.
  Both are structural: an `<img>` cannot send a header, so the media URLs
  need the query string until asset access is redesigned. `connect-src`
  now blunts the consequence.
- **L3** — non-constant-time token comparison.
- **L4/L5** — the SSRF and serial-path primitives behind the token.
- **L6** — `answerWants` serves any held blob by hash without the
  space-scope check its peer-path sibling applies.
- **L7** — `LocalOnly` stamped after the AI space is already attached.
  Bounded, but should be LocalOnly-from-birth.
- **L8** — self-declared `Mentions` forcing a signal; related to the
  already-tracked per-space signals toggle.
- **L9–L13** — the allocation and robustness items, none reachable today.
