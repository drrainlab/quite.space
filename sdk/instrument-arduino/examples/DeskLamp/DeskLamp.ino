// The smallest instrument: one boolean. A lamp that reports whether it is
// on (GPIO 5 as input here; an actuator is QI-4's business).
#include <QuietInstrument.h>
#include <SerialHexBearer.h>

QuietInstrument qi;
SerialHexBearer bearer(Serial);
bool lampOn() { return digitalRead(5) == HIGH; }
uint64_t clockNow() { time_t t = time(nullptr); return t > 1700000000 ? (uint64_t)t : 0; }

void setup() {
  Serial.begin(115200);
  pinMode(5, INPUT);
  qi.begin("Desk lamp");
  qi.booleanSensor("lamp", "Лампа", lampOn, 60, 120);
  qi.setClock(clockNow);
  qi.setBearer(&bearer);
}

void loop() { qi.loop(); delay(50); }
