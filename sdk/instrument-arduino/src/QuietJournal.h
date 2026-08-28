// Crash-consistent records on NVS (owner's amendment 11): two slots per
// record kind; a write goes to the slot that does NOT hold the newest
// valid record, and a read takes the slot whose record both passes its
// CRC and carries the higher generation. After any power loss exactly one
// complete record is readable — old or new, never a blend. Several
// Preferences.put*() calls are NOT this; one putBytes of one record is.
#pragma once
#include <Arduino.h>
#include <Preferences.h>

class QuietJournal {
 public:
  bool begin(const char *ns = "qi");
  // Load the newest valid record of a kind; returns length or 0.
  size_t load(const char *kind, uint8_t *out, size_t cap, uint64_t (*generationOf)(const uint8_t *, size_t));
  // Write a record to the slot not holding the newest valid one.
  bool store(const char *kind, const uint8_t *rec, size_t n, uint64_t (*generationOf)(const uint8_t *, size_t));
  void wipe();

 private:
  Preferences prefs_;
  static void slotKey(char *out, size_t cap, const char *kind, char slot);
};
