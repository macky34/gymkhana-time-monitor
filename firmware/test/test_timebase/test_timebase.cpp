#include <unity.h>

#include "timebase.h"

void setUp(void) {}
void tearDown(void) {}

static void test_timebase_unsynced_by_default(void) {
  TimeBase tb;
  TEST_ASSERT_FALSE(tb.synced());
}

static void test_timebase_synced_after_anchor(void) {
  TimeBase tb;
  tb.anchor(1000000, 500000);
  TEST_ASSERT_TRUE(tb.synced());
}

static void test_timebase_to_wall_us_matches_original_formula(void) {
  // wall = wallAnchor + (mono - monoAnchor), matching main.cpp's
  // edgeToWallUs().
  TimeBase tb;
  tb.anchor(/*wallUs=*/1700000000000000LL, /*monoUs=*/1000000);
  int64_t wall = tb.toWallUs(/*monoUs=*/1000000 + 250000);  // 250ms later
  TEST_ASSERT_EQUAL_INT64(1700000000000000LL + 250000, wall);
}

static void test_timebase_invalidate_clears_synced(void) {
  TimeBase tb;
  tb.anchor(1000000, 500000);
  TEST_ASSERT_TRUE(tb.synced());
  tb.invalidate();
  TEST_ASSERT_FALSE(tb.synced());
}

static void test_timebase_reanchor_updates_conversion(void) {
  TimeBase tb;
  tb.anchor(1000000, 0);
  TEST_ASSERT_EQUAL_INT64(1000000, tb.toWallUs(0));
  tb.anchor(5000000, 100);  // re-sync with a new anchor pair
  TEST_ASSERT_EQUAL_INT64(5000000, tb.toWallUs(100));
  TEST_ASSERT_EQUAL_INT64(5000100, tb.toWallUs(200));
}

static void test_timebase_reanchor_slewed_before_sync_hard_anchors(void) {
  // No prior anchor() call -- nothing to slew away from, so this should
  // behave exactly like anchor().
  TimeBase tb;
  tb.reanchorSlewed(1000000, 0);
  TEST_ASSERT_TRUE(tb.synced());
  TEST_ASSERT_EQUAL_INT64(1000000, tb.toWallUs(0));
}

static void test_timebase_reanchor_slewed_does_not_step(void) {
  TimeBase tb;
  tb.anchor(1000000, 0);
  // New sample says wall should be 9900us ahead of what the old anchor
  // predicts at monoUs=100.
  tb.reanchorSlewed(1010000, 100);
  TEST_ASSERT_EQUAL_INT64(1000100, tb.toWallUs(100));  // unchanged so far
}

static void test_timebase_reanchor_slewed_converges_gradually(void) {
  TimeBase tb;
  tb.anchor(1000000, 0);
  tb.reanchorSlewed(1010000, 100);  // 9900us to slew in, starting at mono=100

  // 1s later: capped at kMaxSlewPpm(500)*1s = 500us applied.
  TEST_ASSERT_EQUAL_INT64(2000600, tb.toWallUs(1000100));

  // 20s later: 500*20=10000us available, clamps at the full 9900us --
  // matching what a hard reanchor() at (1010000, 100) would have given.
  TEST_ASSERT_EQUAL_INT64(21010000, tb.toWallUs(20000100));
}

int main(int argc, char **argv) {
  UNITY_BEGIN();
  RUN_TEST(test_timebase_unsynced_by_default);
  RUN_TEST(test_timebase_synced_after_anchor);
  RUN_TEST(test_timebase_to_wall_us_matches_original_formula);
  RUN_TEST(test_timebase_invalidate_clears_synced);
  RUN_TEST(test_timebase_reanchor_updates_conversion);
  RUN_TEST(test_timebase_reanchor_slewed_before_sync_hard_anchors);
  RUN_TEST(test_timebase_reanchor_slewed_does_not_step);
  RUN_TEST(test_timebase_reanchor_slewed_converges_gradually);
  return UNITY_END();
}
