#include "qi/ids.h"
#include "qi/crypto.h"
#include <string.h>

void qi_event_id(const uint8_t *frame, size_t n, uint8_t out[32]) { qi_sha256(frame, n, out); }

void qi_instrument_plane_id(const uint8_t space[32], uint8_t out[32]) {
  static const char tag[] = "qp.instrument-plane.v1";
  uint8_t buf[sizeof tag - 1 + 32];
  memcpy(buf, tag, sizeof tag - 1);
  memcpy(buf + sizeof tag - 1, space, 32);
  qi_sha256(buf, sizeof buf, out);
}
