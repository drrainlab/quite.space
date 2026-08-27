# ADR-031: The field is a map of claims

Status: accepted (SP-3, 2026-08-28)

## Context

SP-3 gives spaces a geographic layer: places, positions, markers,
routes, check-ins, and a map. The temptation this ADR exists to kill is
the map that lies: "Robert is here" when all anyone holds is a signed
statement from forty minutes ago; "search this heading" computed from a
staleness nobody surfaced; "person in danger" inferred from silence.
The map is not a window onto reality. It is a **local projection of
signed claims**, and every law below keeps it honest about exactly that.

## Laws

### 1. Geo is an author's claim, with the author's stated accuracy

A coordinate on the wire (protocol/geo: offset-shifted fixed point,
~1.1 cm resolution, no floats, one representation per point) is what
somebody SIGNED about where something is — never a measured truth. The
accuracy field is part of the claim, not a quality score the system
assigns. Renderers speak accordingly: "last signed statement about
Robert's position: 59.3321, 18.0412 · ±8 m · 6 min ago".

### 2. Position privacy: an explicit act, a signed TTL, honest ageing

Sharing one's position is an explicit per-space act, and every position
carries a signed `expires_at` — a position that never expires would be
a claim about the future, and the wire refuses it. Turning sharing off
requires no "I stopped sharing" event: knowledge simply ages, and the
map degrades honestly —

    ● live (age)   ◐ stale (age)   ○ unknown (last contact N ago)

— last KNOWN state plus the age of the knowing, never a fabricated
present. A rescue-flavoured space may SUGGEST enabling sharing; no mode
ever enables it for a person (ADR-029). There is no background
tracking in v1; the sharing toggle's label says literally what the
system does («Делиться позицией, пока карта открыта»), and a future
background mode must change the label with the behavior.

### 3. Overdue is the viewer's arithmetic, never a verdict

Check-in cadence is a LOCAL display convention (a network-declared
cadence would smuggle alarm semantics into the protocol). When a
check-in is late the interface says, exactly:

    check-in expected every 4h — last seen 6h ago, overdue

and NEVER "person in danger". The system does not draw conclusions the
data does not contain.

### 4. SOS is the flag; text is only the human fallback

`checkin.sent.v1` carries `sos` as a canonical bool, encoded only when
true. **Automatic fallback for SOS MUST be emergency-semantic. Protocol
consumers MUST use `sos`, never infer emergency state from fallback
text.** The wire does not parse the meaning of Unicode prose — no
validator inspects "✓" or "🆘"; the node's emit path fills an empty
note with "🆘 SOS" when sos is set (else "✓ check-in"), and an author's
own words with sos=true are a perfectly good SOS. SOS is the first use
of the declared `PriorityEmergency` lane.

### 5. The two-tier radio law (revised by measurement)

The plan's first draft said "every field event rides one frame of the
narrowest bearer". Measurement (transports/compact/routebudget_test.go,
warm TN-2B compact, full signed envelopes) corrected it:

- a route revision's envelope FLOOR — signature, metadata, revision
  scaffolding, before the first waypoint — is ~246 B and can NEVER fit
  one Meshtastic frame (233 B);
- chain head-of-line blocking (C3) bites only at ErrTooLarge
  (~1536-2155 B); a two-frame event just rides two frames and blocks
  nothing.

So the law has two tiers, both test-enforced:

- **GUARANTEE — one RNode frame (500 B)**: every field event, at its
  caps, with worst-case incompressible Cyrillic text, fits one RNode
  frame — far below every ErrTooLarge ceiling, so **a conforming field
  event can never strand its author's SOS behind it**. Measured:
  marker worst-RU 436 B; checkin worst-RU+SOS 416 B; route at its
  bound 499 B.
- **IDEAL — one Meshtastic frame (233 B)**: position (210 B) and the
  default check-in (212 B) ride a single frame of the narrowest bearer.

Consequences of the guarantee, all measured and pinned by tests:
`MaxRoutePoints = 21` (full Field-authored revision with a 32-rune
Cyrillic name), `MaxMarkerTextRunes = 120`, `MaxCheckinTextRunes = 120`
(a Cyrillic rune costs two bytes; 200 runes would breach the frame).
The route bound means **ErrTooLarge on a conforming SP-3 route is a
bug — a broken invariant, never expected behavior**. Generic object
revisions (8 KiB records) remain subject to C3 and are NOT claimed
radio-express-safe; the future express plane (radiopeer/beacon-shaped,
out of band of author chains) is the named direction, not this wave's
work.

### 6. Route is intent; movement is observations; a track is neither

    Route       = compact operational INTENT (BASE → WP1 → ridge → RV)
    Breadcrumbs = observation.position.v1 — a stream of facts
    Track log   = a future bulk artifact/asset

A route is a Record (kind=route) with an ordered Path — revised through
the ordinary 409 machinery, never mutated by GPS. The Field authoring
profile keeps route records small (short names, no summary); generic
objects keep their SP-1 limits.

### 7. A marker is a historical claim, optionally with an active window

`marker.placed.v1` is immutable in v1. Its optional `expires_at` is a
PAYLOAD field for display honesty — "this hazard claim stops counting
as active then" — never the envelope's expiry: a custodied marker must
arrive and stay in the log, because «мост был опасен в 14:32» and «мост
опасен сейчас» are different sentences and both deserve to survive. The
UI has no right to render an expired marker as a present danger; it
shows history, dimmed. Markers ride the default Message lane — that
silence in the priority switch is a decision, recorded here.

### 8. Battery lives in the check-in, and why that is not a betrayal

observation.noted.v1's line in the sand (no structured values in the
human prose channel) stands. A check-in is not that channel: it is a
structured, machine-ish radio event, and its optional battery_pct —
key presence IS the declaration, an honest 0% stays expressible — is
exactly the kind of field that primitive exists to carry.

### 9. Bounds said out loud

Circles, not polygons (point + optional radius ≤ 100 km) — v1 proves
the vertical without a geometry library. No altitude. Positions fold as
LWW per source terminal with an EventID tiebreak (the presence twin's
equal-timestamp arrival-order hole, closed); freshness ladders are
API-layer arithmetic over signed expiry and never enter the digest.
`maxForwards` for positions starts at presence's 1; the multi-hop
hardware gate decides whether operational positions earn 3 — the
decision lands here after measurement.

## Consequences

A team can lose the internet, then the LAN, and keep a shared,
honestly-aged picture of who was where, what was claimed about the
terrain, and what the plan was — over single LoRa frames. What the
system will not do is pretend: no realtime illusion, no inferred
emergencies, no immortal hazards, no invisible tracking.
