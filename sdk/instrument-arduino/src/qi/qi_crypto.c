#include "qi/crypto.h"
#include <sodium.h>
#include <string.h>

qi_status qi_crypto_init(void) { return sodium_init() < 0 ? QI_ERR_CRYPTO : QI_OK; }

void qi_sha256(const uint8_t *in, size_t n, uint8_t out[32]) { crypto_hash_sha256(out, in, n); }

void qi_lan_hint(const uint8_t space[32], uint64_t bucket, uint8_t out[16]) {
  uint8_t buf[15 + 32 + 8];
  memcpy(buf, "qp-lan-hint-v0:", 15);
  memcpy(buf + 15, space, 32);
  for (int i = 0; i < 8; i++) buf[47 + i] = (uint8_t)(bucket >> (56 - 8 * i));
  uint8_t h[32];
  qi_sha256(buf, sizeof buf, h);
  memcpy(out, h, 16);
}

void qi_ed25519_from_seed(const uint8_t seed[32], uint8_t pub[32], uint8_t sk[64]) {
  crypto_sign_seed_keypair(pub, sk, seed);
}

void qi_ed25519_sign(const uint8_t sk[64], const uint8_t *msg, size_t n, uint8_t sig[64]) {
  crypto_sign_detached(sig, NULL, msg, n, sk);
}

int qi_ed25519_verify(const uint8_t pub[32], const uint8_t *msg, size_t n, const uint8_t sig[64]) {
  return crypto_sign_verify_detached(sig, msg, n, pub) == 0;
}

qi_status qi_x25519_pub(const uint8_t scalar[32], uint8_t pub[32]) {
  return crypto_scalarmult_curve25519_base(pub, scalar) == 0 ? QI_OK : QI_ERR_CRYPTO;
}

/* HKDF-SHA256 (RFC 5869) over libsodium's HMAC-SHA256. */
qi_status qi_hkdf_sha256(const uint8_t *salt, size_t salt_n,
                         const uint8_t *ikm, size_t ikm_n,
                         const uint8_t *info, size_t info_n,
                         uint8_t *out, size_t out_n) {
  if (out_n > 255 * 32) return QI_ERR_ARG;
  uint8_t zero[32] = {0};
  uint8_t prk[32];
  crypto_auth_hmacsha256_state st;
  crypto_auth_hmacsha256_init(&st, salt_n ? salt : zero, salt_n ? salt_n : 32);
  crypto_auth_hmacsha256_update(&st, ikm, ikm_n);
  crypto_auth_hmacsha256_final(&st, prk);
  uint8_t t[32];
  size_t tn = 0, done = 0;
  for (uint8_t i = 1; done < out_n; i++) {
    crypto_auth_hmacsha256_init(&st, prk, 32);
    crypto_auth_hmacsha256_update(&st, t, tn);
    crypto_auth_hmacsha256_update(&st, info, info_n);
    crypto_auth_hmacsha256_update(&st, &i, 1);
    crypto_auth_hmacsha256_final(&st, t);
    tn = 32;
    size_t take = out_n - done < 32 ? out_n - done : 32;
    memcpy(out + done, t, take);
    done += take;
  }
  sodium_memzero(prk, sizeof prk);
  sodium_memzero(t, sizeof t);
  return QI_OK;
}

/* ---- HPKE (RFC 9180), receiver side, base mode ----
 * suite: KEM 0x0020 (DHKEM X25519, HKDF-SHA256), KDF 0x0001 (HKDF-SHA256),
 * AEAD 0x0003 (ChaCha20-Poly1305). Nk = 32, Nn = 12, Nh = 32, Nsecret = 32. */

static const uint8_t HPKE_V1[] = {'H','P','K','E','-','v','1'};
static const uint8_t KEM_SUITE[] = {'K','E','M', 0x00, 0x20};
static const uint8_t HPKE_SUITE[] = {'H','P','K','E', 0x00, 0x20, 0x00, 0x01, 0x00, 0x03};

/* LabeledExtract(salt, label, ikm) = Extract(salt, "HPKE-v1"||suite||label||ikm) */
static void labeled_extract(const uint8_t *salt, size_t salt_n,
                            const uint8_t *suite, size_t suite_n,
                            const char *label,
                            const uint8_t *ikm, size_t ikm_n, uint8_t prk[32]) {
  uint8_t zero[32] = {0};
  crypto_auth_hmacsha256_state st;
  crypto_auth_hmacsha256_init(&st, salt_n ? salt : zero, salt_n ? salt_n : 32);
  crypto_auth_hmacsha256_update(&st, HPKE_V1, sizeof HPKE_V1);
  crypto_auth_hmacsha256_update(&st, suite, suite_n);
  crypto_auth_hmacsha256_update(&st, (const uint8_t *)label, strlen(label));
  crypto_auth_hmacsha256_update(&st, ikm, ikm_n);
  crypto_auth_hmacsha256_final(&st, prk);
}

/* LabeledExpand(prk, label, info, L) = Expand(prk, I2OSP(L,2)||"HPKE-v1"||suite||label||info, L) */
static void labeled_expand(const uint8_t prk[32],
                           const uint8_t *suite, size_t suite_n,
                           const char *label,
                           const uint8_t *info, size_t info_n,
                           uint8_t *out, size_t out_n) {
  uint8_t t[32];
  size_t tn = 0, done = 0;
  uint8_t l2[2] = {(uint8_t)(out_n >> 8), (uint8_t)out_n};
  crypto_auth_hmacsha256_state st;
  for (uint8_t i = 1; done < out_n; i++) {
    crypto_auth_hmacsha256_init(&st, prk, 32);
    crypto_auth_hmacsha256_update(&st, t, tn);
    crypto_auth_hmacsha256_update(&st, l2, 2);
    crypto_auth_hmacsha256_update(&st, HPKE_V1, sizeof HPKE_V1);
    crypto_auth_hmacsha256_update(&st, suite, suite_n);
    crypto_auth_hmacsha256_update(&st, (const uint8_t *)label, strlen(label));
    crypto_auth_hmacsha256_update(&st, info, info_n);
    crypto_auth_hmacsha256_update(&st, &i, 1);
    crypto_auth_hmacsha256_final(&st, t);
    tn = 32;
    size_t take = out_n - done < 32 ? out_n - done : 32;
    memcpy(out + done, t, take);
    done += take;
  }
  sodium_memzero(t, sizeof t);
}

qi_status qi_hpke_open(const uint8_t skR[32], const uint8_t pkR[32],
                       const uint8_t enc[32],
                       const uint8_t *ct, size_t ct_n,
                       const uint8_t *info, size_t info_n,
                       uint8_t *out, size_t out_cap, size_t *out_n) {
  if (ct_n < QI_TAG_SIZE || out_cap < ct_n - QI_TAG_SIZE) return QI_ERR_SPACE;
  /* DHKEM Decap: dh = X25519(skR, enc); kem_context = enc || pkR */
  uint8_t dh[32];
  if (crypto_scalarmult_curve25519(dh, skR, enc) != 0) return QI_ERR_CRYPTO;
  uint8_t kem_ctx[64];
  memcpy(kem_ctx, enc, 32);
  memcpy(kem_ctx + 32, pkR, 32);
  uint8_t eae_prk[32], shared[32];
  labeled_extract(NULL, 0, KEM_SUITE, sizeof KEM_SUITE, "eae_prk", dh, 32, eae_prk);
  labeled_expand(eae_prk, KEM_SUITE, sizeof KEM_SUITE, "shared_secret", kem_ctx, 64, shared, 32);
  /* KeySchedule(mode_base, shared, info, psk="", psk_id="") */
  uint8_t psk_id_hash[32], info_hash[32], ksc[1 + 32 + 32];
  labeled_extract(NULL, 0, HPKE_SUITE, sizeof HPKE_SUITE, "psk_id_hash", NULL, 0, psk_id_hash);
  labeled_extract(NULL, 0, HPKE_SUITE, sizeof HPKE_SUITE, "info_hash", info, info_n, info_hash);
  ksc[0] = 0x00; /* mode_base */
  memcpy(ksc + 1, psk_id_hash, 32);
  memcpy(ksc + 33, info_hash, 32);
  uint8_t secret[32], key[32], base_nonce[12];
  labeled_extract(shared, 32, HPKE_SUITE, sizeof HPKE_SUITE, "secret", NULL, 0, secret);
  labeled_expand(secret, HPKE_SUITE, sizeof HPKE_SUITE, "key", ksc, sizeof ksc, key, 32);
  labeled_expand(secret, HPKE_SUITE, sizeof HPKE_SUITE, "base_nonce", ksc, sizeof ksc, base_nonce, 12);
  /* seq 0: nonce = base_nonce XOR 0 */
  unsigned long long mlen = 0;
  int rc = crypto_aead_chacha20poly1305_ietf_decrypt(out, &mlen, NULL, ct, ct_n, NULL, 0, base_nonce, key);
  sodium_memzero(dh, 32); sodium_memzero(eae_prk, 32); sodium_memzero(shared, 32);
  sodium_memzero(secret, 32); sodium_memzero(key, 32);
  if (rc != 0) return QI_ERR_CRYPTO;
  *out_n = (size_t)mlen;
  return QI_OK;
}

qi_status qi_xchacha_seal(const uint8_t key[32], const uint8_t nonce[24],
                          const uint8_t *pt, size_t pt_n,
                          const uint8_t *aad, size_t aad_n,
                          uint8_t *ct, size_t ct_cap, size_t *ct_n) {
  if (ct_cap < pt_n + QI_TAG_SIZE) return QI_ERR_SPACE;
  unsigned long long clen = 0;
  if (crypto_aead_xchacha20poly1305_ietf_encrypt(ct, &clen, pt, pt_n, aad, aad_n, NULL, nonce, key) != 0)
    return QI_ERR_CRYPTO;
  *ct_n = (size_t)clen;
  return QI_OK;
}

qi_status qi_xchacha_open(const uint8_t key[32], const uint8_t nonce[24],
                          const uint8_t *ct, size_t ct_n,
                          const uint8_t *aad, size_t aad_n,
                          uint8_t *pt, size_t pt_cap, size_t *pt_n) {
  if (ct_n < QI_TAG_SIZE || pt_cap < ct_n - QI_TAG_SIZE) return QI_ERR_SPACE;
  unsigned long long mlen = 0;
  if (crypto_aead_xchacha20poly1305_ietf_decrypt(pt, &mlen, NULL, ct, ct_n, aad, aad_n, nonce, key) != 0)
    return QI_ERR_CRYPTO;
  *pt_n = (size_t)mlen;
  return QI_OK;
}

qi_status qi_random(uint8_t *out, size_t n) { randombytes_buf(out, n); return QI_OK; }
