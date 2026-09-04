#include "QuietInstrument.h"
#include <string.h>
#include <math.h>
#include <time.h>
extern "C" {
#include "qi/crypto.h"
#include "qi/cbor.h"
}

// generation of a state/identity record, 0 if the CRC refuses it
static uint64_t stateGeneration(const uint8_t *rec, size_t n) {
  if (n < 5) return 0;
  uint32_t want = (uint32_t)rec[n - 4] | (uint32_t)rec[n - 3] << 8 | (uint32_t)rec[n - 2] << 16 | (uint32_t)rec[n - 1] << 24;
  if (qi_crc32(rec, n - 4) != want) return 0;
  qi_cbor_r r; qi_r_init(&r, rec, n - 4);
  qi_cbor_map m; uint64_t k; bool more; uint64_t g = 0;
  if (qi_map_begin(&r, &m)) return 0;
  if (qi_map_next(&m, &k, &more) || !more || k != 1) return 1;  // identity record: any valid = gen 1
  if (qi_r_uint(&r, &g)) return 0;
  return g ? g : 1;
}
// An identity record has no generation: there is only ever one, and a
// valid CRC is the whole verdict. It must NOT be judged by the state
// record's grammar — its key 1 is the device seed (bytes), and reading
// that as a generation uint refused every identity ever stored, so each
// reboot minted a fresh one. The first board that rebooted on a desk
// found this; the hardware gate is the only place it could have shown.
static uint64_t identityGeneration(const uint8_t *rec, size_t n) {
  if (n < 5) return 0;
  uint32_t want = (uint32_t)rec[n - 4] | (uint32_t)rec[n - 3] << 8 | (uint32_t)rec[n - 2] << 16 | (uint32_t)rec[n - 1] << 24;
  return qi_crc32(rec, n - 4) == want ? 1 : 0;
}

// "sp": the space this instrument speaks in — the one it was provisioned
// into most recently. The core keeps up to QI_MAX_SPACES states and does
// not rank them; a board re-provisioned into a new space would otherwise
// keep emitting into the old one after a reboot (found live: a restored
// state from a previous enrollment carried yesterday's space first).
static uint64_t spaceGeneration(const uint8_t *rec, size_t n) {
  if (n != 36) return 0;
  uint32_t want = (uint32_t)rec[32] | (uint32_t)rec[33] << 8 | (uint32_t)rec[34] << 16 | (uint32_t)rec[35] << 24;
  return qi_crc32(rec, 32) == want ? 1 : 0;
}

void QuietInstrument::setUnixTime(uint64_t unixSec) {
  hostTime_ = unixSec; hostTimeMillis_ = millis();
  warnedNoTime_ = false;
  note("clock set by host");
}

uint64_t QuietInstrument::clockThunk(void *ud) {
  QuietInstrument *self = (QuietInstrument *)ud;
  if (self->now_) { uint64_t t = self->now_(); if (t) return t; }
  if (self->hostTime_) return self->hostTime_ + (millis() - self->hostTimeMillis_) / 1000;
  time_t t = time(nullptr);
  return t > 1700000000 ? (uint64_t)t : 0;  // unsynced RTC is not a clock
}

qi_status QuietInstrument::persistState(void *ud, const uint8_t *rec, size_t n) {
  QuietInstrument *self = (QuietInstrument *)ud;
  return self->journal_.store("st", rec, n, stateGeneration) ? QI_OK : QI_ERR_STATE;
}

bool QuietInstrument::begin(const char *label, uint8_t kind) {
  strlcpy(label_, label, sizeof label_);
  kind_ = kind;
  if (qi_crypto_init() != QI_OK) { note("libsodium init"); return false; }
  qi_instrument_init(&c_);
  c_.persist = persistState; c_.persist_ud = this;
  c_.now = clockThunk; c_.now_ud = this;
  if (!journal_.begin()) { note("NVS open"); return false; }
  static uint8_t rec[8192];
  size_t n = journal_.load("id", rec, sizeof rec, identityGeneration);
  if (n && qi_instrument_identity_decode(&c_, rec, n) == QI_OK) {
    { char who[64]; snprintf(who, sizeof who, "identity restored dev=%02x%02x%02x%02x term=%02x%02x%02x%02x",
        c_.device_pub[0], c_.device_pub[1], c_.device_pub[2], c_.device_pub[3],
        c_.terminal_pub[0], c_.terminal_pub[1], c_.terminal_pub[2], c_.terminal_pub[3]);
      note(who); }
    size_t sn = journal_.load("st", rec, sizeof rec, stateGeneration);
    if (sn && qi_instrument_state_decode(&c_, rec, sn) == QI_OK) note("chain state restored");
    uint8_t sp[36];
    if (journal_.load("sp", sp, sizeof sp, spaceGeneration) == 36) {
      for (size_t i = 0; i < c_.nspaces; i++)
        if (!memcmp(c_.spaces[i].space, sp, 32)) { memcpy(space_, sp, 32); haveSpace_ = true; }
    }
    if (!haveSpace_ && c_.nspaces) { memcpy(space_, c_.spaces[c_.nspaces - 1].space, 32); haveSpace_ = true; }
  } else {
    if (qi_instrument_keygen(&c_) != QI_OK) { note("keygen"); return false; }
    // A fresh identity starts with a fresh journal: state and space
    // records of a previous life must not be mistaken for this one's.
    journal_.wipe();
    { char who[64]; snprintf(who, sizeof who, "new identity minted dev=%02x%02x%02x%02x term=%02x%02x%02x%02x",
        c_.device_pub[0], c_.device_pub[1], c_.device_pub[2], c_.device_pub[3],
        c_.terminal_pub[0], c_.terminal_pub[1], c_.terminal_pub[2], c_.terminal_pub[3]);
      note(who); }
    persistIdentity();
  }
  return true;
}

bool QuietInstrument::persistIdentity() {
  static uint8_t rec[8192];
  size_t n;
  if (qi_instrument_identity_encode(&c_, rec, sizeof rec, &n) != QI_OK) return false;
  return journal_.store("id", rec, n, identityGeneration);
}

static bool copyDecl(char *dst, size_t cap, const char *src) {
  if (!src) { dst[0] = 0; return true; }
  return strlcpy(dst, src, cap) < cap;
}

bool QuietInstrument::numberSensor(const char *channel, const char *label, const char *unit, QiReadNumber read,
                                   uint8_t decimals, uint32_t sampleEvery, uint32_t staleAfter) {
  if (nch_ >= MAX_CHANNELS || !staleAfter || !sampleEvery || decimals > 18) return false;
  Channel &c = ch_[nch_];
  memset(&c, 0, sizeof c);
  strlcpy(c.name, channel, sizeof c.name);
  c.isBool = false; c.readN = read; c.decimals = decimals;
  c.sampleEvery = sampleEvery; c.staleAfter = staleAfter;
  c.heartbeat = staleAfter / 2 ? staleAfter / 2 : 1;
  copyDecl(declStore_[nch_][0], QI_MAX_STR, channel);
  copyDecl(declStore_[nch_][1], QI_MAX_STR, unit);
  copyDecl(declStore_[nch_][2], QI_MAX_STR, label);
  decl_[nch_] = {declStore_[nch_][0], "number", declStore_[nch_][1], declStore_[nch_][2]};
  nch_++;
  return true;
}

bool QuietInstrument::booleanSensor(const char *channel, const char *label, QiReadBool read,
                                    uint32_t heartbeat, uint32_t staleAfter) {
  if (nch_ >= MAX_CHANNELS || !staleAfter || !heartbeat) return false;
  Channel &c = ch_[nch_];
  memset(&c, 0, sizeof c);
  strlcpy(c.name, channel, sizeof c.name);
  c.isBool = true; c.readB = read;
  c.sampleEvery = 1; c.heartbeat = heartbeat; c.staleAfter = staleAfter;
  copyDecl(declStore_[nch_][0], QI_MAX_STR, channel);
  declStore_[nch_][1][0] = 0;
  copyDecl(declStore_[nch_][2], QI_MAX_STR, label);
  decl_[nch_] = {declStore_[nch_][0], "boolean", nullptr, declStore_[nch_][2]};
  nch_++;
  return true;
}

bool QuietInstrument::declare() {
  if (!c_.have_principal) return false;
  qi_status s = qi_instrument_declare(&c_, label_, kind_, decl_, nch_);
  if (s != QI_OK) { note("declare", s); return false; }
  declaredOnce_ = true;
  persistIdentity();
  return true;
}

bool QuietInstrument::setPrincipalHex(const char *hexs) {
  uint8_t p[32];
  if (unhex(hexs, p, 32) != 32) { note("principal: want 64 hex chars"); return false; }
  qi_status s = qi_instrument_set_principal(&c_, p);
  if (s != QI_OK) { note("principal", s); return false; }
  if (!declare()) return false;
  printEnrollment();
  return true;
}

void QuietInstrument::printEnrollment() {
  if (!c_.declared) { note("not declared yet: send QI PRINCIPAL <hex> first"); return; }
  uint8_t nonce[16];
  qi_random(nonce, 16);
  static uint8_t out[QI_MAX_ENROLLMENT];
  size_t n;
  qi_status s = qi_instrument_enrollment(&c_, nonce, out, sizeof out, &n);
  if (s != QI_OK) { note("enrollment", s); return; }
  Serial.print("QI ENROLLMENT ");
  hex(out, n, Serial);
  Serial.println();
}

bool QuietInstrument::provisionHex(const char *hexs) {
  static uint8_t buf[QI_MAX_PROVISION];
  size_t n = unhex(hexs, buf, sizeof buf);
  if (!n) { note("provision: bad hex"); return false; }
  qi_status s = qi_instrument_provision(&c_, buf, n);
  if (s != QI_OK) { note("provision", s); return false; }
  {
    // The provision's own space (key 1 of its map) is the one to speak in.
    qi_cbor_r r; qi_r_init(&r, buf, n);
    size_t fields; uint64_t k; const uint8_t *sp; size_t spn;
    if (qi_r_map(&r, &fields) == QI_OK && fields && qi_r_uint(&r, &k) == QI_OK && k == 1 &&
        qi_r_bytes(&r, &sp, &spn) == QI_OK && spn == 32) rememberSpace(sp);
    else if (c_.nspaces) rememberSpace(c_.spaces[c_.nspaces - 1].space);
  }
  persistIdentity();
  // the epoch keys live in the state record
  static uint8_t rec[8192]; size_t rn;
  if (qi_instrument_state_encode(&c_, rec, sizeof rec, &rn) == QI_OK) journal_.store("st", rec, rn, stateGeneration);
  note("provisioned");
  return true;
}

bool QuietInstrument::ingestHex(const char *hexs) {
  static uint8_t buf[QI_MAX_FRAME * 2];
  size_t n = unhex(hexs, buf, sizeof buf);
  if (!n) { note("epoch: bad hex"); return false; }
  return ingestEpochFrame(buf, n);
}

bool QuietInstrument::space(uint8_t out[32]) const {
  if (!haveSpace_) return false;
  memcpy(out, space_, 32);
  return true;
}

void QuietInstrument::rememberSpace(const uint8_t sp[32]) {
  memcpy(space_, sp, 32); haveSpace_ = true;
  uint8_t rec[36];
  memcpy(rec, sp, 32);
  uint32_t crc = qi_crc32(rec, 32);
  rec[32] = (uint8_t)crc; rec[33] = (uint8_t)(crc >> 8); rec[34] = (uint8_t)(crc >> 16); rec[35] = (uint8_t)(crc >> 24);
  journal_.store("sp", rec, sizeof rec, spaceGeneration);
}

bool QuietInstrument::ingestEpochFrame(const uint8_t *buf, size_t n) {
  qi_status s = qi_instrument_absorb_epoch_frame(&c_, buf, n);
  if (s == QI_ERR_NOT_ADDRESSED) { note("epoch turned without me — detached?"); return false; }
  if (s != QI_OK) { note("epoch", s); return false; }
  static uint8_t rec[8192]; size_t rn;
  if (qi_instrument_state_encode(&c_, rec, sizeof rec, &rn) == QI_OK) journal_.store("st", rec, rn, stateGeneration);
  note("epoch learned");
  return true;
}

void QuietInstrument::wipe() {
  journal_.wipe();
  note("wiped; reboot to mint a new identity");
}

int64_t QuietInstrument::fromFloat(float v, uint8_t decimals) {
  double scaled = (double)v;
  for (uint8_t i = 0; i < decimals; i++) scaled *= 10.0;
  return (int64_t)llround(scaled);
}

void QuietInstrument::publish(Channel &c, uint32_t nowSec) {
  qi_observation o;
  memset(&o, 0, sizeof o);
  o.channel = c.name;
  o.stale_after = c.staleAfter;
  if (c.isBool) { o.kind = QI_VALUE_BOOL; o.bool_value = c.lastBool; }
  else { o.kind = QI_VALUE_NUMBER; o.mantissa = c.lastMantissa; o.scale = -(int8_t)c.decimals; }
  size_t n;
  qi_status s = qi_instrument_emit(&c_, space_, &o, frame_, sizeof frame_, &n);
  if (s == QI_ERR_NO_TIME) { if (!warnedNoTime_) { note("no unix time — not emitting (set the clock)"); warnedNoTime_ = true; } return; }
  if (s != QI_OK) { note("emit", s); return; }
  c.lastPublish = nowSec;
  flushOwed();
}

void QuietInstrument::flushOwed() {
  if (!bearer_ || !haveSpace_) return;
  size_t n;
  if (qi_instrument_pending(&c_, space_, frame_, sizeof frame_, &n) != QI_OK || n == 0) return;
  if (bearer_->send(frame_, n)) qi_instrument_ack_sent(&c_, space_);
}

void QuietInstrument::loop() {
  if (bearer_) bearer_->poll(*this);
  if (!c_.provisioned) return;
  flushOwed();  // a frame owed from before a reboot goes first
  uint32_t nowSec = millis() / 1000;
  for (size_t i = 0; i < nch_; i++) {
    Channel &c = ch_[i];
    if (nowSec - c.lastSample < c.sampleEvery && c.have) continue;
    c.lastSample = nowSec;
    bool changed = false;
    if (c.isBool) {
      bool v = c.readB();
      changed = !c.have || v != c.lastBool;
      c.lastBool = v;
    } else {
      float f = c.readN();
      if (isnan(f)) continue;  // the sensor had nothing honest to say
      int64_t m = fromFloat(f, c.decimals);
      changed = !c.have || m != c.lastMantissa;
      c.lastMantissa = m;
    }
    bool heartbeat = c.have && nowSec - c.lastPublish >= c.heartbeat;
    c.have = true;
    if (changed || heartbeat) publish(c, nowSec);
  }
}

void QuietInstrument::note(const char *what, qi_status s) {
  Serial.print("QI NOTE ");
  Serial.print(what);
  if (s != QI_OK) { Serial.print(": "); Serial.print(qi_status_str(s)); }
  Serial.println();
}

size_t QuietInstrument::unhex(const char *h, uint8_t *out, size_t cap) {
  size_t n = 0;
  auto v = [](char c) -> int {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
  };
  while (h[0] && h[1]) {
    int a = v(h[0]), b = v(h[1]);
    if (a < 0 || b < 0 || n >= cap) return 0;
    out[n++] = (uint8_t)(a << 4 | b);
    h += 2;
  }
  return n;
}

void QuietInstrument::hex(const uint8_t *b, size_t n, Stream &out) {
  static const char *d = "0123456789abcdef";
  for (size_t i = 0; i < n; i++) { out.print(d[b[i] >> 4]); out.print(d[b[i] & 15]); }
}
