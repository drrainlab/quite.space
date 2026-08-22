/* The primitives the protocol uses, and exactly those (kernel/crypto):
 *   Ed25519 (device and terminal keys), X25519, SHA-256,
 *   HPKE base mode DHKEM(X25519,HKDF-SHA256)/HKDF-SHA256/ChaCha20-Poly1305
 *     — receiver side only: an instrument unwraps epochs, never wraps,
 *   XChaCha20-Poly1305 with a random 24-byte nonce for sealed payloads.
 * libsodium provides everything except HKDF and HPKE, which are here. */
#ifndef QI_CRYPTO_H
#define QI_CRYPTO_H

#include <stddef.h>
#include <stdint.h>
#include "qi/status.h"

#define QI_ID_SIZE 32
#define QI_SIG_SIZE 64
#define QI_SEED_SIZE 32
#define QI_XNONCE_SIZE 24
#define QI_TAG_SIZE 16
#define QI_HPKE_ENC_SIZE 32

qi_status qi_crypto_init(void);

void qi_sha256(const uint8_t *in, size_t n, uint8_t out[32]);

/* Ed25519 from a 32-byte seed: public key and the 64-byte secret key
 * libsodium signs with. */
void qi_ed25519_from_seed(const uint8_t seed[32], uint8_t pub[32], uint8_t sk[64]);
void qi_ed25519_sign(const uint8_t sk[64], const uint8_t *msg, size_t n, uint8_t sig[64]);
int qi_ed25519_verify(const uint8_t pub[32], const uint8_t *msg, size_t n, const uint8_t sig[64]);

/* X25519: the scalar is stored RAW (as the Go side stores it); libsodium
 * clamps on use. */
qi_status qi_x25519_pub(const uint8_t scalar[32], uint8_t pub[32]);

qi_status qi_hkdf_sha256(const uint8_t *salt, size_t salt_n,
                         const uint8_t *ikm, size_t ikm_n,
                         const uint8_t *info, size_t info_n,
                         uint8_t *out, size_t out_n);

/* HPKE single-shot Open, mode_base, sequence 0, empty aad (RFC 9180):
 * what UnwrapEpoch does for one wrap. ct is the 48-byte (key+tag) field. */
qi_status qi_hpke_open(const uint8_t skR[32], const uint8_t pkR[32],
                       const uint8_t enc[32],
                       const uint8_t *ct, size_t ct_n,
                       const uint8_t *info, size_t info_n,
                       uint8_t *out, size_t out_cap, size_t *out_n);

/* XChaCha20-Poly1305, the sealed-payload AEAD. */
qi_status qi_xchacha_seal(const uint8_t key[32], const uint8_t nonce[24],
                          const uint8_t *pt, size_t pt_n,
                          const uint8_t *aad, size_t aad_n,
                          uint8_t *ct, size_t ct_cap, size_t *ct_n);
qi_status qi_xchacha_open(const uint8_t key[32], const uint8_t nonce[24],
                          const uint8_t *ct, size_t ct_n,
                          const uint8_t *aad, size_t aad_n,
                          uint8_t *pt, size_t pt_cap, size_t *pt_n);

qi_status qi_random(uint8_t *out, size_t n);

#endif
