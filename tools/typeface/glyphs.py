"""Quiet Signal — the letters.

Every glyph is a function of the stroke `s` returning (strokes, advance).
The grid: cap height 700, x-height 520, ascender 740, descender -190,
1000 units per em. A wide, square-shouldered grotesque: bowls are rounded
rectangles, diagonals are cut flush with the lines they meet, and there is
one stroke width in a glyph — the weight IS the family axis.
"""
from engine import CAP as H, XH as X, ASC, DESC, rrect_points, sub, arc_points

# Boxes (outer widths). Wide on purpose: the mockup is "small packets, wide
# horizons", and the letters are the horizons.
WC = 600    # a normal capital
WL = 520    # a normal lowercase
WD = 560    # a digit
SB = 56     # side bearing, both sides
R = 168     # outer corner radius of a bowl at cap height
RL = 150    # ... at x-height


def rr(x0, y0, x1, y1, s, r):
    """Centreline rounded rectangle for an OUTER box (x0,y0)-(x1,y1)."""
    # The centreline radius never drops below half a stroke: an inner
    # offset with a negative radius loops back on itself, and the loop
    # renders as a notch at every tight corner of a bold bowl.
    return rrect_points(x0 + s / 2, y0 + s / 2, x1 - s / 2, y1 - s / 2, max(s / 2 + 12, r - s / 2))


def L(p0, p1):
    return ('line', p0, p1)


def D(p0, p1, cut='h'):
    return ('diag', p0, p1, cut)


def P(points, closed=False):
    return ('path', points, closed)


def adv(w):
    return w + 2 * SB


# ------------------------------------------------------------ building blocks

def stem(x, y0, y1, s):
    return L((x, y0), (x, y1))


def arch_top(x0, y0, x1, y1, s, r, y_from=None):
    """n-shape: up the left side, over the top, down the right side to y0."""
    box = rr(x0, y0, x1, y1, s, r)
    yf = y_from if y_from is not None else y0
    pts = sub(box, (x0, yf), (x1, y1 - r), forward=True)
    return P(pts + [(x1 - s / 2, y0)])


def arch_bottom(x0, y0, x1, y1, s, r):
    """u-shape: down the left side, along the bottom, up the right to y1."""
    box = rr(x0, y0, x1, y1, s, r)
    pts = sub(box, (x0, y0 + r), (x1, y0 + r), forward=False)
    return P([(x0 + s / 2, y1)] + pts + [(x1 - s / 2, y1)])


def c_shape(x0, y0, x1, y1, s, r, top=0.76, bot=0.24, mirror=False):
    """C: the bowl minus a mouth on the right (or the left when mirrored)."""
    box = rr(x0, y0, x1, y1, s, r)
    h = y1 - y0
    if not mirror:
        return P(sub(box, (x1, y0 + h * top), (x1, y0 + h * bot), forward=False))
    return P(sub(box, (x0, y0 + h * top), (x0, y0 + h * bot), forward=True))


def s_shape(x0, y0, x1, y1, s, r):
    """S: a top bowl and a bottom bowl sharing the middle line."""
    m = (y0 + y1) / 2
    h = y1 - y0
    top = rr(x0, m - s / 2, x1, y1, s, r)
    bot = rr(x0, y0, x1, m + s / 2, s, r)
    a = sub(top, (x1, y0 + h * 0.80), (x0 + (x1 - x0) * 0.62, m), forward=False)
    b = sub(bot, (x0 + (x1 - x0) * 0.38, m), (x0, y0 + h * 0.20), forward=True)
    return [P(a), P(b)]


def bowl(x0, y0, x1, y1, s, r):
    return P(rr(x0, y0, x1, y1, s, r), closed=True)


def small_top(s):
    """The top of a lowercase half-bowl (a, ъ, ы, ь, я): a fraction of the
    x-height, but never so low that two strokes cannot fit inside it."""
    return max(0.56 * X, 2 * s + 44)


def dot(x, y, s):
    """A square dot whose side is the stroke, centred at (x, y)."""
    return L((x, y - s / 2), (x, y + s / 2))


# ----------------------------------------------------------------- capitals

def g_A(s):
    W = WC + 60
    return [D((0 + 18, 0), (W / 2, H)), D((W - 18, 0), (W / 2, H)),
            L((W * 0.2, 230), (W * 0.8, 230))], adv(W)


def g_B(s):
    W = WC - 20
    return [stem(s / 2, 0, H, s), bowl(0, H / 2 - s / 2, W * 0.92, H, s, R * 0.8),
            bowl(0, 0, W, H / 2 + s / 2, s, R * 0.85)], adv(W)


def g_C(s):
    return [c_shape(0, 0, WC, H, s, R)], adv(WC)


def g_D(s):
    W = WC
    box = rr(0, 0, W, H, s, R * 1.25)
    pts = sub(box, (0, H - R), (0, R), forward=True)
    return [stem(s / 2, 0, H, s), P([(s / 2, H - s / 2)] + pts + [(s / 2, s / 2)])], adv(W)


def g_E(s):
    W = WC - 40
    return [stem(s / 2, 0, H, s), L((0, H - s / 2), (W, H - s / 2)),
            L((0, H / 2), (W * 0.88, H / 2)), L((0, s / 2), (W, s / 2))], adv(W)


def g_F(s):
    W = WC - 50
    return [stem(s / 2, 0, H, s), L((0, H - s / 2), (W, H - s / 2)),
            L((0, H / 2), (W * 0.86, H / 2))], adv(W)


def g_G(s):
    W = WC + 10
    return [c_shape(0, 0, W, H, s, R, top=0.76, bot=0.22),
            stem(W - s / 2, 0.42 * H - s / 2, 0.42 * H, s) if False else L((W - s / 2, s / 2), (W - s / 2, 0.44 * H)),
            L((W * 0.52, 0.44 * H), (W, 0.44 * H))], adv(W)


def g_H(s):
    return [stem(s / 2, 0, H, s), stem(WC - s / 2, 0, H, s), L((0, H / 2), (WC, H / 2))], adv(WC)


def g_I(s):
    return [stem(s / 2, 0, H, s)], adv(s)


def g_J(s):
    W = WC - 80
    box = rr(0, 0, W, H, s, R)
    pts = sub(box, (0, 0.30 * H), (W, R), forward=False)
    return [P(pts + [(W - s / 2, H)])], adv(W)


def g_K(s):
    W = WC - 30
    return [stem(s / 2, 0, H, s), D((s * 0.55, 0.40 * H), (W, H)), D((s * 0.55 + 20, 0.52 * H), (W, 0))], adv(W)


def g_L(s):
    W = WC - 80
    return [stem(s / 2, 0, H, s), L((0, s / 2), (W, s / 2))], adv(W)


def g_M(s):
    W = WC + 180
    return [stem(s / 2, 0, H, s), stem(W - s / 2, 0, H, s),
            D((s * 0.9, H), (W / 2, 0.24 * H)), D((W - s * 0.9, H), (W / 2, 0.24 * H))], adv(W)


def g_N(s):
    W = WC
    return [stem(s / 2, 0, H, s), stem(W - s / 2, 0, H, s), D((s * 0.95, H), (W - s * 0.95, 0))], adv(W)


def g_O(s):
    return [bowl(0, 0, WC + 20, H, s, R)], adv(WC + 20)


def g_P(s):
    W = WC - 30
    return [stem(s / 2, 0, H, s), bowl(0, 0.44 * H, W, H, s, R * 0.85)], adv(W)


def g_Q(s):
    W = WC + 20
    return [bowl(0, 0, W, H, s, R), D((W * 0.58, 0.30 * H), (W + 10, -30))], adv(W)


def g_R(s):
    W = WC - 20
    return [stem(s / 2, 0, H, s), bowl(0, 0.44 * H, W * 0.96, H, s, R * 0.85),
            D((W * 0.46, 0.47 * H), (W, 0))], adv(W)


def g_S(s):
    return s_shape(0, 0, WC - 40, H, s, R), adv(WC - 40)


def g_T(s):
    W = WC - 20
    return [L((0, H - s / 2), (W, H - s / 2)), stem(W / 2, 0, H, s)], adv(W)


def g_U(s):
    return [arch_bottom(0, 0, WC, H, s, R)], adv(WC)


def g_V(s):
    W = WC + 40
    return [D((18, H), (W / 2, 0)), D((W - 18, H), (W / 2, 0))], adv(W)


def g_W(s):
    W = WC + 300
    return [D((18, H), (W * 0.27, 0)), D((W * 0.27, 0), (W / 2, 0.78 * H)),
            D((W / 2, 0.78 * H), (W * 0.73, 0)), D((W * 0.73, 0), (W - 18, H))], adv(W)


def g_X(s):
    W = WC + 20
    return [D((14, 0), (W - 14, H)), D((14, H), (W - 14, 0))], adv(W)


def g_Y(s):
    W = WC + 20
    return [D((14, H), (W / 2, 0.44 * H)), D((W - 14, H), (W / 2, 0.44 * H)), stem(W / 2, 0, 0.44 * H + s, s)], adv(W)


def g_Z(s):
    W = WC - 20
    return [L((0, H - s / 2), (W, H - s / 2)), D((W - 10, H), (10, 0)), L((0, s / 2), (W, s / 2))], adv(W)


# ---------------------------------------------------------------- lowercase

def g_a(s):
    W = WL
    box = rr(0, 0, W, X, s, RL)
    top = sub(box, (W, 0.62 * X), (0, 0.66 * X), forward=False)
    return [stem(W - s / 2, 0, X, s), P(top), bowl(0, 0, W, small_top(s), s, RL * 0.8)], adv(W)


def g_b(s):
    return [stem(s / 2, 0, ASC, s), bowl(0, 0, WL, X, s, RL)], adv(WL)


def g_c(s):
    return [c_shape(0, 0, WL, X, s, RL)], adv(WL)


def g_d(s):
    return [stem(WL - s / 2, 0, ASC, s), bowl(0, 0, WL, X, s, RL)], adv(WL)


def e_parts(W, s, mirror=False):
    # The mouth is measured in STROKES, not in fractions of the x-height: a
    # crossbar and a terminal a fixed distance apart close up at the bold.
    bar = 0.5 * X + s * 0.35
    end = bar - s - 0.10 * X
    box = rr(0, 0, W, X, s, RL * 0.92)
    if not mirror:
        pts = sub(box, (W, bar), (W, end), forward=False)
        return [P(pts), L((s / 2, bar), (W - s / 2, bar))]
    pts = sub(box, (0, bar), (0, end), forward=True)
    return [P(pts), L((s / 2, bar), (W - s / 2, bar))]


def g_e(s):
    return e_parts(WL, s), adv(WL)


def g_f(s):
    W = WL - 120
    xf = W * 0.42
    rc = max(120, s / 2 + 14)
    pts = [(xf, 0), (xf, ASC - rc)] + arc_points(xf + rc, ASC - rc, rc, 180, 90, 10) + [(W + 30, ASC - s / 2)]
    return [P(pts), L((0, X - s / 2), (W + 10, X - s / 2))], adv(W)


def g_g(s):
    W = WL
    rc = max(130, s / 2 + 14)
    tail = [(W - s / 2, X), (W - s / 2, DESC + rc)] + arc_points(W - s / 2 - rc, DESC + rc, rc, 0, -90, 10) + [(W * 0.12, DESC + s / 2)]
    return [bowl(0, 0, W, X, s, RL), P(tail)], adv(W)


def g_h(s):
    return [stem(s / 2, 0, ASC, s), arch_top(0, 0, WL, X, s, RL)], adv(WL)


def g_i(s):
    return [stem(s / 2, 0, X, s), dot(s / 2, X + 140, s)], adv(s)


def g_j(s):
    W = s + 150
    rc = max(120, s / 2 + 14)
    xj = W - s / 2
    pts = [(xj, X), (xj, DESC + rc)] + arc_points(xj - rc, DESC + rc, rc, 0, -90, 10) + [(0, DESC + s / 2)]
    return [P(pts), dot(xj, X + 140, s)], adv(W)


def g_k(s):
    W = WL - 40
    return [stem(s / 2, 0, ASC, s), D((s * 0.55, 0.36 * X), (W, X)), D((s * 0.55 + 16, 0.50 * X), (W, 0))], adv(W)


def g_l(s):
    return [stem(s / 2, 0, ASC, s)], adv(s)


def g_m(s):
    W = WL + 300
    w1 = (W + s) / 2
    return [stem(s / 2, 0, X, s), arch_top(0, 0, w1, X, s, RL), arch_top(w1 - s, 0, W, X, s, RL)], adv(W)


def g_n(s):
    return [stem(s / 2, 0, X, s), arch_top(0, 0, WL, X, s, RL)], adv(WL)


def g_o(s):
    return [bowl(0, 0, WL + 10, X, s, RL)], adv(WL + 10)


def g_p(s):
    return [stem(s / 2, DESC, X, s), bowl(0, 0, WL, X, s, RL)], adv(WL)


def g_q(s):
    return [stem(WL - s / 2, DESC, X, s), bowl(0, 0, WL, X, s, RL)], adv(WL)


def g_r(s):
    W = WL - 150
    box = rr(0, 0, W + 60, X, s, RL)
    pts = sub(box, (0, 0.5 * X), (W + 60, X - RL * 0.9), forward=True)
    return [stem(s / 2, 0, X, s), P(pts)], adv(W + 40)


def g_s(s):
    return s_shape(0, 0, WL - 30, X, s, RL), adv(WL - 30)


def g_t(s):
    W = WL - 140
    xt = W * 0.40
    rc = max(120, s / 2 + 14)
    pts = [(xt, X + 160), (xt, rc)] + arc_points(xt + rc, rc, rc, 180, 270, 10) + [(W + 20, s / 2)]
    return [P(pts), L((0, X - s / 2), (W + 10, X - s / 2))], adv(W)


def g_u(s):
    return [arch_bottom(0, 0, WL, X, s, RL)], adv(WL)


def g_v(s):
    W = WL
    return [D((14, X), (W / 2, 0)), D((W - 14, X), (W / 2, 0))], adv(W)


def g_w(s):
    W = WL + 260
    return [D((14, X), (W * 0.27, 0)), D((W * 0.27, 0), (W / 2, 0.76 * X)),
            D((W / 2, 0.76 * X), (W * 0.73, 0)), D((W * 0.73, 0), (W - 14, X))], adv(W)


def g_x(s):
    W = WL - 20
    return [D((12, 0), (W - 12, X)), D((12, X), (W - 12, 0))], adv(W)


def g_y(s):
    W = WL
    return [D((14, X), (W / 2 + 10, 0)), D((W - 14, X), (W * 0.18, DESC))], adv(W)


def g_z(s):
    W = WL - 40
    return [L((0, X - s / 2), (W, X - s / 2)), D((W - 8, X), (8, 0)), L((0, s / 2), (W, s / 2))], adv(W)


# ------------------------------------------------------------------- digits

def g_zero(s):
    return [bowl(0, 0, WD, H, s, R)], adv(WD)


def g_one(s):
    W = WD - 200
    x = W * 0.72
    return [stem(x, 0, H, s), D((x, H), (0, 0.70 * H))], adv(W)


def g_two(s):
    W = WD
    box = rr(0, 0.42 * H, W, H, s, R)
    top = sub(box, (0, 0.70 * H), (W, 0.56 * H), forward=True)
    return [P(top), D((W - s / 2, 0.56 * H), (s / 2, s / 2)), L((0, s / 2), (W, s / 2))], adv(W)


def g_three(s):
    W = WD
    m = H / 2
    top = rr(0, m - s / 2, W, H, s, R * 0.9)
    bot = rr(0, 0, W, m + s / 2, s, R * 0.9)
    a = sub(top, (0, 0.78 * H), (W * 0.36, m), forward=True)
    b = sub(bot, (W * 0.36, m), (0, 0.20 * H), forward=True)
    return [P(a), P(b)], adv(W)


def g_four(s):
    W = WD + 20
    x = W * 0.70
    return [stem(x, 0, H, s), D((x, H), (0, 0.28 * H)), L((0, 0.28 * H), (W, 0.28 * H))], adv(W)


def g_five(s):
    W = WD
    box = rr(0, 0, W, 0.62 * H, s, R)
    b = sub(box, (W * 0.12, 0.62 * H), (0, 0.20 * H), forward=True)
    return [L((W * 0.10, H - s / 2), (W, H - s / 2)), stem(W * 0.10 + s / 2, 0.58 * H, H, s), P(b)], adv(W)


def g_six(s):
    W = WD
    box = rr(0, 0, W, H, s, R)
    top = sub(box, (0, 0.30 * H), (W, 0.74 * H), forward=True)
    return [bowl(0, 0, W, 0.60 * H, s, R * 0.9), P(top)], adv(W)


def g_seven(s):
    W = WD
    return [L((0, H - s / 2), (W, H - s / 2)), D((W - 10, H), (W * 0.30, 0))], adv(W)


def g_eight(s):
    W = WD
    m = H / 2
    return [bowl(0, m - s / 2, W, H, s, R * 0.9), bowl(0, 0, W, m + s / 2, s, R * 0.9)], adv(W)


def g_nine(s):
    W = WD
    box = rr(0, 0, W, H, s, R)
    bot = sub(box, (W, 0.70 * H), (0, 0.26 * H), forward=True)
    return [bowl(0, 0.40 * H, W, H, s, R * 0.9), P(bot)], adv(W)


# -------------------------------------------------------------- punctuation

def g_space(s):
    return [], 260


def g_period(s):
    return [dot(s / 2, s / 2, s)], adv(s)


def g_comma(s):
    return [dot(s / 2, s / 2, s), D((s / 2 + 10, 0), (s * 0.15, -120), 'h')], adv(s)


def g_colon(s):
    return [dot(s / 2, s / 2, s), dot(s / 2, X - s / 2, s)], adv(s)


def g_semicolon(s):
    return [dot(s / 2, X - s / 2, s), dot(s / 2, s / 2, s), D((s / 2 + 10, 0), (s * 0.15, -120), 'h')], adv(s)


def g_exclam(s):
    return [stem(s / 2, 220, H, s), dot(s / 2, s / 2, s)], adv(s)


def g_question(s):
    W = WD - 80
    box = rr(0, 0.34 * H, W, H, s, R * 0.9)
    top = sub(box, (0, 0.70 * H), (W / 2, 0.34 * H), forward=True)
    return [P(top), stem(W / 2, 220, 0.34 * H + s / 2, s), dot(W / 2, s / 2, s)], adv(W)


def g_hyphen(s):
    return [L((0, 0.36 * H), (260, 0.36 * H))], adv(260)


def g_endash(s):
    return [L((0, 0.36 * H), (500, 0.36 * H))], adv(500)


def g_emdash(s):
    return [L((0, 0.36 * H), (900, 0.36 * H))], adv(900)


def g_parenleft(s):
    W = 250
    box = rr(0, DESC, W + 200, H + 40, s, 220)
    return [P(sub(box, (W + 200, H + 40 - 60), (W + 200, DESC + 60), forward=False))], adv(W)


def g_parenright(s):
    W = 250
    box = rr(-200, DESC, W, H + 40, s, 220)
    return [P(sub(box, (-200, H + 40 - 60), (-200, DESC + 60), forward=True))], adv(W)


def g_slash(s):
    return [D((0, DESC / 2), (380, H), 'h')], adv(380)


def g_quotesingle(s):
    return [stem(s / 2, H - 200, H, s)], adv(s)


def g_quotedbl(s):
    return [stem(s / 2, H - 200, H, s), stem(s * 1.5 + 60, H - 200, H, s)], adv(s * 2 + 60)


def g_guillemotleft(s):
    W = 420
    return [D((W * 0.45, 0.62 * H - 30), (0 + 20, 0.36 * H), 'h'), D((W * 0.45, 0.10 * H + 30), (0 + 20, 0.36 * H), 'h'),
            D((W, 0.62 * H - 30), (W * 0.55 + 20, 0.36 * H), 'h'), D((W, 0.10 * H + 30), (W * 0.55 + 20, 0.36 * H), 'h')], adv(W)


def g_guillemotright(s):
    W = 420
    return [D((0, 0.62 * H - 30), (W * 0.45 - 20, 0.36 * H), 'h'), D((0, 0.10 * H + 30), (W * 0.45 - 20, 0.36 * H), 'h'),
            D((W * 0.55, 0.62 * H - 30), (W - 20, 0.36 * H), 'h'), D((W * 0.55, 0.10 * H + 30), (W - 20, 0.36 * H), 'h')], adv(W)


def g_plus(s):
    W = 460
    return [L((0, 0.36 * H), (W, 0.36 * H)), stem(W / 2, 0.36 * H - W / 2, 0.36 * H + W / 2, s)], adv(W)


def g_equal(s):
    W = 460
    return [L((0, 0.26 * H), (W, 0.26 * H)), L((0, 0.48 * H), (W, 0.48 * H))], adv(W)


def g_underscore(s):
    return [L((0, -110), (560, -110))], adv(560)


# ---------------------------------------------------------- cyrillic caps

def g_Be(s):   # Б
    W = WC - 20
    return [stem(s / 2, 0, H, s), L((0, H - s / 2), (W, H - s / 2)),
            bowl(0, 0, W, 0.56 * H, s, R * 0.85)], adv(W)


def g_Ge(s):   # Г
    W = WC - 80
    return [stem(s / 2, 0, H, s), L((0, H - s / 2), (W, H - s / 2))], adv(W)


def g_De(s):   # Д
    W = WC + 40
    foot = -140
    return [L((W * 0.30, H - s / 2), (W * 0.80, H - s / 2)), D((W * 0.30 + s * 0.3, H), (W * 0.10, s * 0.8)),
            stem(W * 0.80 - s / 2, 0, H, s), L((0, s / 2), (W, s / 2)),
            stem(s / 2, foot, s, s), stem(W - s / 2, foot, s, s)], adv(W)


def g_Zhe(s):  # Ж
    W = WC + 260
    c = W / 2
    return [stem(c, 0, H, s),
            D((c - s * 0.5, 0.42 * H), (14, H)), D((c - s * 0.5, 0.52 * H), (14, 0)),
            D((c + s * 0.5, 0.42 * H), (W - 14, H)), D((c + s * 0.5, 0.52 * H), (W - 14, 0))], adv(W)


def g_Ze(s):   # З
    strokes, a = g_three(s)
    return strokes, a


def g_I_cyr(s):  # И
    W = WC
    return [stem(s / 2, 0, H, s), stem(W - s / 2, 0, H, s), D((s * 0.95, 0), (W - s * 0.95, H))], adv(W)


def g_Ishort(s):  # Й
    strokes, a = g_I_cyr(s)
    W = WC
    return strokes + [('path', arc_points(W / 2, H + 235, 150, 225, 315, 12), False, 0.8)], a


def g_El(s):   # Л
    W = WC + 20
    return [L((W * 0.28, H - s / 2), (W, H - s / 2)), stem(W - s / 2, 0, H, s),
            D((W * 0.28 + s * 0.3, H), (0 + 14, 0))], adv(W)


def g_Pe(s):   # П
    return [stem(s / 2, 0, H, s), stem(WC - s / 2, 0, H, s), L((0, H - s / 2), (WC, H - s / 2))], adv(WC)


def g_U_cyr(s):  # У
    W = WC + 20
    return [D((W - 14, H), (W * 0.16, 0)), D((14, H), (W * 0.52, 0.42 * H))], adv(W)


def g_Ef(s):   # Ф
    W = WC + 260
    return [stem(W / 2, 0, H, s), bowl(0, 0.16 * H, W, 0.84 * H, s, R)], adv(W)


def g_Tse(s):  # Ц
    W = WC
    return [stem(s / 2, 0, H, s), stem(W - s / 2, 0, H, s), L((0, s / 2), (W + 40, s / 2)),
            stem(W + 40 - s / 2, -140, s, s)], adv(W + 40)


def g_Che(s):  # Ч
    W = WC - 20
    box = rr(0, 0.36 * H, W, H, s, R * 0.9)
    pts = sub(box, (0, H - R), (W, 0.36 * H + R), forward=False)
    return [P([(s / 2, H)] + pts), stem(W - s / 2, 0, H, s)], adv(W)


def g_Sha(s):  # Ш
    W = WC + 300
    return [stem(s / 2, 0, H, s), stem(W / 2, 0, H, s), stem(W - s / 2, 0, H, s), L((0, s / 2), (W, s / 2))], adv(W)


def g_Shcha(s):  # Щ
    W = WC + 300
    return [stem(s / 2, 0, H, s), stem(W / 2, 0, H, s), stem(W - s / 2, 0, H, s), L((0, s / 2), (W + 40, s / 2)),
            stem(W + 40 - s / 2, -140, s, s)], adv(W + 40)


def g_Hard(s):  # Ъ
    W = WC + 120
    x = 170
    return [L((0, H - s / 2), (x + s, H - s / 2)), stem(x + s / 2, 0, H, s),
            bowl(x, 0, W, 0.56 * H, s, R * 0.85)], adv(W)


def g_Yeru(s):  # Ы
    W = WC + 260
    return [stem(s / 2, 0, H, s), bowl(0, 0, W * 0.62, 0.56 * H, s, R * 0.85), stem(W - s / 2, 0, H, s)], adv(W)


def g_Soft(s):  # Ь
    W = WC - 20
    return [stem(s / 2, 0, H, s), bowl(0, 0, W, 0.56 * H, s, R * 0.85)], adv(W)


def g_E_cyr(s):  # Э
    W = WC
    return [c_shape(0, 0, W, H, s, R, top=0.76, bot=0.24, mirror=True), L((W * 0.38, H / 2), (W - s / 2, H / 2))], adv(W)


def g_Yu(s):   # Ю
    W = WC + 320
    x = 200
    return [stem(s / 2, 0, H, s), L((s / 2, H / 2), (x + s, H / 2)), bowl(x, 0, W, H, s, R)], adv(W)


def g_Ya(s):   # Я
    W = WC - 20
    return [stem(W - s / 2, 0, H, s), bowl(W * 0.04, 0.44 * H, W, H, s, R * 0.85),
            D((W * 0.52, 0.47 * H), (0, 0))], adv(W)


# ----------------------------------------------------- cyrillic lowercase

def g_be(s):   # б
    W = WL
    rc = max(120, s / 2 + 14)
    top = [(W - s / 2, 0.45 * X), (W - s / 2, ASC - rc)] + arc_points(W - s / 2 - rc, ASC - rc, rc, 0, 90, 10) + [(W * 0.05, ASC - s / 2)]
    return [bowl(0, 0, W, X, s, RL), P(top)], adv(W)


def g_ve(s):   # в
    W = WL - 20
    return [stem(s / 2, 0, X, s), bowl(0, X / 2 - s / 2, W * 0.92, X, s, RL * 0.75),
            bowl(0, 0, W, X / 2 + s / 2, s, RL * 0.8)], adv(W)


def g_ge(s):   # г
    W = WL - 100
    return [stem(s / 2, 0, X, s), L((0, X - s / 2), (W, X - s / 2))], adv(W)


def g_de(s):   # д
    W = WL + 20
    foot = -130
    return [L((W * 0.30, X - s / 2), (W * 0.80, X - s / 2)), D((W * 0.30 + s * 0.3, X), (W * 0.10, s * 0.8)),
            stem(W * 0.80 - s / 2, 0, X, s), L((0, s / 2), (W, s / 2)),
            stem(s / 2, foot, s, s), stem(W - s / 2, foot, s, s)], adv(W)


def g_zhe(s):  # ж
    W = WL + 240
    c = W / 2
    return [stem(c, 0, X, s),
            D((c - s * 0.5, 0.40 * X), (12, X)), D((c - s * 0.5, 0.52 * X), (12, 0)),
            D((c + s * 0.5, 0.40 * X), (W - 12, X)), D((c + s * 0.5, 0.52 * X), (W - 12, 0))], adv(W)


def g_ze(s):   # з
    W = WL - 40
    m = X / 2
    top = rr(0, m - s / 2, W, X, s, RL * 0.8)
    bot = rr(0, 0, W, m + s / 2, s, RL * 0.8)
    a = sub(top, (0, 0.78 * X), (W * 0.36, m), forward=True)
    b = sub(bot, (W * 0.36, m), (0, 0.20 * X), forward=True)
    return [P(a), P(b)], adv(W)


def g_i_cyr(s):  # и
    W = WL
    return [stem(s / 2, 0, X, s), stem(W - s / 2, 0, X, s), D((s * 0.95, 0), (W - s * 0.95, X))], adv(W)


def g_ishort(s):  # й
    strokes, a = g_i_cyr(s)
    return strokes + [('path', arc_points(WL / 2, X + 225, 140, 225, 315, 12), False, 0.8)], a


def g_ka(s):   # к
    W = WL - 60
    return [stem(s / 2, 0, X, s), D((s * 0.55, 0.38 * X), (W, X)), D((s * 0.55 + 14, 0.50 * X), (W, 0))], adv(W)


def g_el(s):   # л
    W = WL
    return [L((W * 0.28, X - s / 2), (W, X - s / 2)), stem(W - s / 2, 0, X, s),
            D((W * 0.28 + s * 0.3, X), (12, 0))], adv(W)


def g_em(s):   # м
    W = WL + 160
    return [stem(s / 2, 0, X, s), stem(W - s / 2, 0, X, s),
            D((s * 0.9, X), (W / 2, 0.22 * X)), D((W - s * 0.9, X), (W / 2, 0.22 * X))], adv(W)


def g_en(s):   # н
    return [stem(s / 2, 0, X, s), stem(WL - s / 2, 0, X, s), L((0, X / 2), (WL, X / 2))], adv(WL)


def g_pe(s):   # п
    return [stem(s / 2, 0, X, s), stem(WL - s / 2, 0, X, s), L((0, X - s / 2), (WL, X - s / 2))], adv(WL)


def g_te(s):   # т
    W = WL - 40
    return [L((0, X - s / 2), (W, X - s / 2)), stem(W / 2, 0, X, s)], adv(W)


def g_ef(s):   # ф
    W = WL + 260
    return [stem(W / 2, DESC, ASC, s), bowl(0, 0, W, X, s, RL)], adv(W)


def g_tse(s):  # ц
    W = WL
    return [stem(s / 2, 0, X, s), stem(W - s / 2, 0, X, s), L((0, s / 2), (W + 40, s / 2)),
            stem(W + 40 - s / 2, -130, s, s)], adv(W + 40)


def g_che(s):  # ч
    W = WL - 20
    box = rr(0, 0.34 * X, W, X, s, RL * 0.8)
    pts = sub(box, (0, X - RL), (W, 0.34 * X + RL * 0.8), forward=False)
    return [P([(s / 2, X)] + pts), stem(W - s / 2, 0, X, s)], adv(W)


def g_sha(s):  # ш
    W = WL + 280
    return [stem(s / 2, 0, X, s), stem(W / 2, 0, X, s), stem(W - s / 2, 0, X, s), L((0, s / 2), (W, s / 2))], adv(W)


def g_shcha(s):  # щ
    W = WL + 280
    return [stem(s / 2, 0, X, s), stem(W / 2, 0, X, s), stem(W - s / 2, 0, X, s), L((0, s / 2), (W + 40, s / 2)),
            stem(W + 40 - s / 2, -130, s, s)], adv(W + 40)


def g_hard(s):  # ъ
    W = WL + 100
    x = 150
    return [L((0, X - s / 2), (x + s, X - s / 2)), stem(x + s / 2, 0, X, s), bowl(x, 0, W, small_top(s), s, RL * 0.8)], adv(W)


def g_yeru(s):  # ы
    W = WL + 240
    return [stem(s / 2, 0, X, s), bowl(0, 0, W * 0.62, small_top(s), s, RL * 0.8), stem(W - s / 2, 0, X, s)], adv(W)


def g_soft(s):  # ь
    W = WL - 20
    return [stem(s / 2, 0, X, s), bowl(0, 0, W, small_top(s), s, RL * 0.8)], adv(W)


def g_e_cyr(s):  # э
    W = WL
    return [c_shape(0, 0, W, X, s, RL, top=0.76, bot=0.24, mirror=True), L((W * 0.36, X / 2), (W - s / 2, X / 2))], adv(W)


def g_yu(s):   # ю
    W = WL + 300
    x = 180
    return [stem(s / 2, 0, X, s), L((s / 2, X / 2), (x + s, X / 2)), bowl(x, 0, W, X, s, RL)], adv(W)


def g_ya(s):   # я
    W = WL - 20
    return [stem(W - s / 2, 0, X, s), bowl(W * 0.04, X - small_top(s), W, X, s, RL * 0.8),
            D((W * 0.52, X - small_top(s) + s * 0.2, ), (0, 0))], adv(W)


def g_yo_cap(s):  # Ё
    strokes, a = g_E(s)
    W = WC - 40
    return strokes + [dot(W * 0.32, H + 150, s), dot(W * 0.68, H + 150, s)], a


def g_yo(s):   # ё
    strokes, a = g_e(s)
    return strokes + [dot(WL * 0.30, X + 150, s), dot(WL * 0.70, X + 150, s)], a


# ---------------------------------------------------------------- the map

GLYPHS = {
    'space': (0x20, g_space), 'period': (0x2E, g_period), 'comma': (0x2C, g_comma),
    'colon': (0x3A, g_colon), 'semicolon': (0x3B, g_semicolon), 'exclam': (0x21, g_exclam),
    'question': (0x3F, g_question), 'hyphen': (0x2D, g_hyphen), 'endash': (0x2013, g_endash),
    'emdash': (0x2014, g_emdash), 'parenleft': (0x28, g_parenleft), 'parenright': (0x29, g_parenright),
    'slash': (0x2F, g_slash), 'quotesingle': (0x27, g_quotesingle), 'quotedbl': (0x22, g_quotedbl),
    'guillemotleft': (0xAB, g_guillemotleft), 'guillemotright': (0xBB, g_guillemotright),
    'plus': (0x2B, g_plus), 'equal': (0x3D, g_equal), 'underscore': (0x5F, g_underscore),
}
for i, ch in enumerate('ABCDEFGHIJKLMNOPQRSTUVWXYZ'):
    GLYPHS[ch] = (ord(ch), globals()['g_' + ch])
for i, ch in enumerate('abcdefghijklmnopqrstuvwxyz'):
    GLYPHS[ch] = (ord(ch), globals()['g_' + ch])
for name, ch in zip(['zero', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine'], '0123456789'):
    GLYPHS[name] = (ord(ch), globals()['g_' + name])

CYR_CAPS = {
    'А': g_A, 'Б': g_Be, 'В': g_B, 'Г': g_Ge, 'Д': g_De, 'Е': g_E, 'Ё': g_yo_cap, 'Ж': g_Zhe, 'З': g_Ze,
    'И': g_I_cyr, 'Й': g_Ishort, 'К': g_K, 'Л': g_El, 'М': g_M, 'Н': g_H, 'О': g_O, 'П': g_Pe, 'Р': g_P,
    'С': g_C, 'Т': g_T, 'У': g_U_cyr, 'Ф': g_Ef, 'Х': g_X, 'Ц': g_Tse, 'Ч': g_Che, 'Ш': g_Sha, 'Щ': g_Shcha,
    'Ъ': g_Hard, 'Ы': g_Yeru, 'Ь': g_Soft, 'Э': g_E_cyr, 'Ю': g_Yu, 'Я': g_Ya,
}
CYR_LOWER = {
    'а': g_a, 'б': g_be, 'в': g_ve, 'г': g_ge, 'д': g_de, 'е': g_e, 'ё': g_yo, 'ж': g_zhe, 'з': g_ze,
    'и': g_i_cyr, 'й': g_ishort, 'к': g_ka, 'л': g_el, 'м': g_em, 'н': g_en, 'о': g_o, 'п': g_pe, 'р': g_p,
    'с': g_c, 'т': g_te, 'у': g_y, 'ф': g_ef, 'х': g_x, 'ц': g_tse, 'ч': g_che, 'ш': g_sha, 'щ': g_shcha,
    'ъ': g_hard, 'ы': g_yeru, 'ь': g_soft, 'э': g_e_cyr, 'ю': g_yu, 'я': g_ya,
}
for ch, fn in CYR_CAPS.items():
    GLYPHS['uni%04X' % ord(ch)] = (ord(ch), fn)
for ch, fn in CYR_LOWER.items():
    GLYPHS['uni%04X' % ord(ch)] = (ord(ch), fn)
