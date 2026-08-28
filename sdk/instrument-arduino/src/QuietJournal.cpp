#include "QuietJournal.h"
#include <string.h>

bool QuietJournal::begin(const char *ns) { return prefs_.begin(ns, false); }

void QuietJournal::slotKey(char *out, size_t cap, const char *kind, char slot) {
  snprintf(out, cap, "%s%c", kind, slot);
}

size_t QuietJournal::load(const char *kind, uint8_t *out, size_t cap,
                          uint64_t (*generationOf)(const uint8_t *, size_t)) {
  char ka[16], kb[16];
  slotKey(ka, sizeof ka, kind, 'A');
  slotKey(kb, sizeof kb, kind, 'B');
  size_t best = 0;
  uint64_t bestGen = 0;
  bool have = false;
  const char *keys[2] = {ka, kb};
  static uint8_t tmp[8192];
  for (int i = 0; i < 2; i++) {
    size_t n = prefs_.getBytesLength(keys[i]);
    if (n == 0 || n > sizeof tmp || n > cap) continue;
    prefs_.getBytes(keys[i], tmp, n);
    uint64_t g = generationOf(tmp, n);  // 0 = invalid (bad CRC)
    if (g == 0) continue;
    if (!have || g > bestGen) { memcpy(out, tmp, n); best = n; bestGen = g; have = true; }
  }
  return best;
}

bool QuietJournal::store(const char *kind, const uint8_t *rec, size_t n,
                         uint64_t (*generationOf)(const uint8_t *, size_t)) {
  char ka[16], kb[16];
  slotKey(ka, sizeof ka, kind, 'A');
  slotKey(kb, sizeof kb, kind, 'B');
  // Which slot holds the newest valid record? Write the OTHER one.
  static uint8_t tmp[8192];
  uint64_t ga = 0, gb = 0;
  size_t na = prefs_.getBytesLength(ka), nb = prefs_.getBytesLength(kb);
  if (na && na <= sizeof tmp) { prefs_.getBytes(ka, tmp, na); ga = generationOf(tmp, na); }
  if (nb && nb <= sizeof tmp) { prefs_.getBytes(kb, tmp, nb); gb = generationOf(tmp, nb); }
  const char *target = (ga > gb) ? kb : ka;
  size_t wrote = prefs_.putBytes(target, rec, n);
  if (wrote != n) return false;
  // Read back: the slot must now hold exactly what we wrote.
  if (prefs_.getBytesLength(target) != n) return false;
  prefs_.getBytes(target, tmp, n);
  return memcmp(tmp, rec, n) == 0;
}

void QuietJournal::wipe() { prefs_.clear(); }
