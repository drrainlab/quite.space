"""Build Quiet Signal .ttf files from the skeletons, and render a specimen.

  python3 build.py [outdir]      → QuietSignal-Regular.ttf, -Bold.ttf, specimen PNGs
"""
import os
import sys

from fontTools.fontBuilder import FontBuilder
from fontTools.pens.ttGlyphPen import TTGlyphPen

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from engine import UPM, CAP, XH, ASC, DESC, expand  # noqa: E402
from glyphs import GLYPHS, SB  # noqa: E402

FAMILY = 'Quiet Signal'
# Stroke per weight, in units of the 1000 em. The first cut was 92/150 and
# the owner's eye at 13–16 px called the Cyrillic heavy: wide letters with
# many verticals turn to a blocky texture when a stroke is a tenth of the
# em. 80 keeps the monoline at UI sizes; 140 keeps the Bold a display face.
WEIGHTS = {'Regular': (80, 400), 'Bold': (140, 700)}


def build(style, stroke, weight_class, outdir):
    names = ['.notdef'] + list(GLYPHS.keys())
    fb = FontBuilder(UPM, isTTF=True)
    fb.setupGlyphOrder(names)
    cmap = {cp: name for name, (cp, _) in GLYPHS.items()}
    fb.setupCharacterMap(cmap)
    glyf, metrics = {}, {}

    pen = TTGlyphPen(None)
    pen.moveTo((60, 0)); pen.lineTo((60, CAP)); pen.lineTo((400, CAP)); pen.lineTo((400, 0)); pen.closePath()
    pen.moveTo((120, 60)); pen.lineTo((340, 60)); pen.lineTo((340, CAP - 60)); pen.lineTo((120, CAP - 60)); pen.closePath()
    glyf['.notdef'] = pen.glyph()
    metrics['.notdef'] = (460, 60)

    for name, (cp, fn) in GLYPHS.items():
        strokes, advance = fn(stroke)
        contours = expand(strokes, stroke)
        pen = TTGlyphPen(None)
        for c in contours:
            pts = [(round(x), round(y)) for x, y in c]
            pen.moveTo(pts[0])
            for p in pts[1:]:
                pen.lineTo(p)
            pen.closePath()
        g = pen.glyph()
        # Shift every glyph right by the side bearing: skeletons start at x=0.
        if contours:
            pen2 = TTGlyphPen(None)
            for c in contours:
                pts = [(round(x + SB), round(y)) for x, y in c]
                pen2.moveTo(pts[0])
                for p in pts[1:]:
                    pen2.lineTo(p)
                pen2.closePath()
            g = pen2.glyph()
        glyf[name] = g
        lsb = SB if contours else 0
        metrics[name] = (round(advance), lsb)

    fb.setupGlyf(glyf)
    fb.setupHorizontalMetrics(metrics)
    fb.setupHorizontalHeader(ascent=900, descent=-240, lineGap=0)
    fb.setupNameTable({
        'familyName': FAMILY, 'styleName': style,
        'uniqueFontIdentifier': f'{FAMILY} {style} 0.1',
        'fullName': f'{FAMILY} {style}', 'psName': f'QuietSignal-{style}',
        'version': 'Version 0.1',
        'copyright': 'Copyright 2026 quite.space. Licensed under the SIL Open Font License 1.1.',
        'licenseDescription': 'SIL Open Font License, Version 1.1',
        'licenseInfoURL': 'https://openfontlicense.org',
        'description': 'A wide, square-shouldered monoline grotesque built from skeletons. Latin and Cyrillic.',
    })
    fb.setupOS2(sTypoAscender=900, sTypoDescender=-240, sTypoLineGap=0,
                usWinAscent=940, usWinDescent=260, sxHeight=XH, sCapHeight=CAP,
                usWeightClass=weight_class, usWidthClass=7, achVendID='QTSP',
                fsType=0, ulUnicodeRange1=(1 << 0) | (1 << 1) | (1 << 9))
    fb.setupPost()
    fb.setupHead(unitsPerEm=UPM)
    fb.font['head'].flags = 0x0B
    path = os.path.join(outdir, f'QuietSignal-{style}.ttf')
    fb.save(path)
    return path


def specimen(paths, outdir):
    from PIL import Image, ImageDraw, ImageFont
    Wpx, pad = 1800, 60
    lines = [
        (paths['Bold'], 150, 'Quiet Signal'),
        (paths['Regular'], 44, 'Small packets. Wide horizons.'),
        (paths['Bold'], 118, 'Aa Rr Qq 01'),
        (paths['Regular'], 60, 'ABCDEFGHIJKLM NOPQRSTUVWXYZ'),
        (paths['Regular'], 60, 'abcdefghijklm nopqrstuvwxyz'),
        (paths['Regular'], 60, '0123456789 .,:;!?-–— () / «» + ='),
        (paths['Bold'], 150, 'Тихий сигнал'),
        (paths['Regular'], 44, 'Маленькие пакеты. Большие расстояния.'),
        (paths['Bold'], 118, 'Дд Жж Яя'),
        (paths['Regular'], 60, 'АБВГДЕЁЖЗИЙКЛМ НОПРСТУФХЦЧШЩ ЪЫЬЭЮЯ'),
        (paths['Regular'], 60, 'абвгдеёжзийклм нопрстуфхцчшщ ъыьэюя'),
        (paths['Regular'], 36, 'We reached the ridge before sunset. The air is clear. Meet us by the northern trail.'),
        (paths['Regular'], 36, 'Мы поднялись на хребет до заката. Воздух чистый. Встретимся у северной тропы.'),
    ]
    y = pad
    heights = []
    for p, size, text in lines:
        heights.append(int(size * 1.35))
    img = Image.new('RGB', (Wpx, pad * 2 + sum(heights)), (14, 13, 18))
    d = ImageDraw.Draw(img)
    for (p, size, text), h in zip(lines, heights):
        f = ImageFont.truetype(p, size)
        d.text((pad, y), text, font=f, fill=(238, 233, 222))
        y += h
    out = os.path.join(outdir, 'specimen.png')
    img.save(out)
    return out


if __name__ == '__main__':
    outdir = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), 'out')
    os.makedirs(outdir, exist_ok=True)
    paths = {}
    for style, (stroke, wc) in WEIGHTS.items():
        paths[style] = build(style, stroke, wc, outdir)
        print('built', paths[style])
    print('specimen', specimen(paths, outdir))
