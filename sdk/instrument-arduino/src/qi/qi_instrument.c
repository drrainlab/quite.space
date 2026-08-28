#include "qi/instrument.h"
#include "qi/cbor.h"
#include "qi/crypto.h"
#include "qi/envelope.h"
#include "qi/ids.h"
#include <string.h>

static const char SCHEMA_OBS[] = "observation.value.v1";
static const char SCHEMA_EPOCH[] = "membership.instrument_epoch.v1";

/* ---- crc32 (IEEE, reflected), table-free ---- */
uint32_t qi_crc32(const uint8_t *b, size_t n) {
  uint32_t c = 0xffffffffu;
  for (size_t i = 0; i < n; i++) {
    c ^= b[i];
    for (int k = 0; k < 8; k++) c = (c >> 1) ^ (0xedb88320u & (0u - (c & 1u)));
  }
  return ~c;
}

void qi_instrument_init(qi_instrument *c) { memset(c, 0, sizeof *c); }

qi_status qi_instrument_set_keys(qi_instrument *c, const uint8_t device_seed[32],
                                 const uint8_t x25519_scalar[32], const uint8_t terminal_seed[32]) {
  memcpy(c->device_seed, device_seed, 32);
  memcpy(c->x25519, x25519_scalar, 32);
  memcpy(c->terminal_seed, terminal_seed, 32);
  qi_ed25519_from_seed(c->device_seed, c->device_pub, c->device_sk);
  qi_ed25519_from_seed(c->terminal_seed, c->terminal_pub, c->terminal_sk);
  qi_status s = qi_x25519_pub(c->x25519, c->x25519_pub);
  if (s) return s;
  c->have_keys = true;
  return QI_OK;
}

qi_status qi_instrument_keygen(qi_instrument *c) {
  uint8_t d[32], x[32], t[32];
  qi_random(d, 32); qi_random(x, 32); qi_random(t, 32);
  return qi_instrument_set_keys(c, d, x, t);
}

static qi_status copy_str(char *dst, size_t cap, const char *src) {
  size_t n = src ? strlen(src) : 0;
  if (n >= cap) return QI_ERR_LIMIT;
  memcpy(dst, src ? src : "", n);
  dst[n] = 0;
  return QI_OK;
}

static void decls_of(const qi_instrument *c, qi_channel_decl *out) {
  for (size_t i = 0; i < c->nchannels; i++) {
    out[i].channel = c->channels[i].channel;
    out[i].kind = c->channels[i].kind;
    out[i].unit = c->channels[i].unit;
    out[i].label = c->channels[i].label;
  }
}

qi_status qi_instrument_set_principal(qi_instrument *c, const uint8_t principal[32]) {
  if (c->provisioned && memcmp(c->principal, principal, 32) != 0) return QI_ERR_STATE;
  memcpy(c->principal, principal, 32);
  c->have_principal = true;
  return QI_OK;
}

qi_status qi_instrument_declare(qi_instrument *c, const char *label, uint8_t kind,
                                const qi_channel_decl *ch, size_t nch) {
  if (!c->have_keys) return QI_ERR_STATE;
  if (!label || !*label || nch > QI_MAX_CHANNELS) return QI_ERR_ARG;
  qi_status s = copy_str(c->label, sizeof c->label, label);
  if (s) return s;
  for (size_t i = 0; i < nch; i++) {
    if ((s = copy_str(c->channels[i].channel, sizeof c->channels[i].channel, ch[i].channel))) return s;
    if ((s = copy_str(c->channels[i].kind, sizeof c->channels[i].kind, ch[i].kind))) return s;
    if ((s = copy_str(c->channels[i].unit, sizeof c->channels[i].unit, ch[i].unit))) return s;
    if ((s = copy_str(c->channels[i].label, sizeof c->channels[i].label, ch[i].label))) return s;
  }
  c->nchannels = nch;
  c->kind = kind;
  if (!c->have_principal) return QI_ERR_STATE; /* the manifest names its controller */
  qi_channel_decl decls[QI_MAX_CHANNELS];
  decls_of(c, decls);
  s = qi_manifest_sign(c->terminal_pub, c->principal, kind, c->label, decls, nch, 1, NULL,
                       c->terminal_sk, c->manifest, sizeof c->manifest, &c->manifest_n);
  if (s) return s;
  qi_sha256(c->manifest, c->manifest_n, c->manifest_hash);
  c->declared = true;
  return QI_OK;
}

/* ---- enrollment.v1: {1 version 2 device_pub 3 x25519_pub 4 terminal_pub
 *      5 manifest_hash 6 label 7 nonce 8 manifest 9 device_sig 10 terminal_sig} */
static void enrollment_body(const qi_instrument *c, const uint8_t nonce[16], qi_cbor_w *w, size_t count) {
  qi_w_map(w, count);
  qi_w_uint(w, 1); qi_w_uint(w, 1);
  qi_w_uint(w, 2); qi_w_bytes(w, c->device_pub, 32);
  qi_w_uint(w, 3); qi_w_bytes(w, c->x25519_pub, 32);
  qi_w_uint(w, 4); qi_w_bytes(w, c->terminal_pub, 32);
  qi_w_uint(w, 5); qi_w_bytes(w, c->manifest_hash, 32);
  qi_w_uint(w, 6); qi_w_text(w, c->label, strlen(c->label));
  qi_w_uint(w, 7); qi_w_bytes(w, nonce, 16);
  qi_w_uint(w, 8); qi_w_bytes(w, c->manifest, c->manifest_n);
}

qi_status qi_instrument_enrollment(const qi_instrument *c, const uint8_t nonce[16],
                                   uint8_t *out, size_t cap, size_t *n) {
  if (!c->have_keys || !c->declared) return QI_ERR_STATE;
  qi_cbor_w w; qi_w_init(&w, out, cap);
  enrollment_body(c, nonce, &w, 8);
  size_t body_n;
  qi_status s = qi_w_done(&w, &body_n);
  if (s) return s;
  uint8_t dsig[64], tsig[64];
  qi_ed25519_sign(c->device_sk, out, body_n, dsig);
  qi_ed25519_sign(c->terminal_sk, out, body_n, tsig);
  out[0] = 0xa0 | 10;
  qi_w_init(&w, out + body_n, cap - body_n);
  qi_w_uint(&w, 9); qi_w_bytes(&w, dsig, 64);
  qi_w_uint(&w, 10); qi_w_bytes(&w, tsig, 64);
  size_t tail;
  if ((s = qi_w_done(&w, &tail))) return s;
  *n = body_n + tail;
  return QI_OK;
}

/* ---- spaces ---- */
static qi_space_state *find_space(qi_instrument *c, const uint8_t space[32]) {
  for (size_t i = 0; i < c->nspaces; i++)
    if (memcmp(c->spaces[i].space, space, 32) == 0) return &c->spaces[i];
  return NULL;
}

static qi_space_state *ensure_space(qi_instrument *c, const uint8_t space[32]) {
  qi_space_state *s = find_space(c, space);
  if (s) return s;
  if (c->nspaces >= QI_MAX_SPACES) return NULL;
  s = &c->spaces[c->nspaces++];
  memset(s, 0, sizeof *s);
  memcpy(s->space, space, 32);
  return s;
}

static qi_epoch_key *epoch_slot(qi_space_state *s, uint64_t n) {
  for (size_t i = 0; i < s->nepochs; i++) if (s->epochs[i].n == n) return &s->epochs[i];
  if (s->nepochs < QI_MAX_EPOCHS_HELD) return &s->epochs[s->nepochs++];
  /* evict the lowest: late opens of ancient epochs are not an instrument's job */
  qi_epoch_key *low = &s->epochs[0];
  for (size_t i = 1; i < s->nepochs; i++) if (s->epochs[i].n < low->n) low = &s->epochs[i];
  return low;
}

static void epoch_info(const uint8_t space[32], uint64_t n, uint8_t *info, size_t *in) {
  uint8_t plane[32];
  qi_instrument_plane_id(space, plane);
  memcpy(info, "quiet-places-epoch-v0:", 22);
  memcpy(info + 22, plane, 32);
  qi_cbor_w w; qi_w_init(&w, info + 54, 9); qi_w_uint(&w, n);
  size_t wn; qi_w_done(&w, &wn);
  *in = 54 + wn;
}

qi_status qi_instrument_absorb_epoch_payload(qi_instrument *c, const uint8_t space[32],
                                             const uint8_t *payload, size_t n, uint64_t clock) {
  qi_space_state *st = ensure_space(c, space);
  if (!st) return QI_ERR_LIMIT;
  qi_cbor_r r; qi_r_init(&r, payload, n);
  qi_cbor_map m;
  qi_status s = qi_map_begin(&r, &m);
  if (s) return s;
  uint64_t epoch_n = 0;
  const uint8_t *my_enc = NULL, *my_ct = NULL; size_t my_ct_n = 0;
  for (;;) {
    uint64_t k; bool more;
    if ((s = qi_map_next(&m, &k, &more))) return s;
    if (!more) break;
    if (k == 1) { if ((s = qi_r_uint(&r, &epoch_n))) return s; }
    else if (k == 2) {
      size_t cnt;
      if ((s = qi_r_array(&r, &cnt))) return s;
      if (cnt > QI_MAX_EPOCH_RECIPIENTS) return QI_ERR_LIMIT;
      for (size_t i = 0; i < cnt; i++) {
        size_t three;
        if ((s = qi_r_array(&r, &three))) return s;
        if (three != 3) return QI_ERR_CBOR;
        const uint8_t *dev, *enc, *ct; size_t dn, en, cn;
        if ((s = qi_r_bytes(&r, &dev, &dn))) return s;
        if ((s = qi_r_bytes(&r, &enc, &en))) return s;
        if ((s = qi_r_bytes(&r, &ct, &cn))) return s;
        if (dn != 32) return QI_ERR_CBOR;
        if (memcmp(dev, c->device_pub, 32) == 0 && en == 32) { my_enc = enc; my_ct = ct; my_ct_n = cn; }
      }
    } else if ((s = qi_r_skip(&r))) return s;
  }
  if ((s = qi_r_done(&r))) return s;
  if (epoch_n == 0) return QI_ERR_CBOR;
  if (clock > st->clock) st->clock = clock;
  if (epoch_n > st->current) st->current = epoch_n;
  if (!my_enc) return QI_ERR_NOT_ADDRESSED; /* normal after detachment */
  uint8_t info[64]; size_t in;
  epoch_info(space, epoch_n, info, &in);
  uint8_t key[48]; size_t kn;
  s = qi_hpke_open(c->x25519, c->x25519_pub, my_enc, my_ct, my_ct_n, info, in, key, sizeof key, &kn);
  if (s) return s;
  if (kn != 32) return QI_ERR_CRYPTO;
  qi_epoch_key *slot = epoch_slot(st, epoch_n);
  slot->n = epoch_n;
  memcpy(slot->key, key, 32);
  return QI_OK;
}

qi_status qi_instrument_absorb_epoch_frame(qi_instrument *c, const uint8_t *frame, size_t n) {
  if (!c->provisioned) return QI_ERR_STATE;
  qi_envelope e;
  qi_status s = qi_envelope_decode_verify(frame, n, &e);
  if (s) return s;
  if (e.schema_n != sizeof SCHEMA_EPOCH - 1 || memcmp(e.schema, SCHEMA_EPOCH, e.schema_n) != 0) return QI_ERR_ARG;
  if (memcmp(e.principal, c->principal, 32) != 0) return QI_ERR_VERIFY;
  if (!find_space(c, e.terminal)) return QI_ERR_STATE; /* only provisioned spaces */
  if (e.payload_encoding != QI_PAYLOAD_CBOR) return QI_ERR_ARG;
  return qi_instrument_absorb_epoch_payload(c, e.terminal, e.payload, e.payload_n, e.logical_clock);
}

/* ---- provision.v1: {1 space 2 principal 3 cert 4 [epoch frames] 5 manifest_ack} */
qi_status qi_instrument_provision(qi_instrument *c, const uint8_t *bytes, size_t n) {
  if (!c->have_keys || !c->declared) return QI_ERR_STATE;
  qi_cbor_r r; qi_r_init(&r, bytes, n);
  qi_cbor_map m;
  qi_status s = qi_map_begin(&r, &m);
  if (s) return s;
  const uint8_t *space = NULL, *prin = NULL, *cert = NULL, *ack = NULL;
  size_t cert_n = 0;
  const uint8_t *frames[QI_MAX_EPOCHS_HELD]; size_t frame_n[QI_MAX_EPOCHS_HELD]; size_t nframes = 0;
  for (;;) {
    uint64_t k; bool more;
    if ((s = qi_map_next(&m, &k, &more))) return s;
    if (!more) break;
    const uint8_t *b; size_t bn;
    switch (k) {
    case 1: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; space = b; break;
    case 2: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; prin = b; break;
    case 3: if ((s = qi_r_bytes(&r, &b, &bn))) return s; cert = b; cert_n = bn; break;
    case 4: {
      size_t cnt;
      if ((s = qi_r_array(&r, &cnt))) return s;
      for (size_t i = 0; i < cnt; i++) {
        if ((s = qi_r_bytes(&r, &b, &bn))) return s;
        if (nframes < QI_MAX_EPOCHS_HELD) { frames[nframes] = b; frame_n[nframes] = bn; nframes++; }
      }
      break;
    }
    case 5: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; ack = b; break;
    default: if ((s = qi_r_skip(&r))) return s;
    }
  }
  if ((s = qi_r_done(&r))) return s;
  if (!space || !prin || !cert || cert_n == 0 || cert_n > sizeof c->cert || nframes == 0) return QI_ERR_CBOR;
  if (ack && memcmp(ack, c->manifest_hash, 32) != 0) return QI_ERR_VERIFY; /* not the manifest we sent */
  if (memcmp(c->principal, prin, 32) != 0) return QI_ERR_VERIFY; /* not the principal the manifest names */
  memcpy(c->cert, cert, cert_n); c->cert_n = cert_n;
  c->provisioned = true;
  if (!ensure_space(c, space)) return QI_ERR_LIMIT;
  qi_status last = QI_OK;
  for (size_t i = 0; i < nframes; i++) {
    qi_status fs = qi_instrument_absorb_epoch_frame(c, frames[i], frame_n[i]);
    if (fs != QI_OK && fs != QI_ERR_NOT_ADDRESSED) last = fs;
  }
  if (last) return last;
  if (!qi_instrument_current_epoch(c, space)) return QI_ERR_NO_EPOCH;
  return QI_OK;
}

const qi_epoch_key *qi_instrument_current_epoch(const qi_instrument *c, const uint8_t space[32]) {
  for (size_t i = 0; i < c->nspaces; i++) {
    const qi_space_state *s = &c->spaces[i];
    if (memcmp(s->space, space, 32) != 0) continue;
    for (size_t k = 0; k < s->nepochs; k++) if (s->epochs[k].n == s->current && s->current) return &s->epochs[k];
    return NULL;
  }
  return NULL;
}

/* ---- state record ---- */
static qi_status persist_now(qi_instrument *c) {
  if (!c->persist) return QI_OK; /* tests/tools without durability opt out explicitly */
  uint8_t rec[QI_MAX_SPACES * (QI_MAX_FRAME + 512) + 64];
  size_t n;
  qi_status s = qi_instrument_state_encode(c, rec, sizeof rec, &n);
  if (s) return s;
  return c->persist(c->persist_ud, rec, n);
}

qi_status qi_instrument_state_encode(const qi_instrument *c, uint8_t *out, size_t cap, size_t *n) {
  if (cap < 4) return QI_ERR_SPACE;
  qi_cbor_w w; qi_w_init(&w, out, cap - 4);
  qi_w_map(&w, 2);
  qi_w_uint(&w, 1); qi_w_uint(&w, c->generation);
  qi_w_uint(&w, 2); qi_w_array(&w, c->nspaces);
  for (size_t i = 0; i < c->nspaces; i++) {
    const qi_space_state *s = &c->spaces[i];
    qi_w_array(&w, 7);
    qi_w_bytes(&w, s->space, 32);
    qi_w_uint(&w, s->seq);
    qi_w_bytes(&w, s->tip, 32);
    qi_w_uint(&w, s->clock);
    qi_w_uint(&w, s->current);
    qi_w_array(&w, s->nepochs);
    for (size_t k = 0; k < s->nepochs; k++) { qi_w_array(&w, 2); qi_w_uint(&w, s->epochs[k].n); qi_w_bytes(&w, s->epochs[k].key, 32); }
    qi_w_bytes(&w, s->pending, s->pending_n);
  }
  size_t body;
  qi_status st = qi_w_done(&w, &body);
  if (st) return st;
  uint32_t crc = qi_crc32(out, body);
  out[body] = (uint8_t)crc; out[body + 1] = (uint8_t)(crc >> 8); out[body + 2] = (uint8_t)(crc >> 16); out[body + 3] = (uint8_t)(crc >> 24);
  *n = body + 4;
  return QI_OK;
}

static qi_status crc_check(const uint8_t *rec, size_t n, size_t *body) {
  if (n < 5) return QI_ERR_CBOR;
  uint32_t want = (uint32_t)rec[n - 4] | (uint32_t)rec[n - 3] << 8 | (uint32_t)rec[n - 2] << 16 | (uint32_t)rec[n - 1] << 24;
  if (qi_crc32(rec, n - 4) != want) return QI_ERR_VERIFY;
  *body = n - 4;
  return QI_OK;
}

qi_status qi_instrument_state_decode(qi_instrument *c, const uint8_t *rec, size_t n) {
  size_t body;
  qi_status s = crc_check(rec, n, &body);
  if (s) return s;
  qi_cbor_r r; qi_r_init(&r, rec, body);
  qi_cbor_map m;
  if ((s = qi_map_begin(&r, &m))) return s;
  qi_space_state spaces[QI_MAX_SPACES]; size_t nspaces = 0; uint64_t gen = 0;
  memset(spaces, 0, sizeof spaces);
  for (;;) {
    uint64_t k; bool more;
    if ((s = qi_map_next(&m, &k, &more))) return s;
    if (!more) break;
    if (k == 1) { if ((s = qi_r_uint(&r, &gen))) return s; }
    else if (k == 2) {
      size_t cnt;
      if ((s = qi_r_array(&r, &cnt))) return s;
      if (cnt > QI_MAX_SPACES) return QI_ERR_LIMIT;
      for (size_t i = 0; i < cnt; i++) {
        qi_space_state *sp = &spaces[i];
        size_t seven; const uint8_t *b; size_t bn;
        if ((s = qi_r_array(&r, &seven))) return s;
        if (seven != 7) return QI_ERR_CBOR;
        if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; memcpy(sp->space, b, 32);
        if ((s = qi_r_uint(&r, &sp->seq))) return s;
        if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; memcpy(sp->tip, b, 32);
        if ((s = qi_r_uint(&r, &sp->clock))) return s;
        if ((s = qi_r_uint(&r, &sp->current))) return s;
        size_t ne;
        if ((s = qi_r_array(&r, &ne))) return s;
        if (ne > QI_MAX_EPOCHS_HELD) return QI_ERR_LIMIT;
        for (size_t e = 0; e < ne; e++) {
          size_t two;
          if ((s = qi_r_array(&r, &two))) return s;
          if (two != 2) return QI_ERR_CBOR;
          if ((s = qi_r_uint(&r, &sp->epochs[e].n))) return s;
          if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; memcpy(sp->epochs[e].key, b, 32);
        }
        sp->nepochs = ne;
        if ((s = qi_r_bytes(&r, &b, &bn))) return s;
        if (bn > QI_MAX_FRAME) return QI_ERR_LIMIT;
        memcpy(sp->pending, b, bn); sp->pending_n = bn;
        nspaces = i + 1;
      }
    } else if ((s = qi_r_skip(&r))) return s;
  }
  if ((s = qi_r_done(&r))) return s;
  memcpy(c->spaces, spaces, sizeof spaces);
  c->nspaces = nspaces;
  c->generation = gen;
  return QI_OK;
}

/* identity record: {1 device_seed 2 x25519 3 terminal_seed 4 principal 5 cert
 *                   6 label 7 kind 8 [[channel,kind,unit,label]...] 9 manifest 10 provisioned} */
qi_status qi_instrument_identity_encode(const qi_instrument *c, uint8_t *out, size_t cap, size_t *n) {
  if (!c->have_keys || cap < 4) return QI_ERR_STATE;
  qi_cbor_w w; qi_w_init(&w, out, cap - 4);
  qi_w_map(&w, 10);
  qi_w_uint(&w, 1); qi_w_bytes(&w, c->device_seed, 32);
  qi_w_uint(&w, 2); qi_w_bytes(&w, c->x25519, 32);
  qi_w_uint(&w, 3); qi_w_bytes(&w, c->terminal_seed, 32);
  qi_w_uint(&w, 4); qi_w_bytes(&w, c->principal, 32);
  qi_w_uint(&w, 5); qi_w_bytes(&w, c->cert, c->cert_n);
  qi_w_uint(&w, 6); qi_w_text(&w, c->label, strlen(c->label));
  qi_w_uint(&w, 7); qi_w_uint(&w, c->kind);
  qi_w_uint(&w, 8); qi_w_array(&w, c->nchannels);
  for (size_t i = 0; i < c->nchannels; i++) {
    const qi_channel_copy *ch = &c->channels[i];
    qi_w_array(&w, 4);
    qi_w_text(&w, ch->channel, strlen(ch->channel)); qi_w_text(&w, ch->kind, strlen(ch->kind));
    qi_w_text(&w, ch->unit, strlen(ch->unit)); qi_w_text(&w, ch->label, strlen(ch->label));
  }
  qi_w_uint(&w, 9); qi_w_bytes(&w, c->manifest, c->manifest_n);
  qi_w_uint(&w, 10); qi_w_bool(&w, c->provisioned);
  size_t body;
  qi_status st = qi_w_done(&w, &body);
  if (st) return st;
  uint32_t crc = qi_crc32(out, body);
  out[body] = (uint8_t)crc; out[body + 1] = (uint8_t)(crc >> 8); out[body + 2] = (uint8_t)(crc >> 16); out[body + 3] = (uint8_t)(crc >> 24);
  *n = body + 4;
  return QI_OK;
}

qi_status qi_instrument_identity_decode(qi_instrument *c, const uint8_t *rec, size_t n) {
  size_t body;
  qi_status s = crc_check(rec, n, &body);
  if (s) return s;
  qi_cbor_r r; qi_r_init(&r, rec, body);
  qi_cbor_map m;
  if ((s = qi_map_begin(&r, &m))) return s;
  uint8_t ds[32], xs[32], ts[32]; bool have = false;
  for (;;) {
    uint64_t k; bool more;
    if ((s = qi_map_next(&m, &k, &more))) return s;
    if (!more) break;
    const uint8_t *b; size_t bn; const char *t; uint64_t u; bool bv;
    switch (k) {
    case 1: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; memcpy(ds, b, 32); break;
    case 2: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; memcpy(xs, b, 32); break;
    case 3: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; memcpy(ts, b, 32); have = true; break;
    case 4: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn != 32) return QI_ERR_CBOR; memcpy(c->principal, b, 32); c->have_principal = true; break;
    case 5: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn > sizeof c->cert) return QI_ERR_LIMIT; memcpy(c->cert, b, bn); c->cert_n = bn; break;
    case 6: if ((s = qi_r_text(&r, &t, &bn))) return s; if (bn > QI_MAX_LABEL) return QI_ERR_LIMIT; memcpy(c->label, t, bn); c->label[bn] = 0; break;
    case 7: if ((s = qi_r_uint(&r, &u))) return s; c->kind = (uint8_t)u; break;
    case 8: {
      size_t cnt;
      if ((s = qi_r_array(&r, &cnt))) return s;
      if (cnt > QI_MAX_CHANNELS) return QI_ERR_LIMIT;
      for (size_t i = 0; i < cnt; i++) {
        size_t four;
        if ((s = qi_r_array(&r, &four))) return s;
        if (four != 4) return QI_ERR_CBOR;
        char *dst[4] = {c->channels[i].channel, c->channels[i].kind, c->channels[i].unit, c->channels[i].label};
        size_t caps[4] = {sizeof c->channels[i].channel, sizeof c->channels[i].kind, sizeof c->channels[i].unit, sizeof c->channels[i].label};
        for (int f = 0; f < 4; f++) {
          if ((s = qi_r_text(&r, &t, &bn))) return s;
          if (bn >= caps[f]) return QI_ERR_LIMIT;
          memcpy(dst[f], t, bn); dst[f][bn] = 0;
        }
      }
      c->nchannels = cnt;
      break;
    }
    case 9: if ((s = qi_r_bytes(&r, &b, &bn))) return s; if (bn > sizeof c->manifest) return QI_ERR_LIMIT; memcpy(c->manifest, b, bn); c->manifest_n = bn; break;
    case 10: if ((s = qi_r_bool(&r, &bv))) return s; c->provisioned = bv; break;
    default: if ((s = qi_r_skip(&r))) return s;
    }
  }
  if ((s = qi_r_done(&r))) return s;
  if (!have) return QI_ERR_CBOR;
  if ((s = qi_instrument_set_keys(c, ds, xs, ts))) return s;
  if (c->manifest_n) { qi_sha256(c->manifest, c->manifest_n, c->manifest_hash); c->declared = true; }
  return QI_OK;
}

/* ---- emit ---- */
qi_status qi_instrument_emit(qi_instrument *c, const uint8_t space[32], const qi_observation *o,
                             uint8_t *out, size_t cap, size_t *n) {
  if (!c->provisioned) return QI_ERR_STATE;
  qi_space_state *st = find_space(c, space);
  if (!st) return QI_ERR_STATE;
  if (st->pending_n) return QI_ERR_STATE; /* one frame owed at a time: send it first */
  const qi_epoch_key *key = qi_instrument_current_epoch(c, space);
  if (!key) return QI_ERR_NO_EPOCH;
  uint64_t now = c->now ? c->now(c->now_ud) : 0;
  if (!now) return QI_ERR_NO_TIME;
  qi_observation obs = *o;
  if (!obs.observed_at) obs.observed_at = now;
  if (!obs.stale_after) return QI_ERR_ARG;
  uint8_t payload[QI_MAX_PAYLOAD]; size_t pn;
  qi_status s = qi_observation_encode(&obs, payload, sizeof payload, &pn);
  if (s) return s;
  /* seal: aad = plane ‖ cbor(n) ‖ schema; nonce random */
  uint8_t plane[32], aad[64]; size_t an = 0;
  qi_instrument_plane_id(space, plane);
  memcpy(aad, plane, 32); an = 32;
  qi_cbor_w w; qi_w_init(&w, aad + an, 9); qi_w_uint(&w, key->n); size_t wn; qi_w_done(&w, &wn); an += wn;
  memcpy(aad + an, SCHEMA_OBS, sizeof SCHEMA_OBS - 1); an += sizeof SCHEMA_OBS - 1;
  uint8_t nonce[24];
  qi_random(nonce, 24);
  uint8_t ct[QI_MAX_PAYLOAD + 16]; size_t ctn;
  if ((s = qi_xchacha_seal(key->key, nonce, payload, pn, aad, an, ct, sizeof ct, &ctn))) return s;
  uint8_t sealed[QI_MAX_PAYLOAD + 64]; size_t sn;
  qi_w_init(&w, sealed, sizeof sealed);
  qi_w_map(&w, 3); qi_w_uint(&w, 1); qi_w_uint(&w, key->n); qi_w_uint(&w, 2); qi_w_bytes(&w, nonce, 24);
  qi_w_uint(&w, 3); qi_w_bytes(&w, ct, ctn);
  if ((s = qi_w_done(&w, &sn))) return s;
  /* the envelope: next sequence on this device's chain in this space */
  qi_envelope e;
  memset(&e, 0, sizeof e);
  e.terminal = space; e.principal = c->principal; e.device = c->device_pub;
  e.sequence = st->seq + 1; e.previous = st->seq ? st->tip : NULL;
  e.schema = SCHEMA_OBS; e.schema_n = sizeof SCHEMA_OBS - 1;
  e.created_at = obs.observed_at; e.logical_clock = st->clock + 1;
  e.produced_by = QI_AUTHORSHIP_SENSOR; e.source_terminal = c->terminal_pub;
  e.payload_encoding = QI_PAYLOAD_INSTRUMENT_SEALED; e.payload = sealed; e.payload_n = sn;
  e.priority = QI_PRIORITY_TELEMETRY; e.expires_at = obs.observed_at + obs.stale_after; e.max_forwards = 1;
  size_t fn;
  if ((s = qi_envelope_sign(&e, c->device_sk, st->pending, sizeof st->pending, &fn))) return s;
  if (fn > cap) return QI_ERR_SPACE;
  /* advance the chain and make it durable BEFORE the frame leaves */
  st->pending_n = fn;
  st->seq = e.sequence;
  st->clock = e.logical_clock;
  qi_event_id(st->pending, fn, st->tip);
  c->generation++;
  if ((s = persist_now(c))) {
    /* the record did not land: the frame must not leave either */
    st->pending_n = 0; st->seq--; st->clock--;
    return s;
  }
  memcpy(out, st->pending, fn);
  *n = fn;
  return QI_OK;
}

qi_status qi_instrument_pending(const qi_instrument *c, const uint8_t space[32],
                                uint8_t *out, size_t cap, size_t *n) {
  for (size_t i = 0; i < c->nspaces; i++) {
    const qi_space_state *s = &c->spaces[i];
    if (memcmp(s->space, space, 32) != 0) continue;
    if (s->pending_n > cap) return QI_ERR_SPACE;
    memcpy(out, s->pending, s->pending_n);
    *n = s->pending_n;
    return QI_OK;
  }
  return QI_ERR_STATE;
}

qi_status qi_instrument_ack_sent(qi_instrument *c, const uint8_t space[32]) {
  qi_space_state *st = find_space(c, space);
  if (!st) return QI_ERR_STATE;
  if (!st->pending_n) return QI_OK;
  st->pending_n = 0;
  c->generation++;
  return persist_now(c);
}
