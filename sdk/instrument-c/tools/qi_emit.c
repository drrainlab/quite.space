/* qi-emit: the C side of the interop gate. From the fixed vector seeds it
 * becomes the vector's instrument, learns the vector's epoch (the
 * captured wrap), and emits the reference greenhouse's readings as
 * frames — hex, one per line — plus the values it intended. The Go gate
 * (terminals/instrument/interop_test.go) reduces them in a real space.
 * usage: qi-emit instrument_v1.json out_dir */
#include <stdio.h>
#include <string.h>
#include "qi/instrument.h"
#include "qi/crypto.h"
#include "../tests/vectors.h"

static uint64_t t0 = 1755800000;
static uint64_t now_fn(void *ud) { (void)ud; return t0; }

int main(int argc, char **argv) {
  if (argc < 3 || !vec_load(argv[1])) { fprintf(stderr, "usage: qi-emit instrument_v1.json out_dir\n"); return 2; }
  qi_crypto_init();
  uint8_t ds[32], xs[32], ts[32], prin[32], space[32], payload[512], cert[256];
  vec_hex("device_seed", ds, 32); vec_hex("device_x25519_scalar", xs, 32); vec_hex("terminal_seed", ts, 32);
  vec_hex("principal_id", prin, 32); vec_hex("space_id", space, 32);
  size_t pn = vec_hex("epoch_payload_cbor", payload, sizeof payload);
  size_t cn = vec_hex("certificate_frame", cert, sizeof cert);
  static const qi_channel_decl GREENHOUSE[] = {
    {"temperature", "number", "°C", "Температура"}, {"humidity", "number", "%", "Влажность"},
    {"door", "boolean", NULL, "Дверь"}, {"light", "number", "%", "Свет"},
  };
  qi_instrument c; qi_instrument_init(&c);
  c.now = now_fn;
  if (qi_instrument_set_keys(&c, ds, xs, ts) || qi_instrument_set_principal(&c, prin) ||
      qi_instrument_declare(&c, "Greenhouse", QI_KIND_SENSOR, GREENHOUSE, 4)) { fprintf(stderr, "setup\n"); return 1; }
  /* provisioned by hand: the tool has no authority to talk to */
  memcpy(c.cert, cert, cn); c.cert_n = cn; c.provisioned = true;
  qi_status s = qi_instrument_absorb_epoch_payload(&c, space, payload, pn, 1);
  if (s) { fprintf(stderr, "epoch: %s\n", qi_status_str(s)); return 1; }

  char path[512];
  snprintf(path, sizeof path, "%s/greenhouse.frames", argv[2]);
  FILE *ff = fopen(path, "w");
  snprintf(path, sizeof path, "%s/greenhouse.expect", argv[2]);
  FILE *fe = fopen(path, "w");
  if (!ff || !fe) { fprintf(stderr, "cannot write %s\n", argv[2]); return 1; }
  fprintf(ff, "# C-produced frames from the vector seeds (qi-emit); Go reduces them.\n");
  fprintf(fe, "# channel=value the C side intended\n");
  struct { qi_observation o; const char *expect; } readings[] = {
    {{"temperature", QI_VALUE_NUMBER, 214, -1, false, NULL, 0, 60, false}, "temperature=21.4"},
    {{"humidity", QI_VALUE_NUMBER, 480, -1, false, NULL, 0, 60, false}, "humidity=48.0"},
    {{"door", QI_VALUE_BOOL, 0, 0, true, NULL, 0, 120, false}, "door=true"},
    {{"light", QI_VALUE_NUMBER, 37, 0, false, NULL, 0, 60, false}, "light=37"},
    {{"temperature", QI_VALUE_NUMBER, -25, -1, false, NULL, 0, 60, false}, "temperature=-2.5"},
  };
  /* the last temperature wins: the gate checks LWW on the same channel */
  for (size_t i = 0; i < sizeof readings / sizeof readings[0]; i++) {
    uint8_t f[QI_MAX_FRAME]; size_t fn;
    t0 += 5;
    s = qi_instrument_emit(&c, space, &readings[i].o, f, sizeof f, &fn);
    if (s) { fprintf(stderr, "emit %zu: %s\n", i, qi_status_str(s)); return 1; }
    qi_instrument_ack_sent(&c, space);
    for (size_t k = 0; k < fn; k++) fprintf(ff, "%02x", f[k]);
    fprintf(ff, "\n");
  }
  fprintf(fe, "temperature=-2.5\nhumidity=48.0\ndoor=true\nlight=37\n");
  fclose(ff); fclose(fe);
  printf("wrote %s/greenhouse.{frames,expect}\n", argv[2]);
  return 0;
}
