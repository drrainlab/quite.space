# ADR-030: Assets named by structures — edges, lineage, annotations

Status: accepted (SP-2, 2026-08-26)

## Context

SP-1 gave spaces domain objects; the Studio/Label groom asks them to hold
media: a track's mixes, a session's takes, notes at a timecode. Assets in
Quiet are content-addressed (the public id is a hash of the plaintext), so
"version 2 of a mix" is a **different asset with no inherent link back**,
and an asset id written into a structure is just hex — the machinery that
serves, mirrors and retains assets only ever saw ids named by publication
documents and block carriers. Without new law, an object card could point
at a mix no peer will ever serve again.

## Laws

### 1. A record owns only its own pointer; every edge is derived

`Record.Parent` (key 8) models **primary containment** — the one tree an
object lives in. It is not an arbitrary relationship: a track that also
appears on a compilation is a future, separate edge primitive, and this
pointer must not be stretched into it. Children, tasks, observations and
asset edges are all computed at projection time from events that name the
object; a parent record never lists its children, so a child's revision
never touches the parent.

### 2. An LWW register is never evicted

`object.attached.v1` folds to one register per (object, asset); the
candidate ★ is one register per object. Evicting a register to enforce a
bound is the archive/restore ordering hole in a new costume: a late stale
event for an evicted key resurrects it as a fresh register, and replicas
diverge forever. Bounds on register maps are therefore **authoring
bounds** — refusals at the emitting node (200 live edges per object),
advisory across concurrent emitters and harmless when raced past. Because
the maps are eviction-free they are deterministic, and they belong in the
state digest.

### 3. Candidate means "what we are listening to now"

The ★ register is the **preferred current asset for this object** —
nothing more. It is not "approved" (a future member-signed primitive,
SP-3 Creative Presence) and not "released" (a future lifecycle). An event
that does not mention the candidate does not move it: a relabel must
never steal the star. Renderers keep the three words apart.

### 4. Lineage lives on the edge

`Supersedes` names the previous version's asset. Chains are derived with
a seen-set: duplicate parents become siblings, cycles terminate, the
walk never hangs. "Current" = candidate if set and live, else the newest
head — computed role-blind, because role (mix, take, stem) steers
renderers and never the kernel, the same law Status obeys (ADR-029).

### 5. A live structural reference keeps an asset alive; commentary never does

> live structural reference → asset stays live;
> annotation → does NOT keep asset alive.

This is a law of the whole system, not a detail of SP-2: any future
structure that names an asset inherits both halves. Concretely:

- **Emit**: a structure may only name an asset this space's index holds
  (`spaceAssetOK`), checked before signing — checked bytes are signed
  bytes. An edge to an unservable id would be a beautiful card over a
  dead file.
- **Retention**: `ObjectLiveAssetIDs()` — every hex named by a live,
  non-detached edge, plus record covers — joins the publications' live
  set in the projection builder. Old carriers of pinned assets survive
  MaxAge (and still degrade oldest-first under budget pressure — never
  brick). A superseded-but-attached mix stays pinned: it must remain
  playable. A detached edge stops pinning.
- **Mirror custody**: the same set joins the keepalive fetch walk, so a
  mirror holds what the space's objects still name.

### 6. Annotations are bounded, immutable, and prunable

`asset.annotated.v1` is a **universal** media annotation with exactly two
meanings: without `PositionMs` it is about the whole asset; with it (0 is
a legal instant) it is a point-in-time note. No ranges, no regions, no
edits, no threads in v1. Timelines are bounded per asset under the
observation eviction law (newest 200 by (CreatedAt, Clock, EventID),
`AnnotationEvicted` counter); dedupe is by EventID only — the
AnnotationID is a handle, and first-sight-wins by handle would be
arrival-order dependent once eviction starts. The events age out of
public projections like messages: the structural truth lives on edges.

## Deferred, stated as limitations

- **Preview allowlists** do not know object-named assets yet — transient
  public previews do not render object cards, so nothing lies today.
- **Cover emit validation** stays off: SP-1 shipped Cover inert, and
  turning refusal on now would silently break existing flows. Revisit
  when covers render.
- Annotations register no stable target (not keepable/reactable). The
  future path is `SHA256("qs.annotation.v1" ‖ annotation_id)`.

## Consequences

A mirror bootstrapping from an aged projection sees fewer old annotations
— already true of observations, and accepted for the same reason. Mixed
builds treat SP-2 events as Unsupported opaque (ADR-009); digests diverge
only for histories that actually contain SP-2 events.
