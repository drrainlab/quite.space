// QI-G0 — the SensorTerminal's pure math, held to its claims off-hardware.
//
// The Arduino scheduling layer has no test runner (stated in the wave's
// coverage note); what CAN be tested without a board is everything that
// is arithmetic or a state machine, and this suite is where those claims
// live — the native cousin of the C core's cmake+ctest discipline.
#include <unity.h>
#include <math.h>

#include "../../Mq135.h"

void setUp(void) {}
void tearDown(void) {}

// ---- mq135Ratio: the honest ratio -------------------------------------

static void ratio_at_the_clean_air_anchor_is_one(void) {
  // Reading equal to the anchor: Rs == R0 by construction.
  TEST_ASSERT_FLOAT_WITHIN(0.001f, 1.0f, mq135Ratio(1800, 1800, 4095));
}

static void ratio_moves_the_right_way(void) {
  // MQ135's Rs DROPS in dirtier air; a higher divider reading means a
  // lower Rs, so ratio < 1. The sign of the story must never flip.
  float dirtier = mq135Ratio(2400, 1800, 4095);
  float cleaner = mq135Ratio(1200, 1800, 4095);
  TEST_ASSERT_TRUE(dirtier < 1.0f);
  TEST_ASSERT_TRUE(cleaner > 1.0f);
}

static void railed_readings_are_nan_not_numbers(void) {
  TEST_ASSERT_TRUE(isnan(mq135Ratio(0, 1800, 4095)));     // open circuit
  TEST_ASSERT_TRUE(isnan(mq135Ratio(4095, 1800, 4095)));  // short / rail
  TEST_ASSERT_TRUE(isnan(mq135Ratio(4200, 1800, 4095)));  // out of range
}

static void a_broken_anchor_silences_the_channel(void) {
  TEST_ASSERT_TRUE(isnan(mq135Ratio(1800, 0, 4095)));
  TEST_ASSERT_TRUE(isnan(mq135Ratio(1800, 4095, 4095)));
  TEST_ASSERT_TRUE(isnan(mq135Ratio(1800, 1800, 0)));
}

// ---- WarmGate: warming never leaks, time only moves forward ------------

static void cold_before_the_threshold_warm_at_it(void) {
  WarmGate g;
  g.warmAfterSec = 180;
  g.start(1000);
  TEST_ASSERT_FALSE(g.warm(1000));
  TEST_ASSERT_FALSE(g.warm(1179));
  TEST_ASSERT_TRUE(g.warm(1180));
}

static void an_unstarted_gate_is_cold(void) {
  WarmGate g;
  TEST_ASSERT_FALSE(g.warm(999999));
}

static void a_backwards_clock_neither_warms_nor_unwarms(void) {
  WarmGate g;
  g.warmAfterSec = 180;
  g.start(1000);
  // The clock steps back below start: the gate holds cold — it does not
  // compute a huge unsigned delta and lie forward.
  TEST_ASSERT_FALSE(g.warm(500));
  // Once genuinely warm, a backwards step must NOT un-warm: members saw
  // warm=true, and honesty does not flap on an NTP step.
  TEST_ASSERT_TRUE(g.warm(1200));
  TEST_ASSERT_TRUE(g.warm(500));
}

static void start_is_idempotent(void) {
  WarmGate g;
  g.warmAfterSec = 180;
  g.start(1000);
  g.start(5000);  // a second start must not reset the burn-in clock
  TEST_ASSERT_TRUE(g.warm(1180));
}

int main(int, char **) {
  UNITY_BEGIN();
  RUN_TEST(ratio_at_the_clean_air_anchor_is_one);
  RUN_TEST(ratio_moves_the_right_way);
  RUN_TEST(railed_readings_are_nan_not_numbers);
  RUN_TEST(a_broken_anchor_silences_the_channel);
  RUN_TEST(cold_before_the_threshold_warm_at_it);
  RUN_TEST(an_unstarted_gate_is_cold);
  RUN_TEST(a_backwards_clock_neither_warms_nor_unwarms);
  RUN_TEST(start_is_idempotent);
  return UNITY_END();
}
