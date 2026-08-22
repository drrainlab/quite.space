/* Hostile input without libFuzzer: every parser in the core is fed the
 * golden corpus under thousands of deterministic mutations — bit flips,
 * byte substitutions, truncations, insertions — under ASan+UBSan. A
 * parser may refuse; it may never read out of bounds, overflow, or
 * accept a damaged record as genuine. Deterministic (xorshift seed), so
 * a failure reproduces. */
#include <stdio.h>
#include <string.h>
#include "qi/cbor.h"
#include "qi/crypto.h"
#include "qi/envelope.h"
#include "qi/instrument.h"
#include "vectors.h"

static uint64_t rng = 0x9e3779b97f4a7c15ULL;
static uint32_t rnd(void) { rng ^= rng << 13; rng ^= rng >> 7; rng ^= rng << 17; return (uint32_t)rng; }

static int fails;
#define CHECK(cond, msg) do { if (!(cond)) { printf("FAIL %s:%d %s\n", __FILE__, __LINE__, msg); fails++; } } while (0)

typedef void (*feed_fn)(const uint8_t *d, size_t n);

static void feed_cbor(const uint8_t *d, size_t n) {
  qi_cbor_r r; qi_r_init(&r, d, n);
  while (r.pos < r.len) if (qi_r_skip(&r)) break;
}
static void feed_envelope(const uint8_t *d, size_t n) { qi_envelope e; qi_envelope_decode_verify(d, n, &e); }
static qi_instrument ctx; static uint8_t space[32];
static void feed_epoch(const uint8_t *d, size_t n) { ctx.nspaces = 0; qi_instrument_absorb_epoch_payload(&ctx, space, d, n, 1); }
static void feed_provision(const uint8_t *d, size_t n) { qi_instrument c = ctx; qi_instrument_provision(&c, d, n); }
static void feed_state(const uint8_t *d, size_t n) { qi_instrument c = ctx; qi_instrument_state_decode(&c, d, n); qi_instrument_identity_decode(&c, d, n); }

static void mutate_all(const char *name, feed_fn fn, const uint8_t *seed, size_t n, int rounds) {
  uint8_t buf[8192];
  if (n > sizeof buf) return;
  for (int i = 0; i < rounds; i++) {
    size_t len = n;
    memcpy(buf, seed, n);
    switch (rnd() % 5) {
    case 0: buf[rnd() % len] ^= (uint8_t)(1u << (rnd() % 8)); break;                /* bit flip */
    case 1: buf[rnd() % len] = (uint8_t)rnd(); break;                              /* byte */
    case 2: len = rnd() % (len + 1); break;                                        /* truncate */
    case 3: { size_t at = rnd() % len; memmove(buf + at + 1, buf + at, len - at); buf[at] = (uint8_t)rnd(); len++; break; } /* insert */
    case 4: for (int k = 0; k < 4; k++) buf[rnd() % len] = (uint8_t)rnd(); break; /* several */
    }
    fn(buf, len);
  }
  printf("  %s: %d mutations survived\n", name, rounds);
}

int main(int argc, char **argv) {
  if (argc < 2 || !vec_load(argv[1])) return 2;
  qi_crypto_init();
  uint8_t s[32]; memset(s, 1, 32);
  qi_instrument_init(&ctx); qi_instrument_set_keys(&ctx, s, s, s); qi_instrument_set_principal(&ctx, s);
  qi_channel_decl ch = {"t", "number", NULL, NULL};
  qi_instrument_declare(&ctx, "x", QI_KIND_SENSOR, &ch, 1);
  vec_hex("space_id", space, 32);
  uint8_t frame[4096], epoch[1024], enr[4096], rec[8192];
  size_t fn = vec_hex("instrument_envelope_frame", frame, sizeof frame);
  size_t en = vec_hex("epoch_payload_cbor", epoch, sizeof epoch);
  size_t rn = vec_hex("enrollment_v1", enr, sizeof enr);
  size_t recn; qi_instrument_identity_encode(&ctx, rec, sizeof rec, &recn);
  /* genuine inputs are accepted before mutation starts */
  qi_envelope e; CHECK(qi_envelope_decode_verify(frame, fn, &e) == QI_OK, "genuine frame verifies");
  CHECK(qi_instrument_identity_decode(&ctx, rec, recn) == QI_OK, "genuine record decodes");
  mutate_all("cbor/skip over envelope", feed_cbor, frame, fn, 20000);
  mutate_all("cbor/skip over enrollment", feed_cbor, enr, rn, 20000);
  mutate_all("envelope decode+verify", feed_envelope, frame, fn, 20000);
  mutate_all("epoch payload", feed_epoch, epoch, en, 20000);
  mutate_all("provision", feed_provision, enr, rn, 10000);
  mutate_all("state/identity records", feed_state, rec, recn, 20000);
  /* a mutated signed frame must NEVER verify: 5000 random single-byte changes */
  int accepted = 0;
  for (int i = 0; i < 5000; i++) {
    uint8_t b[4096]; memcpy(b, frame, fn);
    size_t at = rnd() % fn; uint8_t v = (uint8_t)rnd(); if (v == b[at]) v ^= 1; b[at] = v;
    if (qi_envelope_decode_verify(b, fn, &e) == QI_OK) accepted++;
  }
  CHECK(accepted == 0, "a damaged signed frame verified");
  printf(fails ? "FAILED: %d\n" : "PASS\n", fails);
  return fails ? 1 : 0;
}
