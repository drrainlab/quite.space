#include <stdint.h>
#include <stddef.h>
#include "qi/cbor.h"
int LLVMFuzzerTestOneInput(const uint8_t *d, size_t n) {
  qi_cbor_r r; qi_r_init(&r, d, n);
  while (r.pos < r.len) if (qi_r_skip(&r)) break;
  qi_r_init(&r, d, n);
  qi_cbor_map m; uint64_t k; bool more;
  if (qi_map_begin(&r, &m) == QI_OK)
    while (qi_map_next(&m, &k, &more) == QI_OK && more) if (qi_r_skip(&r)) break;
  return 0;
}
