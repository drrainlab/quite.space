#include "qi/observation.h"
#include "qi/cbor.h"
#include <string.h>

static int channel_ok(const char *c) {
  size_t n = strlen(c);
  if (n == 0 || n > 32 || c[0] < 'a' || c[0] > 'z') return 0;
  for (size_t i = 1; i < n; i++) {
    char x = c[i];
    if (!((x >= 'a' && x <= 'z') || (x >= '0' && x <= '9') || x == '_' || x == '-')) return 0;
  }
  return 1;
}

qi_status qi_observation_encode(const qi_observation *o, uint8_t *out, size_t cap, size_t *n) {
  if (!o || !o->channel || !channel_ok(o->channel)) return QI_ERR_ARG;
  if (!o->observed_at || !o->stale_after) return QI_ERR_ARG; /* honesty: both mandatory */
  uint64_t magnitude = 0, decimals = 0;
  bool negative = false;
  size_t count = 3; /* channel, observed_at, stale_after */
  switch (o->kind) {
  case QI_VALUE_NUMBER: {
    int64_t m = o->mantissa;
    if (m == INT64_MIN) return QI_ERR_LIMIT;
    negative = m < 0;
    magnitude = (uint64_t)(negative ? -m : m);
    if (o->scale <= 0) {
      decimals = (uint64_t)(-o->scale);
      if (decimals > 18) return QI_ERR_LIMIT;
    } else {
      for (int i = 0; i < o->scale; i++) {
        if (magnitude > UINT64_MAX / 10) return QI_ERR_LIMIT;
        magnitude *= 10;
      }
    }
    if (magnitude == 0) negative = false; /* no negative zero */
    count += 1 + (negative ? 1 : 0) + (decimals ? 1 : 0);
    break;
  }
  case QI_VALUE_BOOL: count += 1; break;
  case QI_VALUE_ENUM:
    if (!o->enum_value || !*o->enum_value || strlen(o->enum_value) > 48) return QI_ERR_ARG;
    count += 1; break;
  default: return QI_ERR_ARG;
  }
  if (o->simulated) count++;
  qi_cbor_w w; qi_w_init(&w, out, cap);
  qi_w_map(&w, count);
  qi_w_uint(&w, 1); qi_w_text(&w, o->channel, strlen(o->channel));
  if (o->kind == QI_VALUE_NUMBER) {
    qi_w_uint(&w, 2); qi_w_uint(&w, magnitude);
    if (negative) { qi_w_uint(&w, 3); qi_w_bool(&w, true); }
    if (decimals) { qi_w_uint(&w, 4); qi_w_uint(&w, decimals); }
  } else if (o->kind == QI_VALUE_BOOL) {
    qi_w_uint(&w, 5); qi_w_bool(&w, o->bool_value);
  } else {
    qi_w_uint(&w, 6); qi_w_text(&w, o->enum_value, strlen(o->enum_value));
  }
  qi_w_uint(&w, 7); qi_w_uint(&w, o->observed_at);
  qi_w_uint(&w, 8); qi_w_uint(&w, o->stale_after);
  if (o->simulated) { qi_w_uint(&w, 9); qi_w_bool(&w, true); }
  return qi_w_done(&w, n);
}
