# Quiet Signal — the project's own typeface

A wide, square-shouldered monoline grotesque, Latin + Cyrillic, built from
skeletons rather than drawn: every glyph in `glyphs.py` is a few strokes on
a 1000-unit grid, and `engine.py` grows the weight around them at build
time. The same skeleton at stroke 92 is the Regular, at 150 the Bold; the
weight IS the family axis.

    python3 tools/typeface/build.py clients/web-ui/assets/fonts

writes `QuietSignal-Regular.ttf`, `QuietSignal-Bold.ttf` and a specimen PNG
(delete the PNG before committing — the fonts dir is embedded in the app).
Needs only fontTools; PIL for the specimen.

## What it is for

The app's voice where it NAMES things — header actions, a space's name, a
panel title — set through `--font-display` in styles.css. Never body text:
a display face at 14 px is a poster, not a paragraph.

## The rules the engine keeps

- Overlapping contours are deliberate: TrueType fills by non-zero winding.
  Every outer contour is clockwise, every hole counter-clockwise, decided by
  area — not by the direction a centreline happened to be drawn in.
- A centreline corner radius never drops below half the stroke, and a bowl
  whose inside is narrower than the stroke is emitted solid: an inner
  offset that folds over itself is a notch, and a notch is a bug.
- Terminals land on straight edges (the rounded-rect skeleton carries
  points along its edges, not only on its arcs), so a cut is square.

## Not yet

Kerning. Diacritics beyond Ё/Й. Italics. Hinting. A text weight for
paragraphs is a different typeface and a different year.

Licence: SIL Open Font License 1.1, copyright quite.space.
