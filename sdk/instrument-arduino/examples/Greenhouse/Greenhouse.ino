// The reference greenhouse on a Heltec WiFi LoRa 32 V3 — the same four
// channels the simulator declares, so the browser cannot tell them apart.
//   temperature °C, humidity %  — BME280 on I²C (falls back to the chip's
//                                  own thermometer, humidity then silent)
//   door boolean                — the PRG button (GPIO 0), pressed = open
//   light %                     — an LDR/ADC on GPIO 7, or nothing
// Wire up the dev bearer over USB serial; provision with the Go stand.
#include <Wire.h>
#include <QuietInstrument.h>
#include <SerialHexBearer.h>
#include <Adafruit_BME280.h>

QuietInstrument qi;
SerialHexBearer bearer(Serial);
Adafruit_BME280 bme;
bool haveBME = false;

float readTemp() { return haveBME ? bme.readTemperature() : temperatureRead(); }
float readHumidity() { return haveBME ? bme.readHumidity() : NAN; }  // NAN = nothing honest to say
bool readDoor() { return digitalRead(0) == LOW; }
float readLight() { return (float)analogRead(7) * 100.0f / 4095.0f; }

// A unix clock: the stand hands one over the bearer, or NTP — here the
// simplest honest thing: none until something sets it.
uint64_t clockNow() { time_t t = time(nullptr); return t > 1700000000 ? (uint64_t)t : 0; }

void setup() {
  Serial.begin(115200);
  delay(300);
  pinMode(0, INPUT_PULLUP);
  Wire.begin(41, 42);  // Heltec V3 I²C header pins (SDA, SCL)
  haveBME = bme.begin(0x76, &Wire) || bme.begin(0x77, &Wire);
  qi.begin("Greenhouse");
  qi.numberSensor("temperature", "Температура", "°C", readTemp, 1, 10, 60);
  qi.numberSensor("humidity", "Влажность", "%", readHumidity, 1, 10, 60);
  qi.booleanSensor("door", "Дверь", readDoor, 60, 120);
  qi.numberSensor("light", "Свет", "%", readLight, 0, 10, 60);
  qi.setClock(clockNow);
  qi.setBearer(&bearer);
  Serial.println(haveBME ? "QI NOTE BME280 found" : "QI NOTE no BME280 — internal thermometer, humidity silent");
}

void loop() {
  qi.loop();
  delay(50);
}
