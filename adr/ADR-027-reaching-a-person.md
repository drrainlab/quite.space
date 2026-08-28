# ADR-027 — Reaching a person: the knock, and the right to answer once

Status: accepted (2026-08-22)
Companion doctrine: ADR-012 (a pass is authority to REQUEST entry),
ADR-023 (inability is never success, never silence),
ADR-024 (relationships belong to the principal)

## The invariant

> **Nobody may open a conversation with a person without that person's
> consent, and refusing must cost the refuser nothing — not a word, not
> a second look, not a reason they have to keep giving.**

Consent to be in a room together is not consent to be written to alone.
The project has said the first half in code for a long time (a pass is
authority to *request* entry; pending has no access) and never said the
second half at all: there is no way to reach a person, and therefore no
way to decline being reached.

## Why this note exists

Everything about who may reach whom is currently unwritten, and the gap
is visible in the product as a missing verb:

- A one-to-one conversation is not a thing. It is a **rendering**: a
  space with exactly one other person (`node/display.go:97`
  `isDisplayDyad`). Nothing declares it; nothing can be addressed to a
  person. README says it plainly: "a direct message is a presentation
  mode, not a data type."
- The member card is inert. There is no "write to this person" anywhere
  in the interface, so the only way to a private conversation is to
  build a room and hand somebody a link.
- The door that DOES exist belongs to a LINK, not a person
  (`approval:"host"` on a `passRecord`), and its memory of a refusal is
  keyed on one `request_id`: "a settled refusal stays settled"
  (`node/pass.go:341`) means *that knock*, not *that person*. Ask again
  with a fresh id and the door forgets.
- There is no per-person policy of any kind: no block, no ignore, no
  "who may write to me". The nearest thing, `qrMuteSpace`, is honest
  that it is not one — "its messages stay exactly as they are, you just
  will not be told about them."

So a person can be reached only by accident of a shared link, and once
reached can only leave the room. That is a worse answer than every
messenger gives, and it is not the answer this project would choose.

## Decision

### 1. A knock is a sealed envelope in the person's mailbox

Not an event in the room they share. Writing "I would like to talk to
you privately" into a shared log tells everyone in the room that you
asked — a fact that belongs to two people. The knock rides the same
shape as an identity-plane message: HPKE-sealed to each of the
recipient's certified devices, left at their relay mailbox, signed by
the knocking DEVICE and carrying its principal's certificate chain.

    knock = { from: principal + cert chain,
              via:  the space we both belong to (the acquaintance claim),
              line: one short sentence, the reason,
              pass: authority to REQUEST entry into a fresh dyad space,
              expires_at }

The pass rides with the knock deliberately: it carries no epoch keys
(ADR-012 invariant 1), so a stranger holding it gains nothing until the
recipient decides. The recipient's YES is an ordinary pass use, through
the acceptance path that already exists.

### 2. The acquaintance floor: a shared PRIVATE space

A knock is admitted only when the knocker is, at the moment of
delivery, a member of a private space the recipient is also in — and the
recipient's own device verifies that from its own log, never from the
knock's word for it. A public directory is not an introduction: being
visible in a catalogue everybody can read would otherwise make everybody
reachable, which is the property the catalogue exists to avoid.

### 3. Three answers, and only one of them is remembered

    let in      → the pass is used; the dyad opens; both sides hear it
    not now     → a sealed Decision travels back; nothing is remembered
    do not ask  → a sealed Decision travels back, AND a refusal is
                  recorded against the PRINCIPAL

The decision is the existing one (`terminals/decision.go`): a distinct
HPKE info string so no parser can read a decline as a grant, no keys, no
manifest — "a sentence", carrying the recipient's own words verbatim and
no blame they did not write.

### 4. A recorded refusal answers on the person's behalf, forever

This is the load-bearing choice, and it is neither silence nor a
notification of blocking.

After "do not ask", further knocks from that principal are answered by
the DEVICE with the same sentence the person wrote once. The person is
never told again. The knocker always gets an answer — this project does
not do silence (ADR-023) — but never a NEW answer, so nothing can be
learned by knocking twice:

> They cannot distinguish "she declined again" from "she never saw it",
> because those are the same fact: no.

An unanswerable knock is worse than a refusal, and a refusal that has to
be repeated is a way of making somebody keep paying attention to the
person they refused.

### 5. The refusal belongs to the person, not to the device

By ADR-024 a relationship is principal-scoped: declining on the phone
must silence the laptop. The refusal record therefore travels the
identity plane to the person's certified siblings, exactly as a space
grant does, and is derived-and-held the same way.

This is what makes reusing the pass door impossible as it stands:
`passRecord.handled` and its entry queue are device-local saga state.
A person-level refusal that lived on one device would be a promise the
person's other devices break.

### 6. Bounds, taken from the door that already works

| bound | value | why |
|---|---|---|
| pending knocks | 20 per recipient | the door's own `maxPendingPerPass`; a queue nobody can drain is a memory-growth vector |
| knock TTL | 24h | the approval TTL: "an hour is dishonest when somebody may be asleep" |
| one live knock per (principal, recipient) | re-knocking is the same knock | a second envelope while one waits is not a second question |
| the line | ≤140 characters, no markup, no links | it is a stranger's text shown to a person; it is rendered as somebody else's words, like a quotation |
| refusals kept | bounded, oldest pruned | a refusal list is not an archive; the floor (§2) is what stops strangers, not the list |

### 7. What a knock sounds like

The sound language (Quiet Chimes) already forbids loudness as a rank, so
the two moments differ in shape rather than volume:

- **a knock arriving** — the smallest thing the voice can say: the
  `tick` grain, lower and shorter. An unanswered question is not a
  demand, and it obeys the ordinary arbitration and the person's own
  switches like everything else.
- **a conversation opening** — a new tier, and the only one that plays on
  BOTH sides at once: the signature strike answered DOWNWARD an octave,
  with a fifth settling after it. Two voices meeting. Longer and fuller
  than a signal, and — by the same law as everything else — never louder.

## Consequences

- Accepted: a person can be reached by anybody they already share a
  private room with, once, until they say otherwise. That is the price
  of being reachable at all, and the floor plus the single-answer rule
  is what keeps it from becoming a channel for strangers.
- Accepted: a knock is not deniable. The recipient's device holds a
  signed envelope naming who knocked. This is deliberate — an anonymous
  knock is a strictly worse object.
- Accepted: a refused knocker cannot tell refusal from absence. That
  ambiguity is the feature, and it is documented here so nobody later
  "fixes" it into a delivery receipt.
- Not decided here: whether a person may ever be reached with no shared
  room at all (an address handed over in person, out of band). The
  quicklink already covers that case by making a ROOM, and this note
  does not extend it to people.
