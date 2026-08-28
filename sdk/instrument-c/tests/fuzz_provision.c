#include <stdint.h>
#include <stddef.h>
#include <string.h>
#include "qi/instrument.h"
#include "qi/crypto.h"
int LLVMFuzzerTestOneInput(const uint8_t *d, size_t n) {
  static int init; static qi_instrument base;
  if (!init) {
    qi_crypto_init(); uint8_t s[32]; memset(s, 1, 32);
    qi_instrument_init(&base); qi_instrument_set_keys(&base, s, s, s); qi_instrument_set_principal(&base, s);
    qi_channel_decl ch = {"t", "number", NULL, NULL};
    qi_instrument_declare(&base, "x", QI_KIND_SENSOR, &ch, 1); init = 1;
  }
  qi_instrument c = base;
  qi_instrument_provision(&c, d, n);
  qi_instrument_state_decode(&c, d, n);
  qi_instrument_identity_decode(&c, d, n);
  return 0;
}
