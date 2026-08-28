#include <stdint.h>
#include <stddef.h>
#include "qi/envelope.h"
#include "qi/crypto.h"
int LLVMFuzzerTestOneInput(const uint8_t *d, size_t n) {
  static int init; if (!init) { qi_crypto_init(); init = 1; }
  qi_envelope e; qi_envelope_decode_verify(d, n, &e);
  return 0;
}
