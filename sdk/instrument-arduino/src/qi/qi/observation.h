/* observation.value.v1 (protocol/schemas): {1 channel, 2 magnitude,
 * 3 negative, 4 decimals, 5 bool_value, 6 enum_value, 7 observed_at,
 * 8 stale_after, 9 simulated}. Exactly one value tag; observed_at and
 * stale_after mandatory; no float anywhere.
 *
 * The core's number is (mantissa, scale): 214,-1 → 21.4; -125,-2 → -1.25;
 * 101325,0 → 101325 (owner's amendment 7). Wire: scale ≤ 0 → decimals =
 * -scale; scale > 0 → mantissa·10^scale with overflow refused. */
#ifndef QI_OBSERVATION_H
#define QI_OBSERVATION_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include "qi/status.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef enum { QI_VALUE_NUMBER = 1, QI_VALUE_BOOL = 2, QI_VALUE_ENUM = 3 } qi_value_kind;

typedef struct qi_observation {
  const char *channel;
  qi_value_kind kind;
  int64_t mantissa; int8_t scale; /* number */
  bool bool_value;                /* bool */
  const char *enum_value;         /* enum, ≤ 48 bytes */
  uint64_t observed_at;           /* unix seconds, mandatory */
  uint64_t stale_after;           /* seconds, mandatory */
  bool simulated;
} qi_observation;

qi_status qi_observation_encode(const qi_observation *o, uint8_t *out, size_t cap, size_t *n);

#ifdef __cplusplus
}
#endif

#endif
