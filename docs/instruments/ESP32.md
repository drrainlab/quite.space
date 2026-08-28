# ESP32 as a Quiet instrument

An ESP32 running `QuietInstrument` becomes a participant of a space: it
has its own keys, declares what it measures, and its readings appear in
the «Instruments» panel of every member — sealed to the space's
instrument plane, unreadable by relays and never mixed with the human
conversation (ADR-025, ADR-026). The virtual greenhouse that ships with
the node is the behavioral contract; a board that declares the same
channels is indistinguishable from it in the browser.

## The sketch

```cpp
#include <QuietInstrument.h>
#include <SerialHexBearer.h>

QuietInstrument qi;
SerialHexBearer bearer(Serial);   // dev bearer: hex lines over USB

float readTemp();  bool readDoor();

void setup() {
  Serial.begin(115200);
  qi.begin("Greenhouse");                                   // instrument label
  qi.numberSensor("temperature", "Температура", "°C", readTemp,
                  /*decimals*/1, /*sampleEvery*/10, /*staleAfter*/60);
  qi.booleanSensor("door", "Дверь", readDoor, /*heartbeat*/60, /*staleAfter*/120);
  qi.setBearer(&bearer);
}

void loop() { qi.loop(); delay(50); }
```

Three clocks, deliberately separate:

| | meaning |
|---|---|
| `sampleEvery` | how often your callback is asked |
| `staleAfter`  | after how long the reading is no longer honest as "now" (rides in every frame) |
| heartbeat     | how often an unchanged value is re-published (numbers: `staleAfter/2`; booleans: as given) |

A changed value publishes at once. Freshness never decides airtime.
Return `NAN` from a number callback when the sensor has nothing honest
to say — the channel stays silent rather than lying.

## Provisioning (dev stand)

1. Run a node with the dev door open: `terminal ui --dev-ingest …`, note
   the API URL/token and the space id.
2. Flash the sketch; connect the board over USB.
3. `go run ./cmd/instrument-serial --port /dev/cu.usbserial-XXXX --api http://127.0.0.1:PORT --token TOKEN --space SPACE`

The stand hands the board the clock and the owner's principal, the board
prints its enrollment (both keys sign it), the stand enrolls it at the
node and returns the provision (certificate + current epoch), and from
then on every `QI FRAME` line goes into the space. Rotations (someone
joins, an instrument is detached) reach the board as `QI EPOCH` lines.

The stand is **not** how instruments will reach a space in production —
LAN, relay, BLE and LoRa bearers are the next decision. The board's
bytes are already the protocol's bytes; only the carrier is open.

## What the board keeps (NVS, crash-consistent)

Two records in an A/B journal: the identity (seeds, declaration,
provision) and the chain state (sequence, tip, clock, epoch keys, and
the one frame it may owe). After any power loss one complete record is
readable. A frame is persisted before it leaves and re-sent after a
reboot, so the device's chain never has a hole (ADR-026 §4).

## Building

- PlatformIO: `cd sdk/instrument-arduino/examples/Greenhouse && pio run`
  (board `heltec_wifi_lora_32_V3`; libsodium comes with arduino-esp32).
- The C core (`sdk/instrument-c`) is copied into `src/qi/` by `sync.sh`;
  host tests (`cmake && ctest`) check it byte for byte against the Go
  vectors, and `tools/qi-emit` feeds the Go interop gate.

## Honesty, built in

No unix time → no readings (the stand or NTP must set it). No current
epoch → no readings. A detached board's frames are refused by every
member even if it still holds an old key. Nothing is ever simulated
unless the driver says so.

## Quick start: LilyGo T3-S3 + DHT22 (`examples/ClimateDHT`)

One command from sketch to a running board — build, flash, serial monitor:

```bash
sdk/instrument-arduino/qi-flash.sh ClimateDHT
```

(`PORT=/dev/cu.usbmodemXXXX` to force a port; second argument picks the
env, e.g. `heltec_wifi_lora_32_V3`; `--no-monitor` to skip the monitor.)
Wiring: DHT22 DATA → GPIO 38 (`-DDHT_PIN` in platformio.ini), VCC 3V3,
GND. The board's OLED shows the quiet.space mark at boot, then the two
readings and the instrument's state (waiting for owner → enrolling… →
in the space).
