#include <unity.h>

#include "edge_queue.h"
#include "lockout.h"

void setUp(void) {}
void tearDown(void) {}

// --- Lockout: first-edge-wins debounce --------------------------------

static void test_lockout_first_edge_always_accepted(void) {
  Lockout l;
  l.setWindowMs(10000);
  TEST_ASSERT_TRUE(l.accept(0));
}

static void test_lockout_rejects_edge_inside_window(void) {
  Lockout l;
  l.setWindowMs(10000);
  TEST_ASSERT_TRUE(l.accept(1000000));           // t=1s, accepted
  TEST_ASSERT_FALSE(l.accept(1000000 + 5000000)); // t=6s, still inside 10s window
}

static void test_lockout_accepts_edge_at_exact_window_boundary(void) {
  Lockout l;
  l.setWindowMs(10000);
  TEST_ASSERT_TRUE(l.accept(0));
  TEST_ASSERT_TRUE(l.accept(10000LL * 1000)); // exactly windowMs later -> >=, accepted
}

static void test_lockout_accepts_edge_after_window(void) {
  Lockout l;
  l.setWindowMs(10000);
  TEST_ASSERT_TRUE(l.accept(0));
  TEST_ASSERT_TRUE(l.accept(11LL * 1000 * 1000)); // 11s later
}

static void test_lockout_reset_reopens_first_edge(void) {
  Lockout l;
  l.setWindowMs(10000);
  TEST_ASSERT_TRUE(l.accept(0));
  TEST_ASSERT_FALSE(l.accept(100000)); // inside window
  l.reset();
  TEST_ASSERT_TRUE(l.accept(200000)); // treated as first edge again
}

static void test_lockout_window_getter_setter(void) {
  Lockout l;
  l.setWindowMs(5000);
  TEST_ASSERT_EQUAL_UINT32(5000, l.windowMs());
}

// --- EdgeQueue: SPSC ring buffer ---------------------------------------

static void test_edge_queue_push_pop_fifo_order(void) {
  EdgeQueue<8> q;
  TEST_ASSERT_TRUE(q.pushFromIsr(100));
  TEST_ASSERT_TRUE(q.pushFromIsr(200));
  int64_t v = 0;
  TEST_ASSERT_TRUE(q.pop(&v));
  TEST_ASSERT_EQUAL_INT64(100, v);
  TEST_ASSERT_TRUE(q.pop(&v));
  TEST_ASSERT_EQUAL_INT64(200, v);
}

static void test_edge_queue_pop_empty_returns_false(void) {
  EdgeQueue<8> q;
  int64_t v = 0;
  TEST_ASSERT_FALSE(q.pop(&v));
}

static void test_edge_queue_full_drops_and_counts(void) {
  EdgeQueue<4> q;  // capacity is N-1 = 3 usable slots
  TEST_ASSERT_TRUE(q.pushFromIsr(1));
  TEST_ASSERT_TRUE(q.pushFromIsr(2));
  TEST_ASSERT_TRUE(q.pushFromIsr(3));
  TEST_ASSERT_FALSE(q.pushFromIsr(4));  // full
  TEST_ASSERT_EQUAL_UINT32(1, q.dropped());
}

static void test_edge_queue_survives_multiple_burst_and_multiple_edges(void) {
  // Regression check for the original single-slot design, which silently
  // overwrote a pending edge if a second one arrived before loop() drained
  // it. A queue depth > 1 must retain both edges.
  EdgeQueue<8> q;
  TEST_ASSERT_TRUE(q.pushFromIsr(10));
  TEST_ASSERT_TRUE(q.pushFromIsr(20));
  int64_t v = 0;
  TEST_ASSERT_TRUE(q.pop(&v));
  TEST_ASSERT_EQUAL_INT64(10, v);
  TEST_ASSERT_TRUE(q.pop(&v));
  TEST_ASSERT_EQUAL_INT64(20, v);
  TEST_ASSERT_FALSE(q.pop(&v));
}

int main(int argc, char **argv) {
  UNITY_BEGIN();
  RUN_TEST(test_lockout_first_edge_always_accepted);
  RUN_TEST(test_lockout_rejects_edge_inside_window);
  RUN_TEST(test_lockout_accepts_edge_at_exact_window_boundary);
  RUN_TEST(test_lockout_accepts_edge_after_window);
  RUN_TEST(test_lockout_reset_reopens_first_edge);
  RUN_TEST(test_lockout_window_getter_setter);
  RUN_TEST(test_edge_queue_push_pop_fifo_order);
  RUN_TEST(test_edge_queue_pop_empty_returns_false);
  RUN_TEST(test_edge_queue_full_drops_and_counts);
  RUN_TEST(test_edge_queue_survives_multiple_burst_and_multiple_edges);
  return UNITY_END();
}
