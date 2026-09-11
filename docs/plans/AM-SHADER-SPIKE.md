# AM shader spike — `nebula@1`, a fragment shader behind the floor

Status: the spike GRADUATED the same day — see
[ADR-037](../../adr/ADR-037-a-scene-may-be-a-shader-never-a-program.md).
Four shader scenes (`nebula@1`, `aurora@1`, `caustics@1`, `silk@1`) are
the composer's offer; the six flow-field scenes are retired from
authoring and keep rendering for every post that names them. The lab
switch that gated the spike is gone with the spike. What follows is the
spike's own record, kept as written.

## The question the spike answers

"Can an atmosphere be a program, and still be safe to look at?" — the
declarative-DSL / ISF-style option from the plugins conversation. A
fragment shader is code, but of one narrow kind: a pure function of
(pixel, time, seed, params) → colour, no state, no DOM, no network, run
by the browser's GPU process. That makes it the only kind of "code" that
can honestly be called a recipe.

## What was built

- `scenes/nebula.js`: `SCENES.define('nebula@1', {lab:true, …})`. GLSL
  built in (fbm value noise advected by a rotating field, hsl ramp by
  hue/glow, seeded soft stars). Params `hue drift density glow`, permille
  like every scene. Renders into an OFFSCREEN WebGL canvas at a third of
  the stage's resolution; `stop()` loses the context; `webglcontextlost`
  handled. No `document` (Node harness) or no WebGL → `{draw(){}}`, the
  still and the fallback text stand in.
- `brush.js`: `surface.plate(source, alpha)` — the one way a raster
  reaches the stage. The source's MEAN colour is metered (8×8 readback,
  composited over ground) and admitted through the same luma-step law as
  every mark (`MAX_LUMA_STEP`), then drawn with the admitted alpha
  (`MAX_PLATE_ALPHA = 0.9`). A shader cannot flash, whatever it computes.
- `scenes.js`: `lab` flag, `isLab`, `labOpen`; the composer dropdown
  hides lab scenes unless the switch is on or the recipe already carries
  the id.

## Measured (Mac, hidden tab, 720×420 stage, GL 240×140)

| | |
|---|---|
| first frame (compile + link) | 36 ms (310 ms cold on the first page load) |
| warm frame p50 / p90 / max | 1.5 / 5.0 / 10.1 ms |
| stage luma max step per frame | 0.001 (law: 0.007) |

The per-frame cost is mostly the 8×8 readback of the GL canvas through a
2D `drawImage` (a sync GPU→CPU hop). Acceptable on a laptop; on a phone
it is the number to watch — NOT measured yet (phone was not plugged in).

## Honest caveats

- **The harness cannot sweep GLSL.** `flashcheck.cjs` enumerates the
  scene and its params, but in Node the scene is a no-op; the safety
  claim for `nebula@1` rests on the plate law (a raster is admitted like
  a mark), not on a sweep of the shader's output. That is the right
  place for the floor to be, and it is also why the plate exists at all.
- **GPU float differences.** Two devices render slightly different
  clouds for the same seed. The recipe still means the same thing; the
  pixels are not byte-identical, and never were promised to be.
- **Battery.** A shader at 1/3 res is cheaper than it sounds, but it is
  a GPU wake per frame; the `calm` rung halves the clock, `poster` never
  starts it. Phone measurement still owed.
- **GLSL riding a recipe is a SEPARATE decision** (allowlisted ids stay
  the law, ADR-013 inv. 2). The spike proves the render path and the
  floor; it does not open the wire to shader source.

## If it graduates

1. Phone measurement beside `drift@1` (mAh over 10 min, plugged then
   unplugged).
2. A palette hook: the scene should take hue from the recipe palette
   rather than a bare `hue` param, so posters and scenes agree.
3. Drop the lab flag; release note.
