# UI-2 — the light shell

**Why.** The owner, 2026-09-18, beside a plain dark-messenger mockup: "our
interface looks very complicated; lighten it, roughly like this". Counted
before touching anything: about twenty-five controls on screen before the
first message (header ten, a two-storey room bar with up to seven buttons
plus tabs, composer five); the right pane open by default on a desktop;
four header actions each with a tilt, an extruded edge, a breathing halo and
a hue; conversation set in 12.5px mono; ~30px reserved inside every bubble
for reactions nobody had left, plus a line for actions; four empty sidebar
sections each explaining itself in a paragraph.

**The rule of the wave.** Nothing loses a capability. The wave decides what
may be VISIBLE AT REST. One accent, and it means "the thing you came to do".
No borders on the shell. The light shell is the default, not a preset; the
two themes and four presets tune tokens on top of it.

**Owner's decisions.** Slices on the live stand, no design canvas first.
Own messages on the LEFT in one column (own commit, revertable alone).
New default for everybody; the old look is deleted, not kept behind a switch.

## What changed, by region

| region | before | after |
|---|---|---|
| header | burger, lockup, relay chip with words, 4 relief pills, radio text, gear, "me" pill, fingerprint | burger, lockup, a mark and a light (words return in any state but healthy), one tinted "create" pill, three flat text actions, gear, a round avatar |
| sidebar | search box, six headings, four paragraphs of empty-state advice | search pill, only the sections that have something; all of them while searching; "+" on Spaces |
| room bar | two storeys, up to 7 buttons + 4 tabs + overflow | one row: face · name · N people · live chip · 3 tabs + overflow · make · invite · ⋯ |
| message | 18px glyph for others only, mono text, stamp in bubble, reaction row inside every bubble, a line of word-links under it, own on the right | 32px face for everyone, name + time on a head line, sans 14.5px, reactions under the bubble and absent when empty, a toolbar of marks in a side lane on hover/tap, one column |
| media | always inside a bubble | a picture or video that stands alone has no bubble; caption small and under it |
| composer | full-width band, paperclip and mic behind rules, select-shaped presence, the word "send" | one rounded bar: +, words, presence ring, mic, round send |
| right pane | third column, open by default | a slide-over with a scrim at every width, closed on load; Escape, scrim or ✕ close it |

## Refused, because it does not exist

The mockup drew these; drawing them would promise something the app cannot do.

- **"Search spaces, messages…"** and a search icon in the room bar — there is
  no message search; the navigator searches names and says so.
- **Inbox / Starred / Archive** in the sidebar — the *signals* button already
  IS the cross-space attention inbox; Keep/Shelf is per-space and SHARED, so a
  private-sounding global "Starred" would misstate both scope and audience;
  there is no archive, and "delete from this device" must not hide behind a
  soft word.
- **A presence dot on the avatar** — presence is declared per space with a
  TTL (ADR-017); a global dot would misstate it.
- **A sticker button** — until the Sticker Lab research says go.
- **Hiding tabs for plain chat spaces** — the strip is built once per visit so
  that navigation never rearranges itself under a working hand, and the
  guides name the tabs.

## Kept on purpose (ADR-023: quieter, never silent)

The connection chip speaks in every state but healthy. Our own stamp stays
in the bubble because it carries the delivery mark. `#honesty`, `#sendNote`
and the outbox strip are restyled, never hidden while they have content.
With the panel shut, instruments stay readable from the room bar
(`#nowChip` — the card's own freshness line, stale said before any number).
The fetch halo and the arrival orb still paint around bubble-less media.

## Found on the way

- **"Place marker" toggled read receipts** — two handlers appended with the
  labels 254/255 at positions 272/273; the markup followed the labels. Fixed,
  and `injection.cjs` now refuses a label that is not its position.
- **placeNavExtras** returned the four header actions to the wrong side of the
  gear after one trip across the 600px breakpoint.
- **A pane parked off the right edge widened main's scroll area**; one click
  slid the whole app a column to the left. `main` is `overflow: clip` and a
  folded pane is `visibility: hidden`.
- The stand's fixture lived in /tmp and the OS swept its keys after three
  days. It lives in `~/.quiet-stand` now (`mkstand.py`, `restart.sh`).

## Where the rules live

One delimited block at the end of `styles.css` (`UI-2: THE LIGHT SHELL`),
phone exceptions gathered once at its end. Superseded rules were DELETED
(the relief system, both hue tables, the minimal-mono special case, the old
`.mk` links, the corner-notch radii, the right-aligned own rows), not reset.
Tokens: `--av-row/-head/-msg`, `--gap-*`, `--text-body/-meta/-cap`,
`--bubble-radius`, `--bar-radius`.

## Not done here

People's photo avatars (still a protocol surface, UI-1's deferral stands).
A cross-space shelf. Message search. Title-over-subtitle stacking in the
room bar. The Sticker Lab research follows this wave
(`~/.claude/plans/luminous-coalescing-nova.md`, Part B).
