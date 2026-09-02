#include "qi/envelope.h"
#include "qi/cbor.h"
#include "qi/crypto.h"
#include "qi/config.h"
#include <string.h>

static size_t pair_count(const qi_envelope *e) {
  size_t c = 0;
  if (e->terminal) c++;        /* 2 */
  if (e->principal) c++;       /* 3 */
  if (e->device) c++;          /* 4 */
  c++;                         /* 5 sequence */
  if (e->previous) c++;        /* 6 */
  c++;                         /* 7 schema */
  if (e->created_at) c++;      /* 8 */
  c++;                         /* 9 clock */
  if (e->produced_by) c++;     /* 10 */
  if (e->human_approved) c++;  /* 11 */
  if (e->source_terminal) c++; /* 12 */
  c++;                         /* 13 encoding */
  c++;                         /* 14 payload */
  c++;                         /* 15 priority */
  if (e->expires_at) c++;      /* 16 */
  if (e->max_forwards) c++;    /* 17 */
  return c;
}

static void write_body(qi_cbor_w *w, const qi_envelope *e, size_t count) {
  qi_w_map(w, count);
  qi_w_uint(w, 2); qi_w_bytes(w, e->terminal, 32);
  qi_w_uint(w, 3); qi_w_bytes(w, e->principal, 32);
  qi_w_uint(w, 4); qi_w_bytes(w, e->device, 32);
  qi_w_uint(w, 5); qi_w_uint(w, e->sequence);
  if (e->previous) { qi_w_uint(w, 6); qi_w_bytes(w, e->previous, 32); }
  qi_w_uint(w, 7); qi_w_text(w, e->schema, e->schema_n);
  if (e->created_at) { qi_w_uint(w, 8); qi_w_uint(w, e->created_at); }
  qi_w_uint(w, 9); qi_w_uint(w, e->logical_clock);
  if (e->produced_by) { qi_w_uint(w, 10); qi_w_uint(w, e->produced_by); }
  if (e->human_approved) { qi_w_uint(w, 11); qi_w_bool(w, true); }
  if (e->source_terminal) { qi_w_uint(w, 12); qi_w_bytes(w, e->source_terminal, 32); }
  qi_w_uint(w, 13); qi_w_uint(w, e->payload_encoding);
  qi_w_uint(w, 14); qi_w_bytes(w, e->payload, e->payload_n);
  qi_w_uint(w, 15); qi_w_uint(w, e->priority);
  if (e->expires_at) { qi_w_uint(w, 16); qi_w_uint(w, e->expires_at); }
  if (e->max_forwards) { qi_w_uint(w, 17); qi_w_uint(w, e->max_forwards); }
}

qi_status qi_envelope_sign(const qi_envelope *e, const uint8_t device_sk[64],
                           uint8_t *out, size_t cap, size_t *n) {
  if (!e || !e->terminal || !e->principal || !e->device || !e->schema || !e->payload ||
      !e->sequence || !e->priority || !e->payload_encoding)
    return QI_ERR_ARG;
  if ((e->sequence == 1) != (e->previous == NULL)) return QI_ERR_ARG;
  /* the device key must be the one the envelope names */
  uint8_t pub[32];
  memcpy(pub, device_sk + 32, 32);
  if (memcmp(pub, e->device, 32) != 0) return QI_ERR_ARG;

  size_t count = pair_count(e);
  qi_cbor_w w;
  qi_w_init(&w, out, cap);
  write_body(&w, e, count);
  size_t body_n;
  qi_status s = qi_w_done(&w, &body_n);
  if (s) return s;
  uint8_t sig[64];
  qi_ed25519_sign(device_sk, out, body_n, sig);
  /* Re-emit the head with count+1 (heads are shortest form: the same
   * byte width for counts < 24, which every protocol envelope has), then
   * append the signature pair. */
  uint8_t head_old[9], head_new[9];
  qi_cbor_w hw; qi_w_init(&hw, head_old, sizeof head_old); qi_w_map(&hw, count);
  size_t ho; qi_w_done(&hw, &ho);
  qi_w_init(&hw, head_new, sizeof head_new); qi_w_map(&hw, count + 1);
  size_t hn; qi_w_done(&hw, &hn);
  if (hn != ho) {
    /* count crossed a head-width threshold: shift the body */
    if (body_n - ho + hn > cap) return QI_ERR_SPACE;
    memmove(out + hn, out + ho, body_n - ho);
  }
  memcpy(out, head_new, hn);
  size_t len = body_n - ho + hn;
  qi_w_init(&w, out + len, cap - len);
  qi_w_uint(&w, 18); qi_w_bytes(&w, sig, 64);
  size_t tail;
  s = qi_w_done(&w, &tail);
  if (s) return s;
  *n = len + tail;
  return QI_OK;
}

static qi_status take32(qi_cbor_r *r, const uint8_t **dst) {
  const uint8_t *b; size_t n;
  qi_status s = qi_r_bytes(r, &b, &n);
  if (s) return s;
  if (n != 32) return QI_ERR_CBOR;
  *dst = b;
  return QI_OK;
}

qi_status qi_envelope_decode_verify(const uint8_t *frame, size_t n, qi_envelope *e) {
  if (!frame || !e || n > QI_MAX_FRAME * 4) return QI_ERR_ARG;
  memset(e, 0, sizeof *e);
  qi_cbor_r r; qi_r_init(&r, frame, n);
  qi_cbor_map m;
  qi_status s = qi_map_begin(&r, &m);
  if (s) return s;
  size_t head_n = r.pos;
  size_t sig_entry_start = 0;
  bool saw_seq = false, saw_clock = false, saw_prio = false, saw_enc = false;
  for (;;) {
    size_t entry_start = r.pos;
    uint64_t k; bool more;
    s = qi_map_next(&m, &k, &more);
    if (s) return s;
    if (!more) break;
    uint64_t u; bool bv; const uint8_t *b; size_t bn; const char *t;
    switch (k) {
    case 1: s = qi_r_uint(&r, &u); break;
    case 2: s = take32(&r, &e->terminal); break;
    case 3: s = take32(&r, &e->principal); break;
    case 4: s = take32(&r, &e->device); break;
    case 5: s = qi_r_uint(&r, &e->sequence); saw_seq = true; break;
    case 6: s = take32(&r, &e->previous); break;
    case 7: s = qi_r_text(&r, &t, &bn); e->schema = t; e->schema_n = bn; break;
    case 8: s = qi_r_uint(&r, &e->created_at); break;
    case 9: s = qi_r_uint(&r, &e->logical_clock); saw_clock = true; break;
    case 10: s = qi_r_uint(&r, &u); e->produced_by = u > 6 ? 0 : (uint8_t)u; break;
    case 11: s = qi_r_bool(&r, &bv); e->human_approved = bv; break;
    case 12: s = take32(&r, &e->source_terminal); break;
    case 13: s = qi_r_uint(&r, &u); e->payload_encoding = (uint8_t)u; saw_enc = true; break;
    case 14: s = qi_r_bytes(&r, &b, &bn); e->payload = b; e->payload_n = bn; break;
    case 15: s = qi_r_uint(&r, &u); e->priority = (uint8_t)u; saw_prio = true; break;
    case 16: s = qi_r_uint(&r, &e->expires_at); break;
    case 17: s = qi_r_uint(&r, &e->max_forwards); break;
    case 18:
      sig_entry_start = entry_start;
      s = qi_r_bytes(&r, &b, &bn); e->signature = b; e->signature_n = bn; break;
    default: s = qi_r_skip(&r); break;
    }
    if (s) return s;
  }
  s = qi_r_done(&r);
  if (s) return s;
  if (!e->terminal || !e->principal || !e->device || !saw_seq || !saw_clock || !e->schema ||
      !saw_enc || !e->payload || !saw_prio || !e->signature || e->signature_n != 64)
    return QI_ERR_CBOR;
  if (e->sequence == 0 || (e->sequence == 1) != (e->previous == NULL)) return QI_ERR_CBOR;
  if (!sig_entry_start) return QI_ERR_CBOR;
  /* Signing bytes = head(count-1) ‖ everything between the head and the
   * signature entry. Unknown fields are kept verbatim, as the Go verifier
   * does. Assembled in a bounded scratch buffer. */
  uint8_t head_new[9];
  qi_cbor_w hw; qi_w_init(&hw, head_new, sizeof head_new); qi_w_map(&hw, m.n - 1);
  size_t hn; qi_w_done(&hw, &hn);
  size_t mid = sig_entry_start - head_n;
  if (hn + mid > QI_MAX_FRAME * 4) return QI_ERR_LIMIT;
  /* STATIC: 8KB dwarfs an MCU loop task's whole stack (the Heltec V3
     overflowed right here on its first intact epoch frame). The core is
     single-threaded by contract; one static scratch, visible to the
     linker, is the honest footprint. */
  static uint8_t scratch[QI_MAX_FRAME * 4];
  memcpy(scratch, head_new, hn);
  memcpy(scratch + hn, frame + head_n, mid);
  if (!qi_ed25519_verify(e->device, scratch, hn + mid, e->signature)) return QI_ERR_VERIFY;
  return QI_OK;
}
