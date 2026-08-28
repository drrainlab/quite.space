/* The signed envelope (protocol/signal/envelope.go). Key table, append-
 * only: 1 version 2 terminal 3 principal 4 device 5 sequence 6 previous
 * 7 schema 8 created_at 9 logical_clock 10 produced_by 11 human_approved
 * 12 source_terminal 13 payload_encoding 14 payload 15 priority
 * 16 expires_at 17 max_forwards 18 signature.
 * ABSENT-WHEN-ZERO IS LOAD-BEARING: version, created_at, produced_by,
 * human_approved, source_terminal, expires_at and max_forwards are
 * omitted when zero/nil, previous exactly when sequence == 1. The
 * signature covers the canonical map WITHOUT key 18 and with the pair
 * count one less; the event id is SHA-256 of the complete frame. */
#ifndef QI_ENVELOPE_H
#define QI_ENVELOPE_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include "qi/status.h"

#ifdef __cplusplus
extern "C" {
#endif

enum {
  QI_AUTHORSHIP_HUMAN = 1, QI_AUTHORSHIP_SENSOR = 5,
  QI_PAYLOAD_CBOR = 1, QI_PAYLOAD_ENCRYPTED = 2, QI_PAYLOAD_INSTRUMENT_SEALED = 3,
  QI_PRIORITY_MESSAGE = 4, QI_PRIORITY_TELEMETRY = 6, QI_PRIORITY_MANIFEST = 7,
};

typedef struct qi_envelope {
  const uint8_t *terminal;   /* 32 */
  const uint8_t *principal;  /* 32 */
  const uint8_t *device;     /* 32 */
  uint64_t sequence;
  const uint8_t *previous;   /* 32, NULL iff sequence == 1 */
  const char *schema; size_t schema_n;
  uint64_t created_at;
  uint64_t logical_clock;
  uint8_t produced_by;
  bool human_approved;
  const uint8_t *source_terminal; /* 32 or NULL */
  uint8_t payload_encoding;
  const uint8_t *payload; size_t payload_n;
  uint8_t priority;
  uint64_t expires_at;
  uint64_t max_forwards;
  const uint8_t *signature; size_t signature_n; /* set by decode */
} qi_envelope;

/* Build and sign: writes the complete frame. */
qi_status qi_envelope_sign(const qi_envelope *e, const uint8_t device_sk[64],
                           uint8_t *out, size_t cap, size_t *n);

/* Decode a frame into views (no copies) and verify its signature against
 * the device key it names. Refuses non-canonical input, a missing
 * required field, or a bad signature. */
qi_status qi_envelope_decode_verify(const uint8_t *frame, size_t n, qi_envelope *e);

#ifdef __cplusplus
}
#endif

#endif
