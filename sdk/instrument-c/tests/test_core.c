/* Host tests against the Go golden vectors (testvectors/instrument_v1.json
 * and protocol_v0.json): cbor thresholds, ids, HPKE unwrap of the
 * captured wrap, the fixed-nonce seal in both directions. */
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include "qi/cbor.h"
#include "qi/crypto.h"
#include "qi/ids.h"
#include "vectors.h"

static int fails;
#define CHECK(cond, msg) do { if (!(cond)) { printf("FAIL %s:%d %s\n", __FILE__, __LINE__, msg); fails++; } } while (0)

static void test_cbor_thresholds(void) {
  uint8_t buf[64];
  struct { uint64_t v; const char *hex; } cases[] = {
    {0, "00"}, {23, "17"}, {24, "1818"}, {255, "18ff"}, {256, "190100"},
    {65535, "19ffff"}, {65536, "1a00010000"}, {4294967295ULL, "1affffffff"},
    {4294967296ULL, "1b0000000100000000"},
  };
  for (size_t i = 0; i < sizeof cases / sizeof cases[0]; i++) {
    qi_cbor_w w; qi_w_init(&w, buf, sizeof buf);
    qi_w_uint(&w, cases[i].v);
    size_t n; CHECK(qi_w_done(&w, &n) == QI_OK, "write");
    char hex[40]; for (size_t k = 0; k < n; k++) sprintf(hex + 2 * k, "%02x", buf[k]);
    CHECK(strcmp(hex, cases[i].hex) == 0, "shortest head");
    qi_cbor_r r; qi_r_init(&r, buf, n);
    uint64_t v; CHECK(qi_r_uint(&r, &v) == QI_OK && v == cases[i].v && qi_r_done(&r) == QI_OK, "read back");
  }
  /* fail-closed: non-shortest, indefinite, tag, float, trailing, out-of-order keys, duplicate */
  const uint8_t non_shortest[] = {0x18, 0x05};
  qi_cbor_r r; uint64_t v;
  qi_r_init(&r, non_shortest, 2); CHECK(qi_r_uint(&r, &v) == QI_ERR_CBOR, "non-shortest refused");
  const uint8_t indef[] = {0x9f};
  size_t n; qi_r_init(&r, indef, 1); CHECK(qi_r_array(&r, &n) == QI_ERR_CBOR, "indefinite refused");
  const uint8_t tag[] = {0xc0, 0x01};
  qi_r_init(&r, tag, 2); CHECK(qi_r_skip(&r) == QI_ERR_CBOR, "tag refused");
  const uint8_t flt[] = {0xf9, 0x00, 0x00};
  qi_r_init(&r, flt, 3); CHECK(qi_r_skip(&r) == QI_ERR_CBOR, "float refused");
  const uint8_t trailing[] = {0x01, 0x02};
  qi_r_init(&r, trailing, 2); CHECK(qi_r_uint(&r, &v) == QI_OK && qi_r_done(&r) == QI_ERR_CBOR, "trailing refused");
  const uint8_t disorder[] = {0xa2, 0x02, 0x01, 0x01, 0x01};
  qi_r_init(&r, disorder, 5); qi_cbor_map m; bool more; uint64_t k;
  CHECK(qi_map_begin(&r, &m) == QI_OK && qi_map_next(&m, &k, &more) == QI_OK, "first key");
  qi_r_uint(&r, &v);
  CHECK(qi_map_next(&m, &k, &more) == QI_ERR_CBOR, "out-of-order key refused");
  const uint8_t dup[] = {0xa2, 0x01, 0x01, 0x01, 0x02};
  qi_r_init(&r, dup, 5); qi_map_begin(&r, &m); qi_map_next(&m, &k, &more); qi_r_uint(&r, &v);
  CHECK(qi_map_next(&m, &k, &more) == QI_ERR_CBOR, "duplicate key refused");
  const uint8_t truncated[] = {0x58, 0x20, 0x01};
  const uint8_t *b; qi_r_init(&r, truncated, 3); CHECK(qi_r_bytes(&r, &b, &n) == QI_ERR_CBOR, "truncated refused");
  const uint8_t deep[] = {0x81,0x81,0x81,0x81,0x81,0x81,0x81,0x81,0x81,0x81,0x01};
  qi_r_init(&r, deep, sizeof deep); CHECK(qi_r_skip(&r) == QI_ERR_CBOR, "nesting limit");
  const uint8_t badutf8[] = {0x62, 0xff, 0xfe};
  const char *s; qi_r_init(&r, badutf8, 3); CHECK(qi_r_text(&r, &s, &n) == QI_ERR_CBOR, "invalid utf-8 refused");
}

static void test_ids_and_keys(void) {
  uint8_t seed[32], want[32], pub[32], sk[64];
  CHECK(vec_hex("device_seed", seed, 32) == 32 && vec_hex("device_id", want, 32) == 32, "device vector");
  qi_ed25519_from_seed(seed, pub, sk);
  CHECK(memcmp(pub, want, 32) == 0, "device id = ed25519 pub of seed");
  CHECK(vec_hex("terminal_seed", seed, 32) == 32 && vec_hex("terminal_id", want, 32) == 32, "terminal vector");
  qi_ed25519_from_seed(seed, pub, sk);
  CHECK(memcmp(pub, want, 32) == 0, "terminal id = ed25519 pub of seed");
  uint8_t scalar[32], xpub[32];
  CHECK(vec_hex("device_x25519_scalar", scalar, 32) == 32 && vec_hex("device_x25519_pub", want, 32) == 32, "x25519 vector");
  CHECK(qi_x25519_pub(scalar, xpub) == QI_OK && memcmp(xpub, want, 32) == 0, "x25519 pub of raw scalar");
  uint8_t space[32], plane[32];
  CHECK(vec_hex("space_id", space, 32) == 32 && vec_hex("instrument_plane_id", want, 32) == 32, "plane vector");
  qi_instrument_plane_id(space, plane);
  CHECK(memcmp(plane, want, 32) == 0, "instrument plane id formula");
  uint8_t frame[2048], eid[32];
  size_t fn = vec_hex("instrument_envelope_frame", frame, sizeof frame);
  CHECK(fn > 0 && vec_hex("instrument_envelope_event_id", want, 32) == 32, "frame vector");
  qi_event_id(frame, fn, eid);
  CHECK(memcmp(eid, want, 32) == 0, "event id = sha256(frame)");
}

static void test_hpke_unwrap_and_seal(void) {
  uint8_t scalar[32], xpub[32], enc[32], ct[64], key[32], got[64], space[32], plane[32];
  CHECK(vec_hex("device_x25519_scalar", scalar, 32) == 32, "scalar");
  CHECK(vec_hex("device_x25519_pub", xpub, 32) == 32, "xpub");
  CHECK(vec_hex("epoch_wrap_enc", enc, 32) == 32, "enc");
  size_t ctn = vec_hex("epoch_wrap_ct", ct, sizeof ct);
  CHECK(ctn == 48, "ct 48 bytes");
  CHECK(vec_hex("epoch_key", key, 32) == 32, "epoch key");
  CHECK(vec_hex("space_id", space, 32) == 32, "space");
  qi_instrument_plane_id(space, plane);
  uint64_t n = vec_u64("epoch_n");
  /* info = "quiet-places-epoch-v0:" ‖ plane ‖ cbor(n) */
  uint8_t info[22 + 32 + 9]; size_t in = 0;
  memcpy(info, "quiet-places-epoch-v0:", 22); in = 22;
  memcpy(info + in, plane, 32); in += 32;
  qi_cbor_w w; qi_w_init(&w, info + in, 9); qi_w_uint(&w, n); size_t wn; qi_w_done(&w, &wn); in += wn;
  size_t gotn = 0;
  CHECK(qi_hpke_open(scalar, xpub, enc, ct, ctn, info, in, got, sizeof got, &gotn) == QI_OK, "hpke open");
  CHECK(gotn == 32 && memcmp(got, key, 32) == 0, "unwrapped epoch key == fixed key");
  /* a bit flipped in the wrap must fail, never yield a wrong key */
  ct[5] ^= 1;
  CHECK(qi_hpke_open(scalar, xpub, enc, ct, ctn, info, in, got, sizeof got, &gotn) == QI_ERR_CRYPTO, "tampered wrap refused");

  /* the fixed-nonce seal: seal == vector, open == observation */
  uint8_t nonce[24], aad[96], obs[128], sealed[256], mine[256], pt[128];
  CHECK(vec_hex("seal_nonce", nonce, 24) == 24, "nonce");
  size_t aadn = vec_hex("seal_aad", aad, sizeof aad);
  size_t obsn = vec_hex("observation_value_payload", obs, sizeof obs);
  size_t sealn = vec_hex("sealed_observation_payload", sealed, sizeof sealed);
  CHECK(aadn && obsn && sealn, "seal vectors");
  /* aad recomputed from the formula must equal the vector's */
  uint8_t aad2[96]; size_t a2 = 0;
  memcpy(aad2, plane, 32); a2 = 32;
  qi_w_init(&w, aad2 + a2, 9); qi_w_uint(&w, n); qi_w_done(&w, &wn); a2 += wn;
  memcpy(aad2 + a2, "observation.value.v1", 20); a2 += 20;
  CHECK(a2 == aadn && memcmp(aad, aad2, aadn) == 0, "seal aad formula");
  size_t ctn2;
  uint8_t ctbuf[256];
  CHECK(qi_xchacha_seal(key, nonce, obs, obsn, aad, aadn, ctbuf, sizeof ctbuf, &ctn2) == QI_OK, "seal");
  qi_w_init(&w, mine, sizeof mine);
  qi_w_map(&w, 3); qi_w_uint(&w, 1); qi_w_uint(&w, n); qi_w_uint(&w, 2); qi_w_bytes(&w, nonce, 24);
  qi_w_uint(&w, 3); qi_w_bytes(&w, ctbuf, ctn2);
  size_t minen; CHECK(qi_w_done(&w, &minen) == QI_OK, "sealed map");
  CHECK(minen == sealn && memcmp(mine, sealed, sealn) == 0, "sealed payload == vector");
  size_t ptn;
  CHECK(qi_xchacha_open(key, nonce, ctbuf, ctn2, aad, aadn, pt, sizeof pt, &ptn) == QI_OK && ptn == obsn &&
        memcmp(pt, obs, obsn) == 0, "open == observation");
}

/* The two-implementation law: transports/lan.Hint computed these bytes
 * once (Go), and both sides pin them forever. terminal[i] = i*7. */
static void test_lan_hint_vectors(void) {
  uint8_t space[32];
  for (int i = 0; i < 32; i++) space[i] = (uint8_t)(i * 7);
  static const uint8_t v0[16]  = {0x4b,0x70,0x9c,0x48,0x1f,0x8f,0x7a,0x43,
                                  0x4d,0x69,0x2c,0xfc,0xbc,0x89,0xfb,0x2b};
  static const uint8_t v1[16]  = {0x7c,0x04,0x8f,0xee,0xc8,0x58,0x2e,0x92,
                                  0x4d,0xb7,0x01,0x0b,0x95,0xea,0x0d,0xb2};
  static const uint8_t v2[16]  = {0x43,0xeb,0x85,0x0b,0x48,0xb0,0x8f,0xc7,
                                  0x09,0x79,0x62,0xe9,0x66,0xa5,0xb0,0x52};
  uint8_t out[16];
  qi_lan_hint(space, 0, out);
  CHECK(memcmp(out, v0, 16) == 0, "hint bucket 0 == Go");
  qi_lan_hint(space, 82911, out);
  CHECK(memcmp(out, v1, 16) == 0, "hint bucket 82911 == Go");
  CHECK(qi_lan_bucket(1788400000u) == 82796u, "bucket formula == Go");
  qi_lan_hint(space, 82796, out);
  CHECK(memcmp(out, v2, 16) == 0, "hint current bucket == Go");
}

int main(int argc, char **argv) {
  if (argc < 2 || !vec_load(argv[1])) { printf("usage: test_core instrument_v1.json\n"); return 2; }
  if (qi_crypto_init() != QI_OK) { printf("sodium init failed\n"); return 2; }
  test_cbor_thresholds();
  test_ids_and_keys();
  test_hpke_unwrap_and_seal();
  test_lan_hint_vectors();
  printf(fails ? "FAILED: %d\n" : "PASS\n", fails);
  return fails ? 1 : 0;
}
