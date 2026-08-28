#include <stdint.h>
#include <stddef.h>
#include <string.h>
#include "qi/instrument.h"
#include "qi/crypto.h"
int LLVMFuzzerTestOneInput(const uint8_t *d, size_t n) {
  static int init; static qi_instrument c; static uint8_t space[32];
  if (!init) { qi_crypto_init(); uint8_t s[32]; memset(s, 1, 32); qi_instrument_init(&c); qi_instrument_set_keys(&c, s, s, s); init = 1; }
  c.nspaces = 0;
  qi_instrument_absorb_epoch_payload(&c, space, d, n, 1);
  return 0;
}
