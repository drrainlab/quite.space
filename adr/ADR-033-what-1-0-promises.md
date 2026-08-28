# ADR-033: What 1.0 promises

Status: accepted (1.0.0-beta, 2026-08-28)

## Context

Every version number before this one meant "here is what we built". This
one means something a person can rely on, and the difference has to be
written down before the tag exists rather than discovered afterwards by
somebody whose log stopped opening.

In a local-first system there is no server to migrate. A log written
today is opened by a build shipped in two years, on a device that never
spoke to us in between, possibly carried there over a radio by a third
party. So a version number here is not a claim about polish. **It is a
claim about the formats.** This ADR says exactly which ones, and — just
as importantly — which parts of the product are deliberately NOT covered,
so that "1.0" never has to be quietly reinterpreted.

The beta suffix carries its own precise meaning, stated in §4.

## 1. What is frozen

A frozen format may gain optional fields. It may never change the meaning
of a field it already has, renumber a key, tighten a bound that existing
data satisfies, or remove anything.

**Signed events — 90 schema strings, all `.v1`.** The families:
`message.*`, `card.*`, `membership.*`, `presence.*`, `observation.*`
(noted, value, temperature, position), `block.*` (visual, video, voice,
audio, file, link, live_signal, reaction, attached), `object.*` (created,
revised, archived, restored, attached, annotated), `publication.*`,
`marker.placed`, `checkin.sent`, `receipt.delivery`, `identity.device_*`,
`terminal.manifest.updated`, `resonance.*`, `keep.*`.

**The envelope** (protocol/signal): its key table, authorship marks,
priority lanes, payload encodings, and the rule that an unknown key is
carried, not dropped.

**Content-addressed and domain-separated names**: `qs.object.v1`,
`qs.asset.v1` and its chunk/manifest shapes, `qs.bundle.v1`, the pairing
`qs.pair.*` family, the instrument plane's separator, target derivations.

**At rest**: the data root's layout, the keystore's record arities
(append-only, forward-compat tails), the sealed store, and the backup
container `QSBACKUP1`. A backup written by 1.0.0 restores into every
later 1.x.

**Relay wire**: generation 2, advertising `min=1` — a 1.x relay keeps
serving a client that only knows generation 1, and a 1.x client keeps
talking to a generation-1 relay.

**Radio**: the compact profile's framing and the two-tier discipline of
ADR-031 — every field-specific event and every Field-authored route fits
one RNode frame, worst case, measured rather than assumed.

## 2. What is explicitly NOT frozen

Naming these is the point of the document. Nothing below may be read as
a promise, and changing any of it is not a 2.0.

- **Every HTTP API under `/api`.** It is this build's own interface to
  its own web-ui, on loopback, behind a token. It is not a public API and
  never was.
- **The interface**: views, navigation, Space Mode's ordering, themes,
  the basemap's styling. ADR-029 already says mode is a UI reading of a
  declared character, and nothing below the UI depends on it.
- **Local projections and derived state**: reducer shapes, digests as
  values (the *rule* that a digest is order-independent stays; the bytes
  are free to change when a projection legitimately grows), relay
  selection, attention/QuietRank scoring, the tile cache.
- **Everything ADR-032 calls refetchable world**: basemap tiles, link
  previews.
- **The desktop shell**, which the README already calls experimental and
  which ships unsigned.

## 3. How a v2 schema arrives, when one does

Not by editing a `.v1`. A new schema string, both emitted and understood
during a transition, with the old one still read forever. The admission
gates already refuse what they cannot read rather than guessing, and
RawExtra already carries unknown keys through a fold and back onto the
wire — so an old replica meeting a new event stays honest instead of
lossy. That behaviour is itself part of the freeze.

## 4. What the beta suffix means, precisely

**The formats above are frozen now, not at 1.0.0 final.** The suffix is
not a hedge on §1 — a beta that reserved the right to break the log would
be worth nothing to the person running it, and would make this document a
draft rather than a promise.

What the suffix marks is the reach of our own evidence. The claims here
are proven by construction (tests, measurement, live stands) and by daily
use on the owner's own devices — not yet by a population of strangers on
hardware we have never seen. Between beta and final, the formats hold
still on purpose, so that the only thing that can change is what we know
about them.

The edges the README names — LoRa proven on two boards but young, no
multi-hop mesh, no wake plane for a sleeping phone, unsigned desktop
packages — are product edges, not format reservations. They stay named
there, in the words a person reads before installing.

## Consequences

- A 1.x build opens every log, backup, pass, bundle and device
  certificate that 1.0.0-beta wrote. If that ever fails, it is a bug of
  the highest order this project has, not a version bump.
- The number can now be spent on something real: 1.1 means new capability
  on the same promise; 2.0 would mean the promise itself changed, which
  should be rare enough to be an event.
- The line between §1 and §2 is where future arguments will happen, so it
  is drawn on a principle rather than a list: **what travels between
  devices or survives a restart is frozen; what this device decides for
  itself is free.** A new surface joins §1 by meeting that test, not by
  being important.
