#include "qi/cbor.h"
#include "qi/config.h"
#include <string.h>

/* ---- writer ---- */

void qi_w_init(qi_cbor_w *w, uint8_t *buf, size_t cap) {
  w->buf = buf; w->cap = cap; w->len = 0; w->overflow = false;
}

static void put(qi_cbor_w *w, const uint8_t *b, size_t n) {
  if (w->overflow || n > w->cap - w->len) { w->overflow = true; return; }
  memcpy(w->buf + w->len, b, n);
  w->len += n;
}

static void head(qi_cbor_w *w, uint8_t major, uint64_t v) {
  uint8_t h[9];
  size_t n;
  uint8_t mj = (uint8_t)(major << 5);
  if (v < 24) { h[0] = (uint8_t)(mj | (uint8_t)v); n = 1; }
  else if (v <= 0xff) { h[0] = (uint8_t)(mj | 24); h[1] = (uint8_t)v; n = 2; }
  else if (v <= 0xffff) { h[0] = (uint8_t)(mj | 25); h[1] = (uint8_t)(v >> 8); h[2] = (uint8_t)v; n = 3; }
  else if (v <= 0xffffffffULL) {
    h[0] = (uint8_t)(mj | 26);
    for (int i = 0; i < 4; i++) h[1 + i] = (uint8_t)(v >> (24 - 8 * i));
    n = 5;
  } else {
    h[0] = (uint8_t)(mj | 27);
    for (int i = 0; i < 8; i++) h[1 + i] = (uint8_t)(v >> (56 - 8 * i));
    n = 9;
  }
  put(w, h, n);
}

void qi_w_uint(qi_cbor_w *w, uint64_t v) { head(w, QI_MT_UINT, v); }
void qi_w_bytes(qi_cbor_w *w, const uint8_t *b, size_t n) { head(w, QI_MT_BYTES, n); put(w, b, n); }
void qi_w_text(qi_cbor_w *w, const char *s, size_t n) { head(w, QI_MT_TEXT, n); put(w, (const uint8_t *)s, n); }
void qi_w_array(qi_cbor_w *w, size_t n) { head(w, QI_MT_ARRAY, n); }
void qi_w_map(qi_cbor_w *w, size_t n) { head(w, QI_MT_MAP, n); }
void qi_w_bool(qi_cbor_w *w, bool v) { uint8_t b = v ? 0xf5 : 0xf4; put(w, &b, 1); }

qi_status qi_w_done(const qi_cbor_w *w, size_t *len) {
  if (w->overflow) return QI_ERR_SPACE;
  if (len) *len = w->len;
  return QI_OK;
}

/* ---- reader ---- */

void qi_r_init(qi_cbor_r *r, const uint8_t *p, size_t len) { r->p = p; r->len = len; r->pos = 0; }

/* read_head consumes a head, enforcing shortest form and refusing
 * indefinite/reserved additional info. */
static qi_status read_head(qi_cbor_r *r, uint8_t *major, uint64_t *arg) {
  if (r->pos >= r->len) return QI_ERR_CBOR;
  uint8_t b = r->p[r->pos];
  uint8_t mt = b >> 5, ai = b & 0x1f;
  uint64_t v;
  size_t need;
  if (ai < 24) { v = ai; need = 0; }
  else if (ai == 24) need = 1;
  else if (ai == 25) need = 2;
  else if (ai == 26) need = 4;
  else if (ai == 27) need = 8;
  else return QI_ERR_CBOR; /* 28-30 reserved, 31 indefinite */
  if (need > r->len - r->pos - 1) return QI_ERR_CBOR;
  if (need) {
    v = 0;
    for (size_t i = 0; i < need; i++) v = v << 8 | r->p[r->pos + 1 + i];
    /* shortest form */
    if (mt != QI_MT_SIMPLE) {
      if ((need == 1 && v < 24) || (need == 2 && v <= 0xff) ||
          (need == 4 && v <= 0xffff) || (need == 8 && v <= 0xffffffffULL))
        return QI_ERR_CBOR;
    }
  }
  r->pos += 1 + need;
  *major = mt; *arg = v;
  return QI_OK;
}

qi_status qi_r_peek(const qi_cbor_r *r, uint8_t *major, uint64_t *arg) {
  qi_cbor_r c = *r;
  return read_head(&c, major, arg);
}

qi_status qi_r_uint(qi_cbor_r *r, uint64_t *v) {
  uint8_t mt; uint64_t a;
  qi_status s = read_head(r, &mt, &a);
  if (s) return s;
  if (mt != QI_MT_UINT) return QI_ERR_CBOR;
  *v = a;
  return QI_OK;
}

static qi_status read_str(qi_cbor_r *r, uint8_t want, const uint8_t **b, size_t *n) {
  uint8_t mt; uint64_t a;
  qi_status s = read_head(r, &mt, &a);
  if (s) return s;
  if (mt != want) return QI_ERR_CBOR;
  if (a > r->len - r->pos) return QI_ERR_CBOR; /* truncated */
  *b = r->p + r->pos; *n = (size_t)a;
  r->pos += (size_t)a;
  return QI_OK;
}

qi_status qi_r_bytes(qi_cbor_r *r, const uint8_t **b, size_t *n) { return read_str(r, QI_MT_BYTES, b, n); }

static bool utf8_valid(const uint8_t *s, size_t n) {
  size_t i = 0;
  while (i < n) {
    uint8_t c = s[i];
    size_t len; uint32_t cp;
    if (c < 0x80) { i++; continue; }
    else if ((c & 0xe0) == 0xc0) { len = 2; cp = c & 0x1f; if (cp < 2) return false; }
    else if ((c & 0xf0) == 0xe0) { len = 3; cp = c & 0x0f; }
    else if ((c & 0xf8) == 0xf0) { len = 4; cp = c & 0x07; }
    else return false;
    if (i + len > n) return false;
    for (size_t k = 1; k < len; k++) {
      if ((s[i + k] & 0xc0) != 0x80) return false;
      cp = cp << 6 | (s[i + k] & 0x3f);
    }
    if ((len == 3 && cp < 0x800) || (len == 4 && (cp < 0x10000 || cp > 0x10ffff)) ||
        (cp >= 0xd800 && cp <= 0xdfff)) return false;
    i += len;
  }
  return true;
}

qi_status qi_r_text(qi_cbor_r *r, const char **s, size_t *n) {
  const uint8_t *b;
  qi_status st = read_str(r, QI_MT_TEXT, &b, n);
  if (st) return st;
  if (!utf8_valid(b, *n)) return QI_ERR_CBOR;
  *s = (const char *)b;
  return QI_OK;
}

static qi_status read_count(qi_cbor_r *r, uint8_t want, size_t *n) {
  uint8_t mt; uint64_t a;
  qi_status s = read_head(r, &mt, &a);
  if (s) return s;
  if (mt != want) return QI_ERR_CBOR;
  if (a > r->len - r->pos) return QI_ERR_CBOR; /* cannot hold that many items */
  *n = (size_t)a;
  return QI_OK;
}

qi_status qi_r_array(qi_cbor_r *r, size_t *n) { return read_count(r, QI_MT_ARRAY, n); }
qi_status qi_r_map(qi_cbor_r *r, size_t *n) { return read_count(r, QI_MT_MAP, n); }

qi_status qi_r_bool(qi_cbor_r *r, bool *v) {
  if (r->pos >= r->len) return QI_ERR_CBOR;
  uint8_t b = r->p[r->pos];
  if (b == 0xf4) *v = false; else if (b == 0xf5) *v = true; else return QI_ERR_CBOR;
  r->pos++;
  return QI_OK;
}

static qi_status skip_depth(qi_cbor_r *r, int depth) {
  if (depth > QI_MAX_CBOR_DEPTH) return QI_ERR_CBOR;
  uint8_t mt; uint64_t a;
  qi_status s = read_head(r, &mt, &a);
  if (s) return s;
  switch (mt) {
  case QI_MT_UINT: return QI_OK;
  case QI_MT_BYTES: case QI_MT_TEXT:
    if (a > r->len - r->pos) return QI_ERR_CBOR;
    r->pos += (size_t)a;
    return QI_OK;
  case QI_MT_ARRAY:
    if (a > r->len - r->pos) return QI_ERR_CBOR;
    for (uint64_t i = 0; i < a; i++) { s = skip_depth(r, depth + 1); if (s) return s; }
    return QI_OK;
  case QI_MT_MAP:
    if (a > (r->len - r->pos) / 2) return QI_ERR_CBOR;
    for (uint64_t i = 0; i < a; i++) {
      s = skip_depth(r, depth + 1); if (s) return s;
      s = skip_depth(r, depth + 1); if (s) return s;
    }
    return QI_OK;
  case QI_MT_SIMPLE:
    /* only false/true/null survive, as in codec.SkipItem */
    if (a == 20 || a == 21 || a == 22) return QI_OK;
    return QI_ERR_CBOR;
  default: /* negative ints, tags: never emitted by the protocol */
    return QI_ERR_CBOR;
  }
}

qi_status qi_r_skip(qi_cbor_r *r) { return skip_depth(r, 1); }

qi_status qi_r_done(const qi_cbor_r *r) { return r->pos == r->len ? QI_OK : QI_ERR_CBOR; }

qi_status qi_map_begin(qi_cbor_r *r, qi_cbor_map *m) {
  m->r = r; m->i = 0; m->prev = 0; m->some = false;
  return qi_r_map(r, &m->n);
}

qi_status qi_map_next(qi_cbor_map *m, uint64_t *key, bool *more) {
  if (m->i >= m->n) { *more = false; return QI_OK; }
  uint64_t k;
  qi_status s = qi_r_uint(m->r, &k);
  if (s) return s;
  if (m->some && k <= m->prev) return QI_ERR_CBOR; /* duplicate or out of order */
  m->prev = k; m->some = true; m->i++;
  *key = k; *more = true;
  return QI_OK;
}
