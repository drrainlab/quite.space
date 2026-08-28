// A climate instrument: LilyGo T3-S3 + DHT22 — with the quiet.space
// mark on the board's OLED. Two channels, temperature °C and humidity %,
// declared like the reference greenhouse's first two, so the panel
// renders them the same way.
//
// Wiring: DHT22 VCC → 3V3, GND → GND, DATA → DHT_PIN (10k pull-up to 3V3
// if your module has none). The T3-S3 OLED is SSD1306 on SDA 18 / SCL 17.
// The DHT library returns NaN on a failed read; the instrument treats NaN
// as "nothing honest to say" and publishes nothing for that tick.
#include <QuietInstrument.h>
#include <SerialHexBearer.h>
#include <QuietGlyph.h>
#include <DHT.h>
#include <U8g2lib.h>
#include <Wire.h>

#ifndef DHT_PIN
#define DHT_PIN 38
#endif
#ifndef OLED_SDA
#define OLED_SDA 18
#define OLED_SCL 17
#endif

QuietInstrument qi;
SerialHexBearer bearer(Serial);
DHT dht(DHT_PIN, DHT22);
U8G2_SSD1306_128X64_NONAME_F_HW_I2C oled(U8G2_R0, U8X8_PIN_NONE);

float lastT = NAN, lastH = NAN;
float readTemp() { lastT = dht.readTemperature(); return lastT; }
float readHumidity() { lastH = dht.readHumidity(); return lastH; }
uint64_t clockNow() { time_t t = time(nullptr); return t > 1700000000 ? (uint64_t)t : 0; }

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
  oled.drawStr(54, 14, "Climate");
  oled.setFont(u8g2_font_6x12_tf);
  char line[24];
  if (isnan(lastT)) snprintf(line, sizeof line, "T  --.- C"); else snprintf(line, sizeof line, "T %5.1f C", lastT);
  oled.drawStr(54, 30, line);
  if (isnan(lastH)) snprintf(line, sizeof line, "H  --.- %%"); else snprintf(line, sizeof line, "H %5.1f %%", lastH);
  oled.drawStr(54, 44, line);
  const char *state = !qi.declared() ? "waiting for owner" : !qi.provisioned() ? "enrolling..." : "in the space";
  oled.drawStr(0, 62, state);
  oled.sendBuffer();
}

void setup() {
  Serial.begin(115200);
  delay(500);
  Wire.begin(OLED_SDA, OLED_SCL);
  oled.begin();
  splash();
  dht.begin();
  qi.begin("Climate");
  qi.numberSensor("temperature", "Температура", "°C", readTemp, 1, 10, 60);
  qi.numberSensor("humidity", "Влажность", "%", readHumidity, 1, 10, 60);
  qi.setClock(clockNow);
  qi.setBearer(&bearer);
  Serial.println("QI NOTE ClimateDHT ready");
  delay(1500);
}

void loop() {
  qi.loop();
  static uint32_t lastDraw = 0;
  if (millis() - lastDraw > 1000) { lastDraw = millis(); status(); }
  delay(50);
}
