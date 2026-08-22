/* Identifiers (protocol/id): every id is a raw 32-byte Ed25519 public
 * key — no hashing — except event ids and hashes, which are SHA-256 of
 * the complete frame. The instrument plane id is the one derived id:
 * SHA256("qp.instrument-plane.v1" ‖ space). */
#ifndef QI_IDS_H
#define QI_IDS_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

void qi_event_id(const uint8_t *frame, size_t n, uint8_t out[32]);
void qi_instrument_plane_id(const uint8_t space[32], uint8_t out[32]);

#ifdef __cplusplus
}
#endif

#endif
