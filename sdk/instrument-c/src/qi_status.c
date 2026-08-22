#include "qi/status.h"

const char *qi_status_str(qi_status s) {
  switch (s) {
  case QI_OK: return "ok";
  case QI_ERR_ARG: return "bad argument";
  case QI_ERR_SPACE: return "buffer too small";
  case QI_ERR_CBOR: return "malformed or non-canonical cbor";
  case QI_ERR_LIMIT: return "compile-time limit exceeded";
  case QI_ERR_CRYPTO: return "cryptographic primitive failed";
  case QI_ERR_VERIFY: return "signature or binding did not verify";
  case QI_ERR_NO_EPOCH: return "no current instrument epoch key";
  case QI_ERR_NO_TIME: return "no unix time source";
  case QI_ERR_STATE: return "called out of order";
  case QI_ERR_NOT_ADDRESSED: return "epoch does not address this device";
  }
  return "unknown";
}
