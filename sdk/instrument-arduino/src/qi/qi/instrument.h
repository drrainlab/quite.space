/* The instrument: one device, its keys, its declaration, and for every
 * space it was provisioned into — its chain, its clock, the instrument
 * epoch keys it may hold, and the ONE frame it may owe the world.
 *
 * Transport-blind by construction: frames come out of qi_instrument_emit
 * as bytes, epoch frames go into qi_instrument_absorb_epoch_frame as
 * bytes, and nothing here knows what carried them.
 *
 * HONESTY, FAIL CLOSED (ADR-023): no unix time → QI_ERR_NO_TIME; no
 * current epoch key → QI_ERR_NO_EPOCH; stale_after == 0 → refused. A
 * reading is never stamped with a guessed clock.
 *
 * DURABILITY (owner's amendments 1 and 11): emit builds the frame, then
 * hands the platform ONE serialized state record — new sequence, new tip,
 * the complete pending frame, a CRC — and only after the platform
 * reports it written does the frame leave. After a reboot the platform
 * loads the record and qi_instrument_pending hands the frame back to be
 * sent first. The platform's duty is that the record is written
 * crash-consistently (an A/B slot journal); the core's duty is that the
 * chain can never have a hole. */
#ifndef QI_INSTRUMENT_H
#define QI_INSTRUMENT_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include "qi/status.h"
#include "qi/config.h"
#include "qi/manifest.h"
#include "qi/observation.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct qi_epoch_key { uint64_t n; uint8_t key[32]; } qi_epoch_key;

typedef struct qi_space_state {
  uint8_t space[32];
  uint64_t seq;      /* last sequence emitted (0 = none yet) */
  uint8_t tip[32];   /* event id of the last emitted frame */
  uint64_t clock;    /* highest Lamport clock seen or spent */
  uint64_t current;  /* current instrument epoch number (0 = none) */
  qi_epoch_key epochs[QI_MAX_EPOCHS_HELD];
  size_t nepochs;
  size_t pending_n;  /* the frame owed, 0 = none */
  uint8_t pending[QI_MAX_FRAME];
} qi_space_state;

typedef struct qi_channel_copy {
  char channel[33];
  char kind[8];
  char unit[QI_MAX_STR];
  char label[QI_MAX_STR];
} qi_channel_copy;

typedef struct qi_instrument {
  /* identity */
  bool have_keys;
  uint8_t device_seed[32], device_sk[64], device_pub[32];
  uint8_t x25519[32], x25519_pub[32];
  uint8_t terminal_seed[32], terminal_sk[64], terminal_pub[32];
  /* declaration */
  bool declared;
  char label[QI_MAX_LABEL + 1];
  uint8_t kind;
  qi_channel_copy channels[QI_MAX_CHANNELS];
  size_t nchannels;
  uint8_t manifest[QI_MAX_MANIFEST];
  size_t manifest_n;
  uint8_t manifest_hash[32];
  /* provision */
  bool have_principal; /* learned out of band before enrollment */
  bool provisioned;
  uint8_t principal[32];
  uint8_t cert[256];
  size_t cert_n;
  /* spaces */
  qi_space_state spaces[QI_MAX_SPACES];
  size_t nspaces;
  uint64_t generation; /* state record counter */
  /* platform hooks */
  qi_status (*persist)(void *ud, const uint8_t *record, size_t n);
  void *persist_ud;
  uint64_t (*now)(void *ud); /* unix seconds; 0 = unknown */
  void *now_ud;
} qi_instrument;

void qi_instrument_init(qi_instrument *c);
qi_status qi_instrument_set_keys(qi_instrument *c, const uint8_t device_seed[32],
                                 const uint8_t x25519_scalar[32], const uint8_t terminal_seed[32]);
qi_status qi_instrument_keygen(qi_instrument *c);

/* The principal the instrument will be controlled by — learned out of
 * band (the owner's QR, a serial prompt) BEFORE declaring, because the
 * manifest names its controller and the enrollment signs the manifest. */
qi_status qi_instrument_set_principal(qi_instrument *c, const uint8_t principal[32]);

qi_status qi_instrument_declare(qi_instrument *c, const char *label, uint8_t kind,
                                const qi_channel_decl *ch, size_t nch);

/* instrument.enrollment.v1 — what the device hands the authority. */
qi_status qi_instrument_enrollment(const qi_instrument *c, const uint8_t nonce[16],
                                   uint8_t *out, size_t cap, size_t *n);

/* instrument.provision.v1 — what comes back. Absorbs the epoch frames. */
qi_status qi_instrument_provision(qi_instrument *c, const uint8_t *bytes, size_t n);

/* A membership.instrument_epoch.v1 envelope frame for a provisioned space. */
qi_status qi_instrument_absorb_epoch_frame(qi_instrument *c, const uint8_t *frame, size_t n);
/* Tool/test entry: an epoch PAYLOAD for a space, no envelope. */
qi_status qi_instrument_absorb_epoch_payload(qi_instrument *c, const uint8_t space[32],
                                             const uint8_t *payload, size_t n, uint64_t clock);

/* Emit one reading: seals, signs, persists the pending frame, returns it. */
qi_status qi_instrument_emit(qi_instrument *c, const uint8_t space[32], const qi_observation *o,
                             uint8_t *out, size_t cap, size_t *n);
/* The frame owed (after a reboot, or before ack). 0 bytes = none. */
qi_status qi_instrument_pending(const qi_instrument *c, const uint8_t space[32],
                                uint8_t *out, size_t cap, size_t *n);
/* The bearer delivered it: clear and persist. */
qi_status qi_instrument_ack_sent(qi_instrument *c, const uint8_t space[32]);

/* The durable records. identity: keys + declaration + provision (written
 * on change); state: chains, clocks, epochs, pending (written per emit).
 * Both end in a CRC-32; decode refuses a record whose CRC disagrees. */
qi_status qi_instrument_identity_encode(const qi_instrument *c, uint8_t *out, size_t cap, size_t *n);
qi_status qi_instrument_identity_decode(qi_instrument *c, const uint8_t *rec, size_t n);
qi_status qi_instrument_state_encode(const qi_instrument *c, uint8_t *out, size_t cap, size_t *n);
qi_status qi_instrument_state_decode(qi_instrument *c, const uint8_t *rec, size_t n);

/* The current epoch key of a space, if held (for tests and diagnostics). */
const qi_epoch_key *qi_instrument_current_epoch(const qi_instrument *c, const uint8_t space[32]);

uint32_t qi_crc32(const uint8_t *b, size_t n);

#ifdef __cplusplus
}
#endif

#endif
