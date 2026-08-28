# SP-3.2 — Field Sessions / Sweep Recording

*Status: proposed, and ready for repository-first grooming (2026-08-28).*
*Origin: owner + site-session discussion after 1.0.0-beta.1; refined by owner
to avoid repeating the Route/C3 oversized-revision problem.*

*Owner's review pass, folded in: tasks gain no new state (the card
vocabulary is closed and would refuse one); gaps became samples rather than
silences; Stop forbids further capture, not finishing what was started; and
completion has ONE owner, chosen here rather than discovered later.*

## The two laws

> **Background location exists only inside an explicitly started, visibly
> active, bounded Field Session.**

> **A Sweep Object owns the meaning of the operation; the detailed trajectory
> is an attached asset, never an oversized Object revision.**

Current Field sharing answers "where are you now?". Sweep answers "where did
you actually go during this operation?". They are different questions and
stay different mechanisms.

## Context

Field today shows signed position claims only while the map is open. That is
the deliberate privacy model:

```
explicit sharing → signed TTL → silence → live → stale → unknown
```

Search operations need a different scenario: a person explicitly starts a
time-bounded field activity, after which the device keeps recording the route
with the screen off. This is **not** background tracking — it is a recording
session with a visible lifecycle. Precedent for background operation already
exists (radio-mode TTS speaks on a locked screen); what is new is only the
declared, bounded session.

## UX

On a sector/Place: `[ Начать свип ]`.

While recording, Android runs a foreground service with a persistent
notification:

```
Quiet · Sweep in progress
Sector B3
24 min · 1.2 km   GPS ●
[ Stop ]
```

Inside Quiet:

```
SWEEP · Sector B3
────────────────────
● recording
24 min · 1.2 km · Robert
[ Завершить ]
```

Screen can go off; leaving Field is fine — the session is its own explicitly
started action, not implicit location sharing. After Stop:

```
SWEEP · Sector B3
────────────────────
✓ completed
42 min · 2.7 km · accuracy median 8 m
result: nothing found
gaps: 2 · total 48 sec
```

**After Stop, no further background location capture or position emission
occurs. Finalization and synchronization of the completed sweep may
continue.** The distinction is the whole point: what stops is the recording
of where this person is. What may continue is finishing the record they
already asked for — sealing the track asset, emitting the completion event,
attaching the asset, and handing it to whichever bearer appears later. A
promise that forbade that would either be broken by the first sync or would
strand the sweep unfinished on the device.

## Wire / model

Three separated things:

```
Sweep Object   = identity + metadata + relationship to sector
Sweep Track    = full detailed GPS history        → Asset
Sweep Summary  = compact geographic projection
```

```
Sector B3
    └── Sweep #4                       Object   — identity and relations
           ├── kind=sweep · name · parent=sector
           ├── operator
           └── track.(cbor)            Asset (SP-2 Object → Asset edge)

        sweep.completed.v1             Event    — what happened
           started_at · ended_at · distance · result · bbox · track asset id
```

The Object never contains thousands of GPS points — this is exactly the
Route/C3 lesson. Storage-wise nothing fundamentally new is required: SP-2
already has the Object → Asset edge.

### Who owns the completion — decided, not left open

Both an Object revision and a completion event can carry `ended_at`,
`result`, `distance`. Two owners of one truth is how replicas learn to
disagree, so this slice picks one **before** grooming:

> **`sweep.completed.v1` is the canonical completion fact. The Sweep Object
> is the container of identity and relations, and its rendering PROJECTS the
> event.**

Why this way round rather than "the Object revision is the state": the
completion event can be held to the same measured one-frame invariant as
every other field event, so *finishing an operation never depends on how
large the Object has grown*. An Object that has collected observations,
edges and a long name would otherwise make its own completion unsendable on
the day it matters most.

It is also the shape this codebase already runs on:

```
Object = the thing, or the operation
Event  = a fact that happened to it
Asset  = the heavy evidence
```

Two consequences to hold in the implementation:

- The Object's `status` (`recording` → `completed`) is a CACHE for list
  rendering, never the source. A replica holding the event but not the
  revision still renders the sweep as completed; where the two disagree, the
  event wins.
- `started_at` inside the event is a deliberate copy so that a LoRa-only
  receiver needs nothing else to render the sentence. It is not a second
  opinion about the start: the Object's creation owns "this sweep began",
  the event owns "this is how it ended".

And it is what makes the degradation below honest rather than lossy:
**LoRa: what happened. Broadband: the full how.**

### Canonical track representation

On-device canonical form is compact, not GPX:

```
field.track.v1
  started_at
  samples [                                  ← ordered; each is ONE of:
    point (dt, lat_e7u, lon_e7u, accuracy)
    gap   (dt, duration_ms, reason?)         ← no_fix | suspended | unknown
  ]
```

**A gap is a sample, not a silence.** A bare list of points cannot tell these
apart:

```
GPS was genuinely absent for 52 s
the recorder samples once a minute by design
the device was suspended
two fixes simply happened to be far apart
```

They are four different claims about the world, and a format that renders
them identically forces the reader to guess — which is the failure the next
section forbids. Making the gap an item the reader must CONSUME means a
renderer cannot accidentally join across it: there is no absence to overlook,
there is a thing in the stream saying "here I did not know".

`reason` is the recorder's own claim and may be `unknown`. A recorder that
does not know why it stopped hearing says so; it never invents a cause.

GPX / GeoJSON / CSV are **export projections** (Export → GPX / GeoJSON / CSV).
Per ADR-033: wire/storage truth ≠ export format; export is a per-device
projection and stays free.

### Honest gaps — a data principle, not a rendering choice

Never interpolate:

```
14:31 ●
14:32 ●
        GPS unavailable · 52 sec
14:33 ●
```

On the map the gap is drawn as a gap (`───── ····· no fix ····· ─────`).
The system never draws a line through a forest because it wants a continuous
polyline. "The field is a map of claims" extends to trajectories.

## LoRa

While the sweep runs: ordinary position claims — small, TTL'd,
radio-friendly. On completion, the canonical completion fact itself — not a
summary derived for radio, which would put the truth somewhere the radio
cannot reach:

```
sweep.completed.v1
  sector/object id · started_at · ended_at
  distance · result · bbox · track asset id
```

optionally plus a very coarse simplified polyline — only if it passes the
same measured one-frame invariant as every field event. A LoRa-only receiver
then knows:

```
Sector B3 ✓ swept · Robert · 13:02–13:47 · 2.7 km
detailed track unavailable yet
```

When LAN/Internet appears, the track asset syncs and the map becomes
detailed. Semantic degradation in the good sense, and the reason the
ownership decision above went the way it did.

## Task / Observation integration

```
Sector B3   TASK ☐ sweep western edge

→ sweep started:   Task remains open
                   Sweep Object created, status recording

→ sweep completed: Task → done
                   Sweep Object → completed
                   Observation "western edge searched, nothing found"
                   track asset attached
```

**No new task state.** A card's lifecycle is `open | done | dropped` and the
schema validator refuses anything else (protocol/schemas/schemas.go:699,711),
so "Task → active" would not merely be new semantics — it would not encode.
A sweep in progress is legible without it: the Sweep Object says `recording`
and the notification is on the person's own screen. If `in_progress` ever
earns its place, that is an evolution of Tasks with its own reasons, not a
side effect of this slice.

In conversation: `# Sector B3`, `# Sweep 04` — a link to the operational
object itself, not a screenshot of a map.

## Coverage (explicit non-goal for this slice)

`Sector geometry + Sweep tracks[]` lets a local renderer later compute
estimated coverage / overlap / gaps / distance / time. That is a projection,
not signed truth — the UI must say `estimated coverage 71%`, never
`71% searched`, unless someone separately attested the fact.

## Export for analysis (LLM or any tool)

```
sector.geojson · sweep-robert.gpx · sweep-katya.gpx
markers.geojson · checkins.csv
```

Questions like "which parts of the sector are uncovered", "where did two
groups duplicate a route", "which hazard markers sat near the paths" are
answered by external tools over honest primary events. Quiet does not become
a GIS analytics suite; it stores the claims, analysis is a replaceable
projection.
