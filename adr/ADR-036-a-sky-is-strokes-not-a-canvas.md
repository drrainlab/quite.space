# ADR-036 — A sky is strokes, not a canvas

**Status:** accepted (2026-09-05) · **Wave:** SK-1

## Context

People wanted a shared drawing inside a conversation — a whiteboard for
brainstorms and fun. A whiteboard as usually built is a *document* that
several hands edit at once: operational transforms or a CRDT library,
conflict rules, live cursors, a "who is drawing" strip. All of that is
weight this protocol does not carry and telemetry this project refuses.

## Decision

There is no canvas object. A **sky** is one ordinary message
(`block.sky.v1`, with a fallback line so a client that has never heard
of skies still shows "a shared sky"). Every gesture is its own small
signed event (`sky.stroke.v1`) naming the sky, carrying quantised points
(a 128×128 grid, one byte per coordinate, at most 256 points) and one
of three brightness levels. An erase is a stroke event naming the strokes
it removes.

The reducer projects the set:

1. **Strokes commute.** The picture is the union; order matters only for
   the film, and the film order is `(logical clock, event id)` — the same
   on every replica, whatever order the events arrived in.
2. **An erase removes only the eraser's own strokes.** Authorship is
   sovereignty; another hand's erase of your stroke is ignored everywhere.
   An erase that outruns the stroke it names is remembered and lands
   when the stroke arrives.
3. **A sky cools.** Past 4000 strokes further strokes are counted, never
   drawn — the same eviction honesty as observations and annotations —
   and the interface says so.
4. **The colour is the person.** No palette: every client derives the
   hue from the author's principal, so who drew what is visible without
   a legend. Brightness is the only knob.
5. **Nothing says who is drawing now.** A stroke is sent on pen-up, never
   while the finger moves; the picture updating is the only presence.
   "Watch how it was drawn" replays the log — a canvas with memory is the
   reimagining, not a feature bolted on.

## Consequences

- Zero relay or transport change; old clients see a message with a
  fallback and count the strokes as unsupported schema — nothing breaks.
- Memory policy applies as to any event: under `private_history` a
  newcomer sees a sky with holes, and the honesty line already says so.
- A sky is not editable as a whole: no layers, no move, no fill, no text
  on the canvas (text is the chat). Those are refusals, not omissions.
