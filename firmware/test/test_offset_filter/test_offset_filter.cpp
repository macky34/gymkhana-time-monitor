#include <unity.h>

#include "offset_filter.h"

void setUp(void) {}
void tearDown(void) {}

// t1=1000, host receives at t2rx=1000 and replies immediately (t2tx=1000),
// client sees the reply at t3=1010 (all 10us of round-trip delay lands on
// the return leg). offset = ((1000-1000)+(1000-1010))/2 = -5;
// delay = (1010-1000)-(1000-1000) = 10.
static void test_add_sample_symmetric_zero_offset(void) {
  OffsetFilter f;
  TEST_ASSERT_TRUE(f.addSample(1000, 1000, 1000, 1010));
  TEST_ASSERT_TRUE(f.hasSample());
  TEST_ASSERT_EQUAL_INT64(-5, f.bestOffsetUs());
  TEST_ASSERT_EQUAL_INT64(10, f.bestDelayUs());
}

// Host's wall clock is 500us ahead of the client's mono-as-if-wall
// reference: t1=1000 (client mono), t2rx=1500 (host wall), t2tx=1505 (host
// spent 5us before replying), t3=1015 (client mono, 15us round trip minus
// host processing). offset = ((1500-1000)+(1505-1015))/2 = (500+490)/2 = 495.
// delay = (1015-1000)-(1505-1500) = 15-5 = 10.
static void test_add_sample_computes_offset_and_delay(void) {
  OffsetFilter f;
  TEST_ASSERT_TRUE(f.addSample(1000, 1500, 1505, 1015));
  TEST_ASSERT_EQUAL_INT64(495, f.bestOffsetUs());
  TEST_ASSERT_EQUAL_INT64(10, f.bestDelayUs());
}

static void test_add_sample_rejects_delay_over_20ms(void) {
  OffsetFilter f;
  // delay = (t3-t1)-(t2tx-t2rx) = (1000+30000) - 0 = 30000us > 20000us cap
  bool accepted = f.addSample(0, 100, 100, 30000);
  TEST_ASSERT_FALSE(accepted);
  TEST_ASSERT_FALSE(f.hasSample());
}

static void test_add_sample_rejects_negative_delay(void) {
  OffsetFilter f;
  // A malformed/adversarial sample where the round trip appears negative.
  bool accepted = f.addSample(1000, 500, 500, 500);
  TEST_ASSERT_FALSE(accepted);
  TEST_ASSERT_FALSE(f.hasSample());
}

static void test_minimum_filter_keeps_lowest_delay_sample(void) {
  OffsetFilter f;
  TEST_ASSERT_TRUE(f.addSample(0, 1000, 1000, 100));  // delay=100, offset=950
  TEST_ASSERT_TRUE(f.addSample(0, 2000, 2000, 20));   // delay=20, offset=1990 (best: lowest delay)
  TEST_ASSERT_TRUE(f.addSample(0, 3000, 3000, 50));   // delay=50, offset=2975 (worse than best)
  TEST_ASSERT_EQUAL_INT64(20, f.bestDelayUs());
  TEST_ASSERT_EQUAL_INT64(1990, f.bestOffsetUs());
}

static void test_reset_clears_best_sample(void) {
  OffsetFilter f;
  f.addSample(0, 1000, 1000, 10);
  TEST_ASSERT_TRUE(f.hasSample());
  f.reset();
  TEST_ASSERT_FALSE(f.hasSample());
  TEST_ASSERT_EQUAL_INT64(0, f.bestOffsetUs());
  TEST_ASSERT_EQUAL_INT64(0, f.bestDelayUs());
}

int main(int argc, char **argv) {
  UNITY_BEGIN();
  RUN_TEST(test_add_sample_symmetric_zero_offset);
  RUN_TEST(test_add_sample_computes_offset_and_delay);
  RUN_TEST(test_add_sample_rejects_delay_over_20ms);
  RUN_TEST(test_add_sample_rejects_negative_delay);
  RUN_TEST(test_minimum_filter_keeps_lowest_delay_sample);
  RUN_TEST(test_reset_clears_best_sample);
  return UNITY_END();
}
