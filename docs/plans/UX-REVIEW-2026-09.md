# UX review, September 2026 — the plan and the bar

An outside review (2026-09-11) of the shipped screens. Its diagnosis,
in one sentence: *the interface explains internal mechanics where it
need not, and fails to explain consequences where it must.* We agree.
This document turns the review into three waves and one acceptance
bar, so each wave closes against scenarios rather than against taste.

## The bar — three scenarios, no explanation given

A person who has never seen Quite, handed a phone with the app:

1. **Invites a friend.** Finds the way in, understands what the friend
   will be able to do, and does it — without choosing a protocol entity.
2. **Handles a delayed message.** Sees that a message has not left,
   understands why and whether anything is expected of them.
3. **Reads a sensor's freshness.** Tells a live reading from a stale one
   before reading the number.

Watch where they stop and what they get wrong. A wave is done when its
scenarios pass with two people out of three.

## What the review got right, and what it conflated

- Right: the space, not the app, is the subject of the screen; the
  navigation mixes conversation, content formats, tools and a spatial
  mode; the most original part (instruments) sits below the fold; four
  coloured header buttons compete with the spaces they lead to; a
  number can look fresher than it is.
- Conflated: **sound and words are device pairing** (your own second
  device, MD-1) while **a pass is a person's entry into a space**
  (owner-confirmed, ADR-012). The contradiction on screen is real, and
  it is fixed by separating the two INTENTS — "this is my device" vs
  "this is another person" — not by unifying the mechanisms.

## Waves

**UX-1 — the shell says less, and says it plainly** (web-ui only) — ✅ 8b8f23f
- Compact space header; the logo lives in the list and the front door.
- Literal buttons: Create a space · Join · Invite · Back up.
- The list outweighs the header: the four coloured buttons become quiet
  entries; create and join sit together.
- Space panel order: **Now** (instruments, live modes) → People → About
  this place → Manage. A reading says *no fresh data · last 6 days ago*
  before it says 24.1 °C.
- Words for delivery: "Saved on this device — sent when a path opens";
  the presence age carries its label.
- Compact link cards by default; fewer nested frames; monospace only
  for codes, verification words and telemetry; a reading typeface for
  conversation; contrast checked against real colours.

**UX-2 — navigation by meaning** — ✅ 3d2976f
- Materials = posts · files · links, one view with filters.
- Shelf and Objects each get their one sentence; a section whose
  sentence does not convince merges into Materials.

**UX-3 — invitations by intent, This device by task** — ✅ (this commit)
- Two entries: *Into this space* / *A new conversation*; then the
  carrier: link · QR · sound (words for a new conversation). Note: in
  the web-ui, words and sound both invite a PERSON (quick link / audio
  pass); pairing your own device lives in the native shells, so the
  device/person split needs no third intent here.
- One paste field that recognises the format. Previewing a quick link
  never spends it — node/quicklink_test.go (a link resolves twice, then
  admits once; backing out keeps the entrance) pins that promise.
- Profile & devices overview; verification, backup and technical
  details open from it. Backup shows its state: not yet · created at.

Out of scope, on purpose: automatic "online", typing lights, read
receipts. Presence stays what a person declares (ADR-017).
