/* The instrument manifest (protocol/manifest + terminals/instrument):
 * signed by the TERMINAL key, revision-chained by SHA-256 of the previous
 * frame. Kind sensor(5)/actuator(6), io_mode source_only(1), capability
 * signal.publish, publishes observation.value.v1, agency deterministic(2),
 * retention 3600, announce_ttl 300. Channel declarations ride as labels:
 *   qp.instr=<channel>:<kind>[:<unit>][:<label>]
 * unit/label percent-escaped ('%' → %25 first, then ':' → %3A). */
#ifndef QI_MANIFEST_H
#define QI_MANIFEST_H

#include <stdint.h>
#include <stddef.h>
#include "qi/status.h"

#ifdef __cplusplus
extern "C" {
#endif

enum { QI_KIND_SENSOR = 5, QI_KIND_ACTUATOR = 6 };

typedef struct qi_channel_decl {
  const char *channel; /* ^[a-z][a-z0-9_-]{0,31}$ */
  const char *kind;    /* "number" | "boolean" | "enum" */
  const char *unit;    /* may be NULL or "" */
  const char *label;   /* may be NULL or "" */
} qi_channel_decl;

/* Render one declaration as its qp.instr label. */
qi_status qi_channel_label(const qi_channel_decl *d, char *out, size_t cap, size_t *n);

qi_status qi_manifest_sign(const uint8_t terminal[32], const uint8_t controller[32],
                           uint8_t kind, const char *label,
                           const qi_channel_decl *ch, size_t nch,
                           uint64_t revision, const uint8_t *previous_hash /* or NULL */,
                           const uint8_t terminal_sk[64],
                           uint8_t *out, size_t cap, size_t *n);

#ifdef __cplusplus
}
#endif

#endif
