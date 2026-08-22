/* Canonical CBOR, the protocol's restricted subset (ADR-003,
 * protocol/codec/codec.go): unsigned ints, byte strings, text strings,
 * arrays, maps with strictly ascending unsigned keys, true/false.
 * Shortest-form heads are MANDATORY on both sides; a reader that meets
 * a non-shortest int, a tag, a float, an indefinite length or a key out
 * of order refuses — fail closed, exactly as the Go decoder does. */
#ifndef QI_CBOR_H
#define QI_CBOR_H

#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include "qi/status.h"

typedef struct qi_cbor_w {
  uint8_t *buf;
  size_t cap;
  size_t len;
  bool overflow; /* sticky: once set, the result is unusable */
} qi_cbor_w;

void qi_w_init(qi_cbor_w *w, uint8_t *buf, size_t cap);
void qi_w_uint(qi_cbor_w *w, uint64_t v);
void qi_w_bytes(qi_cbor_w *w, const uint8_t *b, size_t n);
void qi_w_text(qi_cbor_w *w, const char *s, size_t n);
void qi_w_array(qi_cbor_w *w, size_t n);
void qi_w_map(qi_cbor_w *w, size_t n);
void qi_w_bool(qi_cbor_w *w, bool v);
/* qi_w_done: QI_OK with the length, or QI_ERR_SPACE if anything overflowed. */
qi_status qi_w_done(const qi_cbor_w *w, size_t *len);

typedef struct qi_cbor_r {
  const uint8_t *p;
  size_t len;
  size_t pos;
} qi_cbor_r;

enum { QI_MT_UINT = 0, QI_MT_NEG = 1, QI_MT_BYTES = 2, QI_MT_TEXT = 3,
       QI_MT_ARRAY = 4, QI_MT_MAP = 5, QI_MT_TAG = 6, QI_MT_SIMPLE = 7 };

void qi_r_init(qi_cbor_r *r, const uint8_t *p, size_t len);
/* Peek the next head without consuming: major type and argument. */
qi_status qi_r_peek(const qi_cbor_r *r, uint8_t *major, uint64_t *arg);
qi_status qi_r_uint(qi_cbor_r *r, uint64_t *v);
/* Byte/text strings are returned as views into the input. */
qi_status qi_r_bytes(qi_cbor_r *r, const uint8_t **b, size_t *n);
qi_status qi_r_text(qi_cbor_r *r, const char **s, size_t *n);
qi_status qi_r_array(qi_cbor_r *r, size_t *n);
qi_status qi_r_map(qi_cbor_r *r, size_t *n);
qi_status qi_r_bool(qi_cbor_r *r, bool *v);
/* Skip one complete item (forward compatibility), depth-limited. */
qi_status qi_r_skip(qi_cbor_r *r);
/* QI_OK only if every byte was consumed. */
qi_status qi_r_done(const qi_cbor_r *r);

/* Map iteration with the ascending-key rule enforced. */
typedef struct qi_cbor_map {
  qi_cbor_r *r;
  size_t n, i;
  uint64_t prev;
  bool some;
} qi_cbor_map;

qi_status qi_map_begin(qi_cbor_r *r, qi_cbor_map *m);
/* Next key: QI_OK with *more=true and the key, QI_OK with *more=false at
 * the end, or an error for a key out of order. */
qi_status qi_map_next(qi_cbor_map *m, uint64_t *key, bool *more);

#endif
