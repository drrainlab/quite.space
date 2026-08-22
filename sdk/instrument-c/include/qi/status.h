#ifndef QI_STATUS_H
#define QI_STATUS_H

#ifdef __cplusplus
extern "C" {
#endif

/* Every qi-core call answers with one of these. The doctrine is the
 * protocol's own (ADR-023): inability is never success, never silence —
 * a missing epoch, a missing clock, a buffer too small are named, not
 * papered over. */
typedef enum qi_status {
  QI_OK = 0,
  QI_ERR_ARG,        /* a null or out-of-range argument */
  QI_ERR_SPACE,      /* caller buffer too small (never a partial write) */
  QI_ERR_CBOR,       /* malformed or non-canonical CBOR on input */
  QI_ERR_LIMIT,      /* a compile-time maximum would be exceeded */
  QI_ERR_CRYPTO,     /* a primitive failed (bad tag, bad key, RNG) */
  QI_ERR_VERIFY,     /* a signature or binding did not verify */
  QI_ERR_NO_EPOCH,   /* no current instrument epoch key: cannot seal */
  QI_ERR_NO_TIME,    /* no unix time source: cannot stamp a reading */
  QI_ERR_STATE,      /* called out of order (unprovisioned, unknown space) */
  QI_ERR_NOT_ADDRESSED /* an epoch wrap list does not name this device */
} qi_status;

const char *qi_status_str(qi_status s);

#ifdef __cplusplus
}
#endif

#endif
