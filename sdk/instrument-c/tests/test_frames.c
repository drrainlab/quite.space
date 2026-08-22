/* Golden frames: the C builders must reproduce the Go vectors byte for
 * byte — the message envelope of protocol_v0.json, and the instrument
 * manifest, observation and sealed envelope of instrument_v1.json. */
#include <stdio.h>
#include <string.h>
#include "qi/cbor.h"
#include "qi/crypto.h"
#include "qi/envelope.h"
#include "qi/manifest.h"
#include "qi/observation.h"
#include "vectors.h"

static int fails;
#define CHECK(cond, msg) do { if (!(cond)) { printf("FAIL %s:%d %s\n", __FILE__, __LINE__, msg); fails++; } } while (0)

static void dump(const char *what, const uint8_t *a, size_t n) {
  printf("  %s (%zu):", what, n);
  for (size_t i = 0; i < n && i < 96; i++) printf("%02x", a[i]);
  printf("\n");
}

static void same(const uint8_t *got, size_t gn, const uint8_t *want, size_t wn, const char *msg) {
  if (gn != wn || memcmp(got, want, gn) != 0) {
    printf("FAIL %s\n", msg); fails++;
    dump("got ", got, gn); dump("want", want, wn);
  }
}

static void test_message_envelope(const char *v0) {
  CHECK(vec_load(v0), "load protocol_v0.json");
  uint8_t dseed[32], tseed[32], dpub[32], dsk[64], tpub[32], tsk[64], prin[32] = {0x01};
  vec_hex("device_seed", dseed, 32); vec_hex("terminal_seed", tseed, 32);
  qi_ed25519_from_seed(dseed, dpub, dsk); qi_ed25519_from_seed(tseed, tpub, tsk);
  char text[128]; vec_str("payload_message_text", text, sizeof text);
  uint8_t payload[160]; qi_cbor_w w; qi_w_init(&w, payload, sizeof payload);
  qi_w_map(&w, 1); qi_w_uint(&w, 1); qi_w_text(&w, text, strlen(text));
  size_t pn; qi_w_done(&w, &pn);
  qi_envelope e = {0};
  e.terminal = tpub; e.principal = prin; e.device = dpub; e.sequence = 1;
  e.schema = "message.text.v1"; e.schema_n = 15; e.created_at = 1753142400; e.logical_clock = 1;
  e.produced_by = QI_AUTHORSHIP_HUMAN; e.payload_encoding = QI_PAYLOAD_CBOR;
  e.payload = payload; e.payload_n = pn; e.priority = QI_PRIORITY_MESSAGE;
  uint8_t frame[512], want[512];
  size_t fn;
  CHECK(qi_envelope_sign(&e, dsk, frame, sizeof frame, &fn) == QI_OK, "sign message envelope");
  size_t wn = vec_hex("envelope_frame", want, sizeof want);
  same(frame, fn, want, wn, "message envelope == golden");
  qi_envelope d;
  CHECK(qi_envelope_decode_verify(frame, fn, &d) == QI_OK, "decode+verify golden");
  CHECK(d.sequence == 1 && d.previous == NULL && d.created_at == 1753142400 && d.schema_n == 15, "decoded fields");
  frame[fn - 1] ^= 1;
  CHECK(qi_envelope_decode_verify(frame, fn, &d) == QI_ERR_VERIFY, "tampered signature refused");
  frame[fn - 1] ^= 1; frame[40] ^= 1;
  CHECK(qi_envelope_decode_verify(frame, fn, &d) == QI_ERR_VERIFY, "tampered body refused");
}

static void test_instrument_vectors(const char *v1) {
  CHECK(vec_load(v1), "load instrument_v1.json");
  uint8_t dseed[32], tseed[32], pseed[32], dpub[32], dsk[64], tpub[32], tsk[64], ppub[32], psk[64], space[32];
  vec_hex("device_seed", dseed, 32); vec_hex("terminal_seed", tseed, 32); vec_hex("principal_seed", pseed, 32);
  vec_hex("space_id", space, 32);
  qi_ed25519_from_seed(dseed, dpub, dsk); qi_ed25519_from_seed(tseed, tpub, tsk); qi_ed25519_from_seed(pseed, ppub, psk);
  uint8_t want[2048]; size_t wn;

  /* manifest: the reference greenhouse, verbatim */
  qi_channel_decl ch[] = {
    {"temperature", "number", "°C", "Температура"},
    {"humidity", "number", "%", "Влажность"},
    {"door", "boolean", NULL, "Дверь"},
    {"light", "number", "%", "Свет"},
  };
  uint8_t man[1536]; size_t mn;
  CHECK(qi_manifest_sign(tpub, ppub, QI_KIND_SENSOR, "Greenhouse", ch, 4, 1, NULL, tsk, man, sizeof man, &mn) == QI_OK, "sign manifest");
  wn = vec_hex("manifest_frame", want, sizeof want);
  same(man, mn, want, wn, "manifest == golden");
  char lab[128]; size_t ln;
  qi_channel_decl esc = {"temperature", "number", "%", "Температура: улица"};
  CHECK(qi_channel_label(&esc, lab, sizeof lab, &ln) == QI_OK &&
        !strncmp(lab, "qp.instr=temperature:number:%25:Температура%3A улица", ln), "escaping");
  qi_channel_decl bad = {"Temp", "number", NULL, NULL};
  CHECK(qi_channel_label(&bad, lab, sizeof lab, &ln) == QI_ERR_ARG, "bad channel refused");

  /* observation payload */
  qi_observation o = {0};
  o.channel = "temperature"; o.kind = QI_VALUE_NUMBER; o.mantissa = 214; o.scale = -1;
  o.observed_at = vec_u64("observed_at"); o.stale_after = 60;
  uint8_t obs[128]; size_t on;
  CHECK(qi_observation_encode(&o, obs, sizeof obs, &on) == QI_OK, "encode observation");
  wn = vec_hex("observation_value_payload", want, sizeof want);
  same(obs, on, want, wn, "observation == golden");
  /* fixed point edge cases */
  qi_observation neg = o; neg.mantissa = -125; neg.scale = -2;
  uint8_t nb[64]; size_t nn;
  CHECK(qi_observation_encode(&neg, nb, sizeof nb, &nn) == QI_OK && nb[0] == 0xa6, "negative: 6 pairs");
  qi_observation up = o; up.mantissa = 5; up.scale = 3;
  CHECK(qi_observation_encode(&up, nb, sizeof nb, &nn) == QI_OK, "scale>0 encodes");
  {
    qi_cbor_r r; qi_r_init(&r, nb, nn); qi_cbor_map m; uint64_t k, v = 0; bool more; const char *t; size_t tn;
    qi_map_begin(&r, &m); qi_map_next(&m, &k, &more); qi_r_text(&r, &t, &tn);
    qi_map_next(&m, &k, &more); qi_r_uint(&r, &v);
    CHECK(k == 2 && v == 5000 && nb[0] == 0xa4, "scale>0 → magnitude 5000, no decimals");
  }
  qi_observation ovf = o; ovf.mantissa = INT64_MAX; ovf.scale = 2;
  CHECK(qi_observation_encode(&ovf, nb, sizeof nb, &nn) == QI_ERR_LIMIT, "overflow refused");
  qi_observation nostale = o; nostale.stale_after = 0;
  CHECK(qi_observation_encode(&nostale, nb, sizeof nb, &nn) == QI_ERR_ARG, "stale_after mandatory");

  /* the sealed instrument envelope, sequence 7 */
  uint8_t sealed[256], prev[32], prin[32], term[32];
  size_t sn = vec_hex("sealed_observation_payload", sealed, sizeof sealed);
  vec_hex("previous_event_id", prev, 32); vec_hex("principal_id", prin, 32); vec_hex("terminal_id", term, 32);
  qi_envelope e = {0};
  e.terminal = space; e.principal = prin; e.device = dpub; e.sequence = vec_u64("sequence"); e.previous = prev;
  e.schema = "observation.value.v1"; e.schema_n = 20; e.created_at = vec_u64("observed_at");
  e.logical_clock = vec_u64("logical_clock"); e.produced_by = QI_AUTHORSHIP_SENSOR; e.source_terminal = term;
  e.payload_encoding = QI_PAYLOAD_INSTRUMENT_SEALED; e.payload = sealed; e.payload_n = sn;
  e.priority = QI_PRIORITY_TELEMETRY; e.expires_at = e.created_at + 60; e.max_forwards = 1;
  uint8_t frame[1024]; size_t fn;
  CHECK(qi_envelope_sign(&e, dsk, frame, sizeof frame, &fn) == QI_OK, "sign instrument envelope");
  wn = vec_hex("instrument_envelope_frame", want, sizeof want);
  same(frame, fn, want, wn, "instrument envelope == golden");
  qi_envelope d;
  CHECK(qi_envelope_decode_verify(frame, fn, &d) == QI_OK && d.max_forwards == 1 && d.source_terminal &&
        d.payload_encoding == QI_PAYLOAD_INSTRUMENT_SEALED, "decode instrument envelope");
}

int main(int argc, char **argv) {
  if (argc < 3) { printf("usage: test_frames instrument_v1.json protocol_v0.json\n"); return 2; }
  qi_crypto_init();
  test_message_envelope(argv[2]);
  test_instrument_vectors(argv[1]);
  printf(fails ? "FAILED: %d\n" : "PASS\n", fails);
  return fails ? 1 : 0;
}
