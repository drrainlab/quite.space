// QuietInstrument — an ESP32 becomes an instrument of a Quiet space.
//
//   QuietInstrument qi;
//   qi.begin("Greenhouse");                        // instrument label
//   qi.numberSensor("temperature", "Температура", "°C", readTemp,
//                   /*decimals*/1, /*sampleEvery*/10, /*staleAfter*/60);
//   qi.booleanSensor("door", "Дверь", readDoor, /*heartbeat*/60, /*staleAfter*/120);
//   qi.loop();
//
// Three clocks that are NOT the same thing (owner's amendment 6):
//   sampleEvery — how often the callback is asked;
//   staleAfter  — after how long a reading is no longer honest as "now";
//   heartbeat   — how often an unchanged value is re-published anyway
//                 (numbers: ≤ staleAfter/2; booleans: as given).
// A changed value publishes at once. Freshness never dictates airtime.
//
// The core underneath (src/qi) holds the keys, the chain, the epoch keys
// and the one frame it may owe; this class is scheduling, storage and
// the bearer seam. Readings are floats at the edge and fixed point on
// the wire: fromFloat rounds honestly to the declared decimals, and no
// float ever reaches the core.
#pragma once
#include <Arduino.h>
#include "QuietBearer.h"
#include "QuietJournal.h"
extern "C" {
#include "qi/instrument.h"
}

typedef float (*QiReadNumber)();
typedef bool (*QiReadBool)();

class QuietInstrument {
 public:
  static const size_t MAX_CHANNELS = QI_MAX_CHANNELS;

  // label: the instrument's name. Loads or mints the identity.
  bool begin(const char *label, uint8_t kind = QI_KIND_SENSOR);

  bool numberSensor(const char *channel, const char *label, const char *unit, QiReadNumber read,
                    uint8_t decimals, uint32_t sampleEvery, uint32_t staleAfter);
  bool booleanSensor(const char *channel, const char *label, QiReadBool read,
                     uint32_t heartbeat, uint32_t staleAfter);

  // Unix time source (seconds). Without one the instrument does not emit.
  void setClock(uint64_t (*now)()) { now_ = now; }
  // A host handed over the time (dev bearer "QI TIME <unix>"): kept as an
  // offset from millis(), honest until the next reboot.
  void setUnixTime(uint64_t unixSec);
  void setBearer(QuietBearer *b) { bearer_ = b; }

  // Provisioning (dev path; a real bearer will carry the same bytes).
  bool setPrincipalHex(const char *hex);
  bool provisionHex(const char *hex);
  bool ingestHex(const char *hex);
  void printEnrollment();
  void wipe();

  bool provisioned() const { return c_.provisioned; }
  bool declared() const { return c_.declared; }
  const qi_instrument &core() const { return c_; }

  void loop();

 private:
  struct Channel {
    char name[33];
    bool isBool;
    QiReadNumber readN;
    QiReadBool readB;
    uint8_t decimals;
    uint32_t sampleEvery, heartbeat, staleAfter;
    uint32_t lastSample, lastPublish;  // millis()/1000
    bool have;
    int64_t lastMantissa;
    bool lastBool;
  };
  qi_instrument c_;
  Channel ch_[MAX_CHANNELS];
  size_t nch_ = 0;
  qi_channel_decl decl_[MAX_CHANNELS];
  char declStore_[MAX_CHANNELS][3][QI_MAX_STR];
  char label_[QI_MAX_LABEL + 1];
  uint8_t kind_;
  bool declaredOnce_ = false;
  QuietJournal journal_;
  QuietBearer *bearer_ = nullptr;
  uint64_t (*now_)() = nullptr;
  bool warnedNoTime_ = false;
  uint64_t hostTime_ = 0;      // unix seconds at hostTimeMillis_
  uint32_t hostTimeMillis_ = 0;
  uint8_t space_[32];
  bool haveSpace_ = false;
  uint8_t frame_[QI_MAX_FRAME];

  bool declare();
  bool persistIdentity();
  static qi_status persistState(void *ud, const uint8_t *rec, size_t n);
  static uint64_t clockThunk(void *ud);
  void publish(Channel &c, uint32_t nowSec);
  void flushOwed();
  void note(const char *what, qi_status s = QI_OK);
  static size_t unhex(const char *hex, uint8_t *out, size_t cap);
  static void hex(const uint8_t *b, size_t n, Stream &out);
  static int64_t fromFloat(float v, uint8_t decimals);
};
