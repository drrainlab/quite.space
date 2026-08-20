# Media Presence — Release / Arrival / Presence

The UX contract for media transfer. Not upload/download: **two states of
one object**. The sender RELEASES an object into the space; at each
member it ARRIVES. The interface's systemic rule:

> **Movement means something is happening. Stillness means the object
> has landed.**

Everything below is derived from signals the node already publishes
honestly (ADR-023: inability is never success, never silence). Nothing
here invents state, and nothing depends on WHICH transport carries the
bytes — LAN, relay, direct, radio, another member. The visual contract
must survive topology evolution.

## Arrival (receiver) — v1, shipped

The card appears at its final size and layout immediately (the preview
rides inside the signed frame). Progress is a property of the media's
own surface, never a separate spinner.

| Contract state       | Signal                                        | Surface |
|----------------------|-----------------------------------------------|---------|
| DISCOVERED           | ref known, not fetching                       | soft preview, "fetch original" |
| WAITING FOR A PATH   | `fetching`, zero bytes (`got == 0`)           | "reaching the sender…", quiet breathe |
| STILL ASKING         | `fetching`, `reason: no_source` (20s silence) | waiting text, no bar (nothing progresses) |
| ARRIVING             | `fetching`, `got > 0`                         | surface resolves with the bytes |
| HERE                 | `complete`                                    | full stillness |

- **Photo / video poster**: blur is what remains of the distance —
  `blur(7px × (1 − got/total))`. Half the bytes, half the softness.
  Steps are chunky because a relay round IS a step; honesty over
  smoothness. Sharpening on completion takes .8s; nothing ever runs
  backwards, because `got` only grows.
- **Audio / voice**: the waveform materialises bar by bar; bars beyond
  the arrived fraction are ghosts at 22% — the shape is known from the
  preview, the substance is still travelling. Layout never moves.
- **Grouped media**: every tile carries its own asset state and arrives
  independently inside an unchanging layout.
- **Numbers** (`fetching… 4/160` + rail) stay in the asset note under
  the card — the detail, not the show.
- **Failure is quiet**: WAITING FOR A PATH ↔ ARRIVING oscillation, not
  a red verdict. The two terminal states that exist are terminal for
  real: `integrity_error` (bytes are not the file they claim) and
  `no_peers` (a fact about US — no relay, no link).
- **Reduced motion**: every animation has a static equivalent (dimmed
  fill, static tint, no breathe).

## Release (sender) — v1, shipped

The object is fully visible from the first frame — it exists and is
never drawn "incomplete". Its edge is what speaks:

| Contract state | Signal                                             | Surface |
|----------------|----------------------------------------------------|---------|
| PREPARING      | POST in flight (chunking, crypto, preview)         | composer busy state |
| RELEASING      | the space is in `relay/status.held`                | faintly breathing 1px edge |
| AVAILABLE      | hold cleared — transport confirmed the hand-over as far as it CAN confirm | stillness |

- The held list is the transport's own verdict (heldTentative,
  held_no_route, dead relay…) — the edge never claims more than the
  node knows.
- A solo space's "nobody to send to yet" is filtered out: there is no
  one to hand to, and an edge breathing forever in a room of one is
  motion with nothing to say.
- Progress never rolls back; a confirmed part stays material.

## Presence — v1.1+, NOT yet built (design constraints binding)

The strongest moment of the metaphor: *I released something into the
space and can watch it become part of the space.* Small calm presence
dots on the card (○ not started · ◔◑◕ receiving · ● here), aggregate
first ("2 receiving · 3 have it").

Binding constraints agreed in advance:

1. **Three independent notions** — `RECEIVING` (bytes en route),
   `HERE` (locally complete), `SEEN` (a person actually opened it).
   `FETCHED != VIEWED`; SEEN is a read receipt and a materially more
   sensitive fact.
2. **Privacy ladder, per space**: default = aggregate counts only, no
   names. `Media presence: Off / Aggregate / Members` as a space
   setting; `Media view receipts` (SEEN) is a separate, explicit
   decision members can see BEFORE it applies to them. Quiet Spaces
   does not become a behavioral surveillance system by default.
3. **Never route-shaped**: no "Anna directly from you / Li via relay".
   Semantic receipts only (`asset_receiving`, `asset_available`,
   optionally `asset_seen`), whatever carries the bytes.
4. **Cheap or explicit**: sender-side knowledge that already exists
   (wants received per certified device, chunks answered) may feed
   `receiving` for free. Anything beyond that becomes a small semantic
   signal (`asset.progress.v1`) with BUCKETED progress
   (started/25/50/75/complete) and local throttling — a big space with
   a big file must not generate a service-traffic storm for a pretty
   UI.
5. SEEN ships only after its own privacy decision. Not in v1.x.

## Implementation notes (v1)

- `markArriving(el, asset)` writes `--arrive` (0..1, real chunks) onto
  the preview element; CSS does the rest. `clients/web-ui/assets/app.js`,
  `styles.css` ("MEDIA ARRIVING", "RELEASE").
- `heldSpaceIds` is read off the `/api/relay/status` poll that already
  feeds the connection chip — Release costs no request of its own.
- The node side of this contract is 0.1.5's honesty work: tentative
  puts, WantHolds with transport words, `reason: no_source`, ride-ahead
  (≤8 MB rides with the frame), route announces. The UI only renders
  what the node already admits.
