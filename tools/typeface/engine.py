"""Quiet Signal — the skeleton engine.

A glyph is a list of STROKES on a 1000-unit grid, baseline at y=0, y up.
Each stroke is one of:

  ('line', (x0, y0), (x1, y1))            a straight bar, square ends
  ('diag', (x0, y0), (x1, y1), cut)       a diagonal cut flush 'h' or 'v'
  ('path', [points...], closed)           a smooth centreline, offset both ways
  ('poly', [points...])                   a ready polygon, filled as is

The weight is applied at build time: the same skeleton at stroke 92 is the
Regular, at 150 the Bold. Overlapping contours are deliberate — TrueType
fills by non-zero winding, so every contour is emitted clockwise and the
union is what you see. Inner contours (the hole of an O) are emitted
counter-clockwise by the path offset itself.
"""
import math

UPM = 1000
CAP = 700      # cap height
XH = 520       # x-height
ASC = 740      # ascender (b d h k l)
DESC = -190    # descender (g p q y)


# ---------------------------------------------------------------- geometry

def _norm(dx, dy):
    L = math.hypot(dx, dy) or 1.0
    return dx / L, dy / L


def rrect_points(x0, y0, x1, y1, r, n=10):
    """Closed rounded-rectangle centreline, clockwise from the top-left
    corner's end, densely sampled (n points per corner)."""
    r = max(0.0, min(r, (x1 - x0) / 2, (y1 - y0) / 2))
    pts = []

    def corner(cx, cy, a0, a1):
        for k in range(n + 1):
            a = math.radians(a0 + (a1 - a0) * k / n)
            pts.append((cx + r * math.cos(a), cy + r * math.sin(a)))

    def edge(p, q, k=8):
        # Points ALONG the straight edges too, so sub() can start and end a
        # stroke anywhere on them — a terminal that lands on a straight
        # edge is cut square, one that lands inside an arc is cut askew.
        for i in range(1, k):
            pts.append((p[0] + (q[0] - p[0]) * i / k, p[1] + (q[1] - p[1]) * i / k))

    # clockwise in y-up: top edge left→right, right edge top→bottom, ...
    corner(x0 + r, y1 - r, 180, 90)    # top-left arc (from left to top)
    edge((x0 + r, y1), (x1 - r, y1))
    corner(x1 - r, y1 - r, 90, 0)      # top-right
    edge((x1, y1 - r), (x1, y0 + r))
    corner(x1 - r, y0 + r, 0, -90)     # bottom-right
    edge((x1 - r, y0), (x0 + r, y0))
    corner(x0 + r, y0 + r, -90, -180)  # bottom-left
    edge((x0, y0 + r), (x0, y1 - r))
    return pts


def sub(points, p_from, p_to, forward=True):
    """The stretch of a closed polyline between the points nearest p_from
    and p_to, walking forward (clockwise for rrect_points) or backward."""
    def nearest(p):
        return min(range(len(points)), key=lambda i: (points[i][0] - p[0]) ** 2 + (points[i][1] - p[1]) ** 2)
    i, j = nearest(p_from), nearest(p_to)
    out = [points[i]]
    n = len(points)
    k = i
    while k != j:
        k = (k + 1) % n if forward else (k - 1) % n
        out.append(points[k])
    return out


def arc_points(cx, cy, r, a0, a1, n=24):
    return [(cx + r * math.cos(math.radians(a0 + (a1 - a0) * k / n)),
             cy + r * math.sin(math.radians(a0 + (a1 - a0) * k / n))) for k in range(n + 1)]


def _dedupe(points):
    out = []
    for p in points:
        if not out or abs(out[-1][0] - p[0]) > 0.01 or abs(out[-1][1] - p[1]) > 0.01:
            out.append(p)
    return out


def offset_path(points, w, closed):
    """Offset a centreline by ±w/2. Returns a list of contours."""
    pts = _dedupe(points)
    if closed and pts and (abs(pts[0][0] - pts[-1][0]) < 0.01 and abs(pts[0][1] - pts[-1][1]) < 0.01):
        pts = pts[:-1]
    n = len(pts)
    if n < 2:
        return []
    h = w / 2.0
    left, right = [], []
    for i, (x, y) in enumerate(pts):
        if closed:
            px, py = pts[i - 1]
            nx, ny = pts[(i + 1) % n]
        else:
            px, py = pts[i - 1] if i > 0 else (x, y)
            nx, ny = pts[i + 1] if i < n - 1 else (x, y)
        # tangent = average of incoming and outgoing directions
        t1 = _norm(x - px, y - py) if (px, py) != (x, y) else None
        t2 = _norm(nx - x, ny - y) if (nx, ny) != (x, y) else None
        if t1 and t2:
            tx, ty = t1[0] + t2[0], t1[1] + t2[1]
            if abs(tx) < 1e-9 and abs(ty) < 1e-9:
                tx, ty = t2
            tx, ty = _norm(tx, ty)
            # miter length so the offset stays w/2 from both segments
            cos_half = max(0.35, tx * t2[0] + ty * t2[1])
            m = 1.0 / cos_half
        else:
            tx, ty = t1 or t2
            m = 1.0
        nx_, ny_ = -ty, tx  # left normal
        left.append((x + nx_ * h * m, y + ny_ * h * m))
        right.append((x - nx_ * h * m, y - ny_ * h * m))
    if closed:
        # The OUTER contour clockwise, the INNER counter-clockwise — decided
        # by area, not by the centreline's direction. Getting this backwards
        # made every bowl an inverted ring: alone it still filled, but where
        # a stem or a crossbar crossed it the windings cancelled to a hole,
        # and the "notches" at the e's crossbar and the a's arch were holes.
        outer, inner = (left, right) if abs(_area(left)) > abs(_area(right)) else (right, left)
        # A bowl whose inside is narrower than the stroke has no inside: the
        # inner offset folds over itself and comes out wound the SAME way as
        # the outer. Emitting it would punch a sliver hole (the hairline seen
        # in a bold 'a'); a solid blob is the honest rendering of that shape.
        if _area(inner) * _area(outer) > 0 or abs(_area(inner)) < 400:
            return [_cw(outer)]
        return [_cw(outer), _ccw(inner)]
    return [_cw(left + right[::-1])]


def _area(poly):
    a = 0.0
    for i in range(len(poly)):
        x0, y0 = poly[i]
        x1, y1 = poly[(i + 1) % len(poly)]
        a += x0 * y1 - x1 * y0
    return a / 2.0


def _cw(poly):
    return poly[::-1] if _area(poly) > 0 else poly


def _ccw(poly):
    return poly[::-1] if _area(poly) < 0 else poly


def line_poly(p0, p1, w):
    (x0, y0), (x1, y1) = p0, p1
    tx, ty = _norm(x1 - x0, y1 - y0)
    nx, ny = -ty * w / 2, tx * w / 2
    return _cw([(x0 + nx, y0 + ny), (x1 + nx, y1 + ny), (x1 - nx, y1 - ny), (x0 - nx, y0 - ny)])


def diag_poly(p0, p1, w, cut):
    """A diagonal whose ends are cut flush: 'h' horizontal cuts (legs that
    meet a baseline or cap line), 'v' vertical cuts."""
    (x0, y0), (x1, y1) = p0, p1
    dx, dy = x1 - x0, y1 - y0
    if cut == 'h':
        # horizontal thickness so that perpendicular thickness is w
        t = w / max(0.2, abs(dy) / math.hypot(dx, dy))
        return _cw([(x0 - t / 2, y0), (x0 + t / 2, y0), (x1 + t / 2, y1), (x1 - t / 2, y1)])
    t = w / max(0.2, abs(dx) / math.hypot(dx, dy))
    return _cw([(x0, y0 - t / 2), (x0, y0 + t / 2), (x1, y1 + t / 2), (x1, y1 - t / 2)])


def expand(strokes, w):
    """Skeleton strokes → list of contours (each a list of points)."""
    out = []
    for s in strokes:
        kind = s[0]
        if kind == 'line':
            out.append(line_poly(s[1], s[2], w))
        elif kind == 'diag':
            out.append(diag_poly(s[1], s[2], w, s[3]))
        elif kind == 'path':
            # An optional fourth field scales the stroke: a breve or a dot
            # of the full weight would be a blob.
            out.extend(offset_path(s[1], w * (s[3] if len(s) > 3 else 1.0), s[2]))
        elif kind == 'poly':
            out.append(_cw(list(s[1])))
        else:
            raise ValueError(kind)
    return out
