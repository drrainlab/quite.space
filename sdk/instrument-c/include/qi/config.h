/* qi-core compile-time maxima (QI-M, owner's amendments 8 and 9).
 *
 * The core never allocates: every buffer is caller-owned and every limit
 * is a constant here, so the RAM an instrument needs is known before it
 * boots and stays the same after a year of uptime. Raise a number by
 * editing this file, never by reaching for malloc. */
#ifndef QI_CONFIG_H
#define QI_CONFIG_H

#define QI_MAX_FRAME            2048  /* one signed envelope, bytes */
#define QI_MAX_PAYLOAD          1024  /* plaintext payload inside a frame */
#define QI_MAX_MANIFEST         1536  /* a signed manifest frame */
#define QI_MAX_CHANNELS         24    /* matches terminals.MaxInstrumentChannels */
#define QI_MAX_EPOCH_RECIPIENTS 64    /* wraps scanned in one epoch payload */
#define QI_MAX_EPOCHS_HELD      4     /* epoch keys kept (current + late opens) */
#define QI_MAX_CBOR_DEPTH       8     /* nesting a reader will follow */
#define QI_MAX_STR              96    /* any single text item, bytes */
#define QI_MAX_LABEL            64    /* enrollment label */
#define QI_MAX_ENROLLMENT       (QI_MAX_MANIFEST + 512)
#define QI_MAX_PROVISION        (QI_MAX_FRAME * 2 + 512)
#define QI_MAX_SPACES           2     /* spaces one instrument speaks in */

#endif
