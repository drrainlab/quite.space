# ADR-034: A sweep is a bounded session

Status: accepted (SP-3.2, 2026-08-28)

## Context

ADR-031 built Field on a deliberate privacy model: positions are signed,
TTL'd claims shared **while the map is open**, and silence degrades
honestly — live, stale, unknown. That answers "where are you now?".

A search operation asks a different question: "where did you actually go
during this operation?" A person sweeping a sector needs the device to
keep recording with the screen off, for an hour, through forests and
pockets. That is background location — the thing ADR-031's model
deliberately did not have — and the way it is admitted matters more than
the feature itself.

## The two laws

> **Background location exists only inside an explicitly started, visibly
> active, bounded Field Session.**

> **A Sweep Object owns the meaning of the operation; the detailed
> trajectory is an attached asset, never an oversized Object revision.**

Every clause of the first law is load-bearing. *Explicitly started*: the
session begins with a person's press on a button that says what it does,
from a visible screen — which is also, not coincidentally, what Android
requires of a location foreground service. *Visibly active*: a persistent
notification names the operation for the whole session and carries the
Stop that ends it, from the lock screen if need be; inside the app, the
Field view shows the same session as a banner. *Bounded*: the session
ends — by the person's Stop, by the node finalizing it, or by the node
declaring it interrupted when nobody claims it (§ orphans). Location with
no session and no visible card is structurally impossible: the manifest
still declares **no** `ACCESS_BACKGROUND_LOCATION` — a location
foreground service is Android's while-in-use grant plus a permanent
card, which is exactly the promise's surviving half.

The second law is the Route/C3 lesson applied before the mistake instead
of after it: an Object that carried thousands of GPS points would grow
past every radio budget on the day it mattered most. So three things are
kept separate:

```
Sweep Object   = identity + relations (kind=sweep, parent=sector)
field.track.v1 = the full trajectory                    → an Asset
sweep.completed.v1 = what happened                      → an Event
```

## Who owns the completion

**`sweep.completed.v1` is the canonical completion fact. The Object's
`status` is a render cache; where the two disagree, the event wins.**

Both an Object revision and an event can carry `ended_at`, `result`,
`distance` — and two owners of one truth is how replicas learn to
disagree. The event was chosen because it can be held to the same
measured one-frame invariant as every field event: *finishing an
operation never depends on how large its Object has grown.* A LoRa-only
receiver holding just the event renders the whole sentence — "Sector B3
swept · 13:02–13:47 · 2.7 km · nothing found" — and the asset arrives
when broadband does. LoRa: what happened. Broadband: the full how.

`started_at` inside the event is a deliberate copy for that receiver,
not a second opinion: the Object's creation owns "this began", the event
owns "this is how it ended".

## A gap is a sample, not a silence

`field.track.v1` is a list of tagged samples: `point` or `gap`. A bare
list of points cannot distinguish "GPS was absent for 52 s" from "the
recorder samples once a minute" from "the phone was asleep" from "two
fixes happened to be far apart" — four different claims about the world
that a plain polyline renders identically. Making the gap an item the
reader must CONSUME means a renderer cannot accidentally join across it;
the JS decoder goes one further and only ever returns *segments between
gaps*, so joining is impossible by construction. Exports keep the
honesty: GPX breaks `<trkseg>` at every gap, GeoJSON is a
MultiLineString, CSV carries gap rows.

**The recorder never invents a cause.** The gap vocabulary is closed and
each word has an explicit bar:

- `no_fix` — the platform itself said so (provider disabled/unavailable).
  Silence alone NEVER earns this word.
- `suspended` — sleep proven locally by the same clock pair the core
  trusts: `elapsedRealtime` advances through suspend, `uptimeMillis`
  does not; their divergence over the span IS the time asleep.
- `unknown` — everything else. `unknown` is the classifier working, not
  failing.

A silent stretch beyond 3× the sampling interval is the *trigger* for
recording a gap — never its cause. Distance is haversine within
segments, never across a gap: the system does not measure a line it
refused to draw. And when the host's buffer overflows, the dropped span
becomes a `gap(unknown)` — "I lost them" is not a cause either.

## The result vocabulary, closed

`nothing_found | found | interrupted | undeclared`

`undeclared` is the honest word for "Stop was pressed, judgement was not
given": the completion facts (time, distance, track) are known and
travel immediately instead of waiting for a person to reach a phone; the
judgement arrives later as an ordinary observation. `interrupted` is
never a person's choice — it is the node's word for a sweep nobody
finished.

**`interrupted` does not close the linked task.** A sweep that completed
with a judgement (`nothing_found`, `found`, `undeclared`) marks its task
done; an interrupted sweep did not do the work, and a ✓ on the card
would be a lie. Task states themselves gain nothing: the card vocabulary
(`open | done | dropped`) is closed by the schema validator, and a sweep
in progress is already legible — the Object says `recording` and the
notification is on the person's own screen.

## Orphans: the hybrid

A session can outlive its recorder (the process is killed) or its
recorder can outlive nothing (the phone never comes back). Two halves:

- **The live half.** Android restarts the sticky service; it asks the
  NODE — not its own memory — whether the session still runs
  (`GET /api/sweeps`), and claims it back with a resume whose seam is
  exactly one `gap(suspended, now − last_sample)`. The node authors that
  gap: its own lifecycle is the one thing it may testify about.
- **The dead half.** A node that opens holding a live-state sweep marks
  it suspended and arms a 2-minute grace. Claimed in time → recording
  continues. Unclaimed — including on any non-Android node — the sweep
  finalizes as `interrupted` with the spooled track preserved and sealed
  as it stands. Temporary failure is not permanent refusal, and a track
  already walked is never thrown away.

Start and finalization are both persisted sagas (SweepRecord markers,
re-driven on open): a crash between any two steps leaves neither an
Object without a session nor a session without an Object, and never a
sealed asset without its completion fact.

## After Stop

**No further background location capture or position emission occurs.
Finalization and synchronization of the completed sweep may continue.**
What stops is the recording of where this person is; what may continue
is finishing the record they already asked for — draining queued
samples, sealing the asset, emitting the completion, syncing. A promise
that forbade that would either be broken by the first sync or strand the
sweep unfinished. A Stop whose POST cannot reach the node becomes a
pending judgement, delivered when the core next opens — a person's
verdict is not droppable.

While the sweep runs, ordinary position claims are emitted every ~60 s
with TTL 600 — but only from a fix younger than 90 s: re-emitting an old
fix with a fresh TTL would draw a live dot on a stale point. The sweep
is its own consent scope; ADR-031's "while the map is open" toggle is
untouched.

## The radio budget, measured

`sweep.completed.v1` worst-case (40-rune Cyrillic fallback, full bbox,
32-byte asset id) measures **348 bytes COLD / 343 bytes WARM** in a
compact frame against the RNode 500-byte guarantee — cold meaning the
very first frame of a fresh radio session, before any id interning,
because a sweep's completion is exactly the kind of event that arrives
first. Both numbers are pinned by `transports/compact/sweepbudget_test.go`;
if code and constant ever disagree, the constant loses.

**The coarse polyline (key 9) is deferred with its number**: the warm
remainder fits ~14 raw [lat,lon] pairs — a bbox, not a shape; enough to
be tempting, not enough to be a trajectory. It stays out of v1 rather than shipping as a
drawing that pretends to be a track; the key is reserved.

## No wake lock, in v1

Doze on aggressive OEMs will slow the pump, and the design's answer is
honesty rather than force: suspension is measurable (the clock pair
above), and a measured `gap(suspended)` is data. Whether a wake lock is
worth its battery is a question for the hardware gate
(`scripts/android/sp32-sweep-gate.sh`, the 30-minute screen-off walk and
the forced-Doze probe), not an assumption. If the measurement says the
tracks are unusable without one, that is a v1.x decision made on
evidence, recorded here.

**Measured (2026-08-29, Nothing Phone (1), on water).** Two live
sessions, screen off, no cable: 29 min / 4.0 km / 113 points and
1 h 31 min / 5.0 km / 359 points — mean interval 15.2–15.4 s against
the designed 15 s, median accuracy 3–6 m, and ZERO gaps across two
hours. On this hardware the location-type foreground service keeps GPS
flowing through the whole session; Doze never silenced the pump. The
v1 answer stands on evidence: **no wake lock.** A different OEM may
measure differently — the gap classifier is what makes that a data
point rather than a corrupted track.

## Non-goals

Coverage estimation ("71% of the sector") is a local projection a
renderer may compute and must label as an estimate — it is never signed
truth unless someone separately attests it. GIS analytics stay outside:
Quiet stores the claims; GPX/GeoJSON/CSV exports feed whatever tool
analyses them (ADR-033 — export is a free per-device projection).

## See also

ADR-031 (the field is a map of claims — the model this extends without
touching), ADR-030 (assets named by structures — the track asset rides
the same `block.attached.v1` carrier), ADR-033 (what is frozen —
`field.track.v1` and `sweep.completed.v1` join the `.v1` families).
