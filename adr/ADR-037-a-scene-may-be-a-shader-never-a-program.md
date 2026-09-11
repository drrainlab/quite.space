# ADR-037 — A scene may be a shader, never a program

Status: accepted, 2026-09-11. Supersedes nothing; extends ADR-013
(atmosphere invariants) and the AM wave's BRUSH floor.

## Context

The atmosphere scenes shipped in the AM wave were one flow-field engine
with six presets, drawn through BRUSH's four canvas verbs. They were safe
by construction — every mark admitted against a luminance-step law — and
they were mild by construction too: a brush with four verbs paints motes
and washes, not skies.

The plugins conversation asked whether an atmosphere could be a
*program* a person installs. The answer stayed no: a recipe is an
allowlisted scene id, a seed, a palette and permille parameters, and an
implementation never arrives from a space (ADR-013 invariant 2). But one
kind of program is narrow enough to be worth a second look. A fragment
shader is a pure function of (pixel, time, seed, palette, params) →
colour: no state, no DOM, no network, no clock but the one it is handed,
run by the browser's GPU process. It cannot read the post, the person or
the room. The only thing it can do is be bright — and brightness is
exactly what the floor already governs.

## Decision

1. **A scene may be a fragment shader.** `shader.js` is the one place
   WebGL is spoken; a scene file is GLSL defining
   `vec3 scene(vec2 uv, vec2 q, float t)` plus a label and parameters.
   The GLSL ships with the build. A recipe still carries an id, a seed,
   a palette and parameters — never shader source. Whether source may
   one day ride a recipe is a separate decision this one does not open.

2. **A shader reaches the stage only through the floor.** It renders into
   an offscreen canvas at a third of the stage's resolution, and the frame
   is admitted by `BRUSH.plate`: the frame's mean colour is metered and
   admitted by the same luminance-step law as any brush mark, then drawn
   at the admitted alpha. A shader cannot flash, whatever it computes.
   The plate's alpha is bounded (`MAX_PLATE_ALPHA`), so even an admitted
   frame is a soft replacement of the last one, never a cut.

3. **The meter is a measurement, not a pick.** The 8×8 readback that the
   floor rests on is taken in two stages at the canvas's best smoothing,
   because a single 90× downscale samples sparsely, and a sparse sample
   of fine bright lines is noise. Measured before the change: a caustic
   overshot the step law by 13%; after: every shader scene within it.

4. **A scene reads the recipe's palette.** `surface.palette` exposes the
   colours as values, so a shader paints with what the poster shows.
   The canvas verbs still take an index — a scene painting through them
   cannot invent a colour — and the plate law bounds a raster's luminance
   either way.

5. **Retired, not removed.** The six flow-field scenes are `retired`:
   the composer no longer offers them, and nothing else changes. A recipe
   that names one keeps its picture on every device; the harness still
   sweeps it. A scene id, once published, is a promise to every post that
   names it (ADR-013 invariant 1). The default for a new atmosphere is
   `nebula@1`.

6. **No WebGL is a missing scene.** No document (the Node harness), a
   document without canvases (the DOM stub), or a device without WebGL:
   the scene admits nothing and the still plus the fallback text stand
   in, exactly as for a scene this build does not have. A reader on such
   a device loses motion, never meaning.

## Consequences

- The flash harness (`flashcheck.cjs`) enumerates shader scenes and their
  parameters but cannot execute GLSL in Node: their sweep is vacuous. The
  safety claim for a shader scene rests on the plate law, verified in a
  real browser against a real readback (this ADR's numbers), not on a
  sweep of the shader's output. That is the correct place for the floor
  to be — it is why the plate exists — and it is a thing to say out loud.
- GPU float differences mean two devices render slightly different clouds
  for one seed. The recipe means the same thing; the pixels were never
  promised to be identical.
- Cost: a shader frame at a third resolution is 1.4–1.6 ms p50 on a
  laptop, the same as a flow-field frame (1.9 ms) — the shared cost is
  the readback, not the picture. Phone battery beside the old scenes is
  still owed a measurement.

## Measured (Mac, hidden tab, 720×420 stage, plate 240×140, 150 frames)

| scene | first frame | p50 | p90 | max luma step (law 0.007) |
|---|---|---|---|---|
| nebula@1 | 17.8 ms | 1.4 | 3.4 | 0.0058 |
| aurora@1 | 5.0 ms | 1.4 | 3.2 | 0.0067 |
| caustics@1 | 43.2 ms | 1.6 | 4.4 | 0.0068 |
| silk@1 | 4.9 ms | 1.4 | 3.2 | 0.0066 |
| drift@1 (retired, for scale) | 11.4 ms | 1.9 | 3.9 | 0.0001 |

First frames include shader compile and link; the very first on a cold
page was 310 ms.
