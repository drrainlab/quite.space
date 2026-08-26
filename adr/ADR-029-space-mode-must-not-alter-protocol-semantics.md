# ADR-029: Space mode must not alter protocol semantics

Status: accepted (SP-1, 2026-08-26)

## Context

The Space Primitives groom introduces *Space Mode*: a workshop, a farm, a
rescue operation, a studio — the same space, tuned. The temptation this ADR
exists to kill is obvious and recurring: "the workshop mode could just
require every task to have an object", "the rescue mode could reject
observations without coordinates", "farm spaces could replicate telemetry
differently". Each of those sentences quietly forks the protocol per mode.
Six modes later there are six messengers wearing one name, and a frame
valid in one space is invalid in another *for reasons that are not in the
frame*.

The existing seam is `qp.central` — a signed five-value declaration
(`chat|objects|audio|members|telemetry`) in the space character, written at
creation, and (until SP-1) read by no one.

## Decision

A space mode MAY change:

- the **default projection/view** a client opens (central=objects → the
  Objects tab first);
- the **choice of renderer** for a given entry kind;
- the **suggested actions** (composer buttons, kind suggestions, prop
  templates);
- the **ordering and prominence** of UI surfaces.

A space mode MUST NOT change:

- **event validity** — a payload valid under a schema is valid in every
  space of every mode;
- **replication** — what syncs, prunes, or projects publicly never
  consults the mode;
- **identity and authorization** — who may write is policy (ADR-016), not
  mode;
- **transport semantics** — envelopes, custody, admission (ADR-022) are
  mode-blind.

Consequently the kernel never reads `qp.central`. It is parsed in
`terminals` and surfaced through the API as display metadata; the reducers,
the log, the relays and the radio profiles do not know it exists.

The same discipline applies inside SP-1's own records: `Status` on an
object or card is domain-local display state — carried, sorted, shown,
never interpreted by the kernel. There is no `if status == "done"` anywhere
below the UI, because "done" for a lathe, an order and a seedling tray are
three different sentences.

## Consequences

- Rescue/Studio/Farm/Festival are **presets, not forks**: a mode ships as
  a default character + UI vocabulary, and any client that ignores it
  still interoperates perfectly.
- A future mode needing a genuinely new semantic (e.g. geographic
  anchoring) must add it as a **protocol feature available everywhere**
  (SP-3 Places), then let the mode surface it — never gate it.
- Mode can be revised freely without a migration: nothing durable depends
  on it.
