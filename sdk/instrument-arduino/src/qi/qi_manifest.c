#include "qi/manifest.h"
#include "qi/cbor.h"
#include "qi/crypto.h"
#include "qi/config.h"
#include <string.h>

static int channel_ok(const char *c) {
  size_t n = strlen(c);
  if (n == 0 || n > 32) return 0;
  if (c[0] < 'a' || c[0] > 'z') return 0;
  for (size_t i = 1; i < n; i++) {
    char x = c[i];
    if (!((x >= 'a' && x <= 'z') || (x >= '0' && x <= '9') || x == '_' || x == '-')) return 0;
  }
  return 1;
}

static int kind_ok(const char *k) {
  return !strcmp(k, "number") || !strcmp(k, "boolean") || !strcmp(k, "enum");
}

static qi_status put_escaped(const char *s, char *out, size_t cap, size_t *pos) {
  for (; *s; s++) {
    const char *rep = NULL;
    if (*s == '%') rep = "%25"; else if (*s == ':') rep = "%3A";
    size_t need = rep ? 3 : 1;
    if (*pos + need > cap) return QI_ERR_SPACE;
    if (rep) { memcpy(out + *pos, rep, 3); *pos += 3; } else out[(*pos)++] = *s;
  }
  return QI_OK;
}

static qi_status put_raw(const char *s, char *out, size_t cap, size_t *pos) {
  size_t n = strlen(s);
  if (*pos + n > cap) return QI_ERR_SPACE;
  memcpy(out + *pos, s, n); *pos += n;
  return QI_OK;
}

qi_status qi_channel_label(const qi_channel_decl *d, char *out, size_t cap, size_t *n) {
  if (!d || !d->channel || !d->kind || !channel_ok(d->channel) || !kind_ok(d->kind)) return QI_ERR_ARG;
  size_t pos = 0;
  qi_status s;
  if ((s = put_raw("qp.instr=", out, cap, &pos))) return s;
  if ((s = put_raw(d->channel, out, cap, &pos))) return s;
  if ((s = put_raw(":", out, cap, &pos))) return s;
  if ((s = put_raw(d->kind, out, cap, &pos))) return s;
  const char *unit = d->unit ? d->unit : "", *label = d->label ? d->label : "";
  if (*unit || *label) {
    if ((s = put_raw(":", out, cap, &pos))) return s;
    if ((s = put_escaped(unit, out, cap, &pos))) return s;
  }
  if (*label) {
    if ((s = put_raw(":", out, cap, &pos))) return s;
    if ((s = put_escaped(label, out, cap, &pos))) return s;
  }
  *n = pos;
  return QI_OK;
}

static void text_array(qi_cbor_w *w, const char *const *items, size_t n) {
  qi_w_array(w, n);
  for (size_t i = 0; i < n; i++) qi_w_text(w, items[i], strlen(items[i]));
}

qi_status qi_manifest_sign(const uint8_t terminal[32], const uint8_t controller[32],
                           uint8_t kind, const char *label,
                           const qi_channel_decl *ch, size_t nch,
                           uint64_t revision, const uint8_t *previous_hash,
                           const uint8_t terminal_sk[64],
                           uint8_t *out, size_t cap, size_t *n) {
  if (!terminal || !controller || !label || !out || !n || !terminal_sk) return QI_ERR_ARG;
  if (kind != QI_KIND_SENSOR && kind != QI_KIND_ACTUATOR) return QI_ERR_ARG;
  if (nch > QI_MAX_CHANNELS) return QI_ERR_LIMIT;
  if (revision == 0 || (revision == 1) != (previous_hash == NULL)) return QI_ERR_ARG;
  if (memcmp(terminal_sk + 32, terminal, 32) != 0) return QI_ERR_ARG;
  /* duplicate channels are refused, as InstrumentLabels does */
  for (size_t i = 0; i < nch; i++)
    for (size_t j = 0; j < i; j++)
      if (!strcmp(ch[i].channel, ch[j].channel)) return QI_ERR_ARG;

  static const char *caps[] = {"signal.publish"};
  static const char *pubs[] = {"observation.value.v1"};
  /* keys: 2 terminal 3 controller 4 kind 5 labels 6 io_mode 7 caps 8 publishes
   *       12 agency 15 retention 16 announce 18 revision [19 previous] */
  size_t count = 11 + (previous_hash ? 1 : 0);
  qi_cbor_w w; qi_w_init(&w, out, cap);
  qi_w_map(&w, count);
  qi_w_uint(&w, 2); qi_w_bytes(&w, terminal, 32);
  qi_w_uint(&w, 3); qi_w_bytes(&w, controller, 32);
  qi_w_uint(&w, 4); qi_w_uint(&w, kind);
  qi_w_uint(&w, 5); qi_w_array(&w, 1 + nch);
  qi_w_text(&w, label, strlen(label));
  for (size_t i = 0; i < nch; i++) {
    char lab[QI_MAX_STR * 3];
    size_t ln;
    qi_status s = qi_channel_label(&ch[i], lab, sizeof lab, &ln);
    if (s) return s;
    qi_w_text(&w, lab, ln);
  }
  qi_w_uint(&w, 6); qi_w_uint(&w, 1);
  qi_w_uint(&w, 7); text_array(&w, caps, 1);
  qi_w_uint(&w, 8); text_array(&w, pubs, 1);
  qi_w_uint(&w, 12); qi_w_uint(&w, 2);
  qi_w_uint(&w, 15); qi_w_uint(&w, 3600);
  qi_w_uint(&w, 16); qi_w_uint(&w, 300);
  qi_w_uint(&w, 18); qi_w_uint(&w, revision);
  if (previous_hash) { qi_w_uint(&w, 19); qi_w_bytes(&w, previous_hash, 32); }
  size_t body_n;
  qi_status s = qi_w_done(&w, &body_n);
  if (s) return s;
  uint8_t sig[64];
  qi_ed25519_sign(terminal_sk, out, body_n, sig);
  /* counts here are < 24: one-byte head either way */
  out[0] = (uint8_t)(0xa0 | (count + 1));
  qi_w_init(&w, out + body_n, cap - body_n);
  qi_w_uint(&w, 20); qi_w_bytes(&w, sig, 64);
  size_t tail;
  if ((s = qi_w_done(&w, &tail))) return s;
  *n = body_n + tail;
  return QI_OK;
}
