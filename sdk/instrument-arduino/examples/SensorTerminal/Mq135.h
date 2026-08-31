// SP: QI-G0 — MQ135 math that refuses to name ppm.
//
// An MQ135 without a per-unit calibration is not a CO2 meter and not an
// AQI meter — it is a heated resistor whose ratio Rs/R0 moves with the
// air. That ratio is the HONEST channel: raw, unitless-but-named, and
// never dressed up as a concentration. Calibration that does not exist
// is not declared (ADR-026 honesty rules; the wave's law #2).
//
// Arduino-free on purpose: this header is pure arithmetic so the native
// test runner can hold it to its claims — the same discipline the C core
// lives under (cmake+ctest), applied to the example's own math.
#pragma once
#include <math.h>
#include <stdint.h>

// Rs/R0 from a raw ADC reading against the clean-air anchor reading.
//
// Both readings are the SAME divider on the SAME ADC, so every constant
// of the divider and the supply cancels out of the ratio:
//   Rs/R0 = (adcClean / adcRaw) * (FS - adcRaw) / (FS - adcClean)
// where FS is ADC full scale. NaN is the honest answer for a reading
// the math cannot stand on: rail values (0 or full scale) mean a wiring
// fault or a missing sensor, and a fabricated number would travel as a
// fact. NaN means the tick publishes nothing (the SDK's contract).
static inline float mq135Ratio(uint16_t adcRaw, uint16_t adcCleanAir,
                               uint16_t adcFullScale) {
  if (adcFullScale == 0 || adcCleanAir == 0 || adcCleanAir >= adcFullScale) {
    return NAN;  // anchor itself is unusable — nothing honest to say
  }
  if (adcRaw == 0 || adcRaw >= adcFullScale) {
    return NAN;  // railed reading: fault, not data
  }
  const float fs = (float)adcFullScale;
  const float rs = (fs - (float)adcRaw) / (float)adcRaw;
  const float r0 = (fs - (float)adcCleanAir) / (float)adcCleanAir;
  return rs / r0;
}

// The warm gate: the MQ heater needs minutes before the ratio means
// anything, and "warming" must never leak as a low reading.
//
// Clock discipline: the gate latches warm ONCE the clock has advanced
// warmAfterSec past start. A clock that jumps BACKWARDS (NTP step, a
// bearer handing down a corrected time) un-latches nothing and
// re-latches nothing early — time only moves the gate forward, the same
// law the epoch floor lives by. A gate that flapped on a time step
// would publish a cold reading with a straight face.
struct WarmGate {
  uint32_t startedSec = 0;   // 0 = not started
  uint32_t warmAfterSec = 180;
  bool latched = false;

  void start(uint32_t nowSec) {
    if (startedSec == 0) startedSec = nowSec;
  }

  bool warm(uint32_t nowSec) {
    if (latched) return true;              // forward-only: never un-warms
    if (startedSec == 0) return false;     // never started
    if (nowSec < startedSec) return false; // clock went backwards: hold
    if (nowSec - startedSec >= warmAfterSec) latched = true;
    return latched;
  }
};
