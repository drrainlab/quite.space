// QI-G0 — the Sensor Terminal: five honest channels on a Heltec V3.
//
// The honesty laws this sketch lives by, numbered so the gate can point
// at them:
//   1. NaN IS SILENCE. A failed DHT read, a cold MQ135 — the reader
//      returns NaN and the SDK publishes NOTHING for that tick. No
//      sentinels, no last-known-good dressed as fresh.
//   2. A RAW UNIT TELLS THE TRUTH. air_raw is the uncalibrated Rs/R0
//      ratio of an MQ135. It is not ppm and not AQI; calibration that
//      does not exist is not declared.
//   3. WARMING IS A CHANNEL, NOT VOCABULARY. The wire has no "warming"
//      status, so the story is told twice and honestly both times:
//      warm=false beside air_raw's silence, then warm=true beside its
//      numbers. The gate latches once and never un-warms on a clock
//      step (see Mq135.h).
//   4. THE RELAY IS OBSERVED, NEVER DRIVEN. Reading its control line is
//      a sensor; driving it is QI-4's business and forbidden until that
//      design note exists (ADR-025 §13).
//   5. ABSENT HARDWARE IS ABSENT, NOT ZERO. A channel whose sensor is
//      not wired is NOT declared (MQ_PRESENT / RELAY_PRESENT build
//      flags): a floating ADC pin publishes noise with a straight face,
//      and a declared channel that never speaks would look broken
//      rather than absent. Absence of a declaration is the truth here.
//
// Board notes, learned live on the first Heltec V3 this SDK ever ran
// on: the OLED sits behind Vext (GPIO36, active LOW) and its reset is
// GPIO21 — without both, u8g2's init hangs before setup() prints a
// byte, which is exactly how ClimateDHT's T3-S3-shaped init died here.
#include <QuietInstrument.h>
#include <SerialHexBearer.h>
#include <QuietGlyph.h>
#include <DHT.h>
#include <U8g2lib.h>
#include <Wire.h>

#include "Mq135.h"

#ifndef DHT_PIN
#define DHT_PIN 7
#endif
#ifndef MQ_PIN
#define MQ_PIN 6
#endif
#ifndef RELAY_PIN
#define RELAY_PIN 5
#endif
#ifndef MQ_WARMUP_SEC
#define MQ_WARMUP_SEC 180
#endif
#ifndef MQ_PRESENT
#define MQ_PRESENT 0
#endif
#ifndef RELAY_PRESENT
#define RELAY_PRESENT 0
#endif

#define VEXT_PIN 36
#define OLED_RST 21
#define OLED_SDA 17
#define OLED_SCL 18

QuietInstrument qi;
SerialHexBearer serialBearer(Serial);
DHT dht(DHT_PIN, DHT22);
U8G2_SSD1306_128X64_NONAME_F_HW_I2C oled(U8G2_R0, OLED_RST);

// TattlingBearer: the OLED's honest "last delivered" clock. Composes
// over the bearer seam — the SDK is untouched, the decorator only
// remembers WHEN a send was genuinely taken.
struct TattlingBearer : QuietBearer {
  QuietBearer *inner;
  const char *label;
  uint32_t lastSentMs = 0;
  TattlingBearer(QuietBearer *b, const char *l) : inner(b), label(l) {}
  bool send(const uint8_t *frame, size_t n) override {
    bool ok = inner->send(frame, n);
    if (ok) lastSentMs = millis();
    return ok;
  }
  void poll(QuietInstrument &q) override { inner->poll(q); }
};
TattlingBearer bearer(&serialBearer, "serial");

float lastT = NAN, lastH = NAN, lastAir = NAN;
WarmGate warmGate;

float readTemp() { lastT = dht.readTemperature(); return lastT; }
float readHumidity() { lastH = dht.readHumidity(); return lastH; }

uint64_t clockNow() {
  time_t t = time(nullptr);
  return t > 1700000000 ? (uint64_t)t : 0;
}

#if MQ_PRESENT
// The clean-air anchor: captured once at first warm read. A real
// calibration would live in NVS and be set deliberately; v1 anchors on
// "the air when it warmed up", which is exactly as much as an
// uncalibrated MQ can honestly claim — a ratio against its own morning.
uint16_t mqAnchor = 0;

float readAir() {
  uint32_t now = (uint32_t)(clockNow() ? clockNow() : 0);
  if (now == 0) { lastAir = NAN; return NAN; }   // no clock, no warm story
  warmGate.start(now);
  if (!warmGate.warm(now)) { lastAir = NAN; return NAN; }  // law 1 + 3
  uint16_t raw = analogRead(MQ_PIN);
  if (mqAnchor == 0 && raw > 200 && raw < 3900) mqAnchor = raw;
  lastAir = mq135Ratio(raw, mqAnchor, 4095);
  return lastAir;
}

bool readWarm() {
  uint32_t now = (uint32_t)clockNow();
  if (now == 0) return false;
  warmGate.start(now);
  return warmGate.warm(now);
}
#endif

#if RELAY_PRESENT
bool readRelay() { return digitalRead(RELAY_PIN) == HIGH; }
#endif

void splash() {
  oled.clearBuffer();
  oled.drawXBMP((128 - QUIET_GLYPH_W) / 2, 2, QUIET_GLYPH_W, QUIET_GLYPH_H, QUIET_GLYPH_XBM);
  oled.setFont(u8g2_font_6x12_tf);
  const char *brand = "quiet.space";
  oled.drawStr((128 - oled.getStrWidth(brand)) / 2, 62, brand);
  oled.sendBuffer();
}

void status() {
  oled.clearBuffer();
  oled.drawXBMP(0, 0, QUIET_GLYPH_W, QUIET_GLYPH_H, QUIET_GLYPH_XBM);
  oled.setFont(u8g2_font_7x13B_tf);
  oled.drawStr(54, 14, "Sensors");
  oled.setFont(u8g2_font_6x12_tf);
  char line[26];
  if (isnan(lastT)) snprintf(line, sizeof line, "T  --.-");
  else snprintf(line, sizeof line, "T %5.1fC", lastT);
  oled.drawStr(54, 28, line);
  if (isnan(lastH)) snprintf(line, sizeof line, "H  --.-");
  else snprintf(line, sizeof line, "H %5.1f%%", lastH);
  oled.drawStr(54, 40, line);
#if MQ_PRESENT
  // the face mirrors the wire's silence: dashes until the gate opens
  if (isnan(lastAir)) snprintf(line, sizeof line, "air --.-");
  else snprintf(line, sizeof line, "air %4.2f", lastAir);
  oled.drawStr(54, 52, line);
#endif
  // bottom line alternates: who am I ↔ who carried the last frame
  static bool flip = false;
  flip = !flip;
  if (flip && bearer.lastSentMs > 0) {
    uint32_t age = (millis() - bearer.lastSentMs) / 1000;
    snprintf(line, sizeof line, "via %s * %lus", bearer.label, (unsigned long)age);
    oled.drawStr(0, 62, line);
  } else {
    const char *state = !qi.declared() ? "waiting for owner"
                        : !qi.provisioned() ? "enrolling..."
                        : "in the space";
    oled.drawStr(0, 62, state);
  }
  oled.sendBuffer();
}

void setup() {
  // The provision line is ~1.7KB and an epoch push can reach 4.6KB; the
  // ESP32 HardwareSerial default RX buffer is 256 bytes — 22ms of line
  // at 115200 — while one OLED sendBuffer blocks for 30-40ms. On the
  // T3-S3 the USB-CDC path hid this; the Heltec's CP210x UART exposed
  // it on the first live provision: a torn line, refused as malformed
  // CBOR. The buffer must hold the longest line the wire may carry.
  Serial.setRxBufferSize(8192);
  Serial.begin(115200);
  delay(500);

  // Heltec V3 display power-up ritual (see the board note up top)
  pinMode(VEXT_PIN, OUTPUT);
  digitalWrite(VEXT_PIN, LOW);
  delay(100);
  pinMode(OLED_RST, OUTPUT);
  digitalWrite(OLED_RST, LOW);
  delay(20);
  digitalWrite(OLED_RST, HIGH);
  delay(20);

  Wire.begin(OLED_SDA, OLED_SCL);
  oled.begin();
  splash();

  dht.begin();
#if RELAY_PRESENT
  pinMode(RELAY_PIN, INPUT);
#endif
#if MQ_PRESENT
  analogReadResolution(12);
  warmGate.warmAfterSec = MQ_WARMUP_SEC;
#endif

  qi.begin("Sensors");
  qi.numberSensor("temperature", "Температура", "°C", readTemp, 1, 10, 60);
  qi.numberSensor("humidity", "Влажность", "%", readHumidity, 1, 10, 60);
#if MQ_PRESENT
  qi.numberSensor("air_raw", "Воздух (сырое)", "Rs/R0", readAir, 2, 15, 120);
  qi.booleanSensor("warm", "Прогрев", readWarm, 60, 120);
#endif
#if RELAY_PRESENT
  qi.booleanSensor("relay", "Реле", readRelay, 60, 120);
#endif
  qi.setClock(clockNow);
  qi.setBearer(&bearer);
  Serial.println("QI NOTE SensorTerminal ready");
  delay(1200);
}

void loop() {
  qi.loop();
  static uint32_t lastDraw = 0;
  if (millis() - lastDraw > 1000) {
    lastDraw = millis();
    status();
  }
  delay(50);
}
