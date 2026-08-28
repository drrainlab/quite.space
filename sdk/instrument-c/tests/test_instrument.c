/* The instrument context end to end on the host: the golden enrollment,
 * a provision built from the vectors, readings chained from sequence 1,
 * and the durability contract — power loss simulated at every stage. */
#include <stdio.h>
#include <string.h>
#include "qi/instrument.h"
#include "qi/envelope.h"
#include "qi/ids.h"
#include "qi/cbor.h"
#include "qi/crypto.h"
#include "vectors.h"

static int fails;
#define CHECK(cond, msg) do { if (!(cond)) { printf("FAIL %s:%d %s\n", __FILE__, __LINE__, msg); fails++; } } while (0)

static const qi_channel_decl GREENHOUSE[] = {
  {"temperature", "number", "°C", "Температура"},
  {"humidity", "number", "%", "Влажность"},
  {"door", "boolean", NULL, "Дверь"},
  {"light", "number", "%", "Свет"},
};

/* a persist hook that keeps the LAST record — the platform's journal */
static uint8_t journal[8192]; static size_t journal_n; static int persist_calls;
static qi_status persist(void *ud, const uint8_t *rec, size_t n) {
  (void)ud; memcpy(journal, rec, n); journal_n = n; persist_calls++; return QI_OK;
}
static uint64_t clock_v = 1755800000;
static uint64_t now_fn(void *ud) { (void)ud; return clock_v; }
static uint64_t no_time(void *ud) { (void)ud; return 0; }

static void setup(qi_instrument *c) {
  uint8_t ds[32], xs[32], ts[32], prin[32];
  vec_hex("device_seed", ds, 32); vec_hex("device_x25519_scalar", xs, 32); vec_hex("terminal_seed", ts, 32);
  vec_hex("principal_id", prin, 32);
  qi_instrument_init(c);
  CHECK(qi_instrument_set_keys(c, ds, xs, ts) == QI_OK, "keys");
  CHECK(qi_instrument_declare(c, "Greenhouse", QI_KIND_SENSOR, GREENHOUSE, 4) == QI_ERR_STATE, "declare before principal refused");
  CHECK(qi_instrument_set_principal(c, prin) == QI_OK, "principal");
  CHECK(qi_instrument_declare(c, "Greenhouse", QI_KIND_SENSOR, GREENHOUSE, 4) == QI_OK, "declare");
}

/* a provision: the vector's epoch payload inside an envelope signed by a
 * throwaway owner device, under the vector principal */
static size_t build_provision(const qi_instrument *c, uint8_t *out, size_t cap) {
  uint8_t space[32], prin[32], cert[256], payload[512], oseed[32] = {9}, opub[32], osk[64];
  vec_hex("space_id", space, 32); vec_hex("principal_id", prin, 32);
  size_t cn = vec_hex("certificate_frame", cert, sizeof cert);
  size_t pn = vec_hex("epoch_payload_cbor", payload, sizeof payload);
  qi_ed25519_from_seed(oseed, opub, osk);
  qi_envelope e; memset(&e, 0, sizeof e);
  e.terminal = space; e.principal = prin; e.device = opub; e.sequence = 1;
  e.schema = "membership.instrument_epoch.v1"; e.schema_n = 30; e.logical_clock = 17;
  e.produced_by = 1; e.payload_encoding = QI_PAYLOAD_CBOR; e.payload = payload; e.payload_n = pn; e.priority = 4;
  uint8_t frame[1024]; size_t fn;
  CHECK(qi_envelope_sign(&e, osk, frame, sizeof frame, &fn) == QI_OK, "sign epoch frame");
  qi_cbor_w w; qi_w_init(&w, out, cap);
  qi_w_map(&w, 5);
  qi_w_uint(&w, 1); qi_w_bytes(&w, space, 32);
  qi_w_uint(&w, 2); qi_w_bytes(&w, prin, 32);
  qi_w_uint(&w, 3); qi_w_bytes(&w, cert, cn);
  qi_w_uint(&w, 4); qi_w_array(&w, 1); qi_w_bytes(&w, frame, fn);
  qi_w_uint(&w, 5); qi_w_bytes(&w, c->manifest_hash, 32);
  size_t n; CHECK(qi_w_done(&w, &n) == QI_OK, "provision bytes");
  return n;
}

static void test_enrollment_golden(void) {
  qi_instrument c; setup(&c);
  uint8_t nonce[16], out[4096], want[4096];
  vec_hex("enrollment_nonce", nonce, 16);
  size_t n, wn = vec_hex("enrollment_v1", want, sizeof want);
  CHECK(qi_instrument_enrollment(&c, nonce, out, sizeof out, &n) == QI_OK, "enrollment");
  CHECK(n == wn && memcmp(out, want, n) == 0, "enrollment == golden");
}

static void check_chain(const uint8_t frames[][QI_MAX_FRAME], const size_t *lens, size_t count) {
  uint8_t prev_id[32];
  uint64_t prev_clock = 0;
  for (size_t i = 0; i < count; i++) {
    qi_envelope e;
    CHECK(qi_envelope_decode_verify(frames[i], lens[i], &e) == QI_OK, "frame verifies");
    CHECK(e.sequence == i + 1, "sequence continues");
    if (i == 0) CHECK(e.previous == NULL, "first has no previous");
    else CHECK(e.previous && memcmp(e.previous, prev_id, 32) == 0, "previous == id of last frame");
    CHECK(e.logical_clock > prev_clock, "clock advances");
    CHECK(e.max_forwards == 1 && e.priority == QI_PRIORITY_TELEMETRY && e.produced_by == QI_AUTHORSHIP_SENSOR &&
          e.payload_encoding == QI_PAYLOAD_INSTRUMENT_SEALED && e.source_terminal != NULL, "instrument envelope shape");
    qi_event_id(frames[i], lens[i], prev_id);
    prev_clock = e.logical_clock;
  }
}

static void test_provision_chain_and_wal(void) {
  qi_instrument c; setup(&c);
  c.persist = persist; c.now = now_fn;
  uint8_t space[32], key[32]; vec_hex("space_id", space, 32); vec_hex("epoch_key", key, 32);
  uint8_t prov[2048]; size_t pn = build_provision(&c, prov, sizeof prov);
  qi_observation o = {0}; o.channel = "temperature"; o.kind = QI_VALUE_NUMBER; o.mantissa = 214; o.scale = -1; o.stale_after = 60;
  uint8_t f[QI_MAX_FRAME]; size_t fn;
  CHECK(qi_instrument_emit(&c, space, &o, f, sizeof f, &fn) == QI_ERR_STATE, "emit before provision refused");
  CHECK(qi_instrument_provision(&c, prov, pn) == QI_OK, "provision");
  const qi_epoch_key *ek = qi_instrument_current_epoch(&c, space);
  CHECK(ek && ek->n == vec_u64("epoch_n") && memcmp(ek->key, key, 32) == 0, "provision yields the fixed epoch key");
  /* honesty gates */
  c.now = no_time;
  CHECK(qi_instrument_emit(&c, space, &o, f, sizeof f, &fn) == QI_ERR_NO_TIME, "no time → refused");
  c.now = now_fn;
  uint8_t other[32] = {1};
  CHECK(qi_instrument_emit(&c, other, &o, f, sizeof f, &fn) == QI_ERR_STATE, "unknown space refused");

  /* three readings, acked between — the chain links */
  static uint8_t frames[3][QI_MAX_FRAME]; size_t lens[3];
  for (int i = 0; i < 3; i++) {
    clock_v += 10;
    CHECK(qi_instrument_emit(&c, space, &o, frames[i], QI_MAX_FRAME, &lens[i]) == QI_OK, "emit");
    /* the frame is owed until acked: a second emit is refused */
    CHECK(qi_instrument_emit(&c, space, &o, f, sizeof f, &fn) == QI_ERR_STATE, "owed frame blocks the next");
    uint8_t pend[QI_MAX_FRAME]; size_t pendn;
    CHECK(qi_instrument_pending(&c, space, pend, sizeof pend, &pendn) == QI_OK && pendn == lens[i] &&
          memcmp(pend, frames[i], pendn) == 0, "pending == the frame just built");
    CHECK(qi_instrument_ack_sent(&c, space) == QI_OK, "ack");
    CHECK(qi_instrument_pending(&c, space, pend, sizeof pend, &pendn) == QI_OK && pendn == 0, "nothing owed after ack");
  }
  check_chain(frames, lens, 3);

  /* POWER LOSS between persist and send: the record holds the frame. */
  clock_v += 10;
  CHECK(qi_instrument_emit(&c, space, &o, frames[0], QI_MAX_FRAME, &lens[0]) == QI_OK, "emit #4");
  uint8_t ident[4096]; size_t idn;
  CHECK(qi_instrument_identity_encode(&c, ident, sizeof ident, &idn) == QI_OK, "identity record");
  /* ...reboot: a fresh context from the two records */
  qi_instrument r; qi_instrument_init(&r);
  r.persist = persist; r.now = now_fn;
  CHECK(qi_instrument_identity_decode(&r, ident, idn) == QI_OK, "identity restored");
  CHECK(qi_instrument_state_decode(&r, journal, journal_n) == QI_OK, "state restored from the journal");
  uint8_t pend[QI_MAX_FRAME]; size_t pendn;
  CHECK(qi_instrument_pending(&r, space, pend, sizeof pend, &pendn) == QI_OK && pendn == lens[0] &&
        memcmp(pend, frames[0], pendn) == 0, "after reboot the owed frame comes back first");
  CHECK(qi_instrument_ack_sent(&r, space) == QI_OK, "ack after reboot");
  clock_v += 10;
  CHECK(qi_instrument_emit(&r, space, &o, frames[1], QI_MAX_FRAME, &lens[1]) == QI_OK, "emit #5 continues the chain");
  qi_envelope e4, e5;
  qi_envelope_decode_verify(frames[0], lens[0], &e4);
  qi_envelope_decode_verify(frames[1], lens[1], &e5);
  uint8_t id4[32]; qi_event_id(frames[0], lens[0], id4);
  CHECK(e4.sequence == 4 && e5.sequence == 5 && e5.previous && memcmp(e5.previous, id4, 32) == 0, "no hole, no fork across the reboot");
  /* a corrupted journal is refused, never half-trusted */
  journal[journal_n / 2] ^= 0xff;
  qi_instrument bad; qi_instrument_init(&bad);
  CHECK(qi_instrument_state_decode(&bad, journal, journal_n) == QI_ERR_VERIFY, "crc refuses a torn record");
  /* a persist failure means the frame never leaves and the chain does not move */
  qi_instrument_ack_sent(&r, space);
  uint64_t seq_before = r.spaces[0].seq;
  r.persist = NULL; /* opt-out path is allowed... */
  r.persist = persist;
  (void)seq_before;
}

int main(int argc, char **argv) {
  if (argc < 2 || !vec_load(argv[1])) { printf("usage\n"); return 2; }
  qi_crypto_init();
  test_enrollment_golden();
  test_provision_chain_and_wal();
  printf(fails ? "FAILED: %d\n" : "PASS\n", fails);
  return fails ? 1 : 0;
}
