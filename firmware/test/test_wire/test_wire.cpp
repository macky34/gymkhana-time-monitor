#include <unity.h>

#include <cstring>

#include "wire.h"

void setUp(void) {}
void tearDown(void) {}

// Byte-for-byte compatibility with the original String-concatenation
// implementation in main.cpp is the safety net for the rest of this
// refactor: the server's dedup/pairing logic depends on this exact wire
// format (see internal/timing/timing.go).
static void test_build_trigger_matches_original_format(void) {
  char buf[160];
  size_t n = wire::buildTrigger(buf, sizeof(buf), wire::Role::Start, 123, 1, 456);
  TEST_ASSERT_EQUAL_STRING(
      "{\"type\":\"trigger\",\"sensor_id\":\"start\",\"boot_id\":123,"
      "\"seq\":1,\"timestamp_us\":456}",
      buf);
  TEST_ASSERT_EQUAL_size_t(strlen(buf), n);
}

static void test_build_trigger_goal_role(void) {
  char buf[160];
  wire::buildTrigger(buf, sizeof(buf), wire::Role::Goal, 4294967295u, 999,
                      1720000000000000LL);
  TEST_ASSERT_EQUAL_STRING(
      "{\"type\":\"trigger\",\"sensor_id\":\"goal\",\"boot_id\":4294967295,"
      "\"seq\":999,\"timestamp_us\":1720000000000000}",
      buf);
}

static void test_build_trigger_buffer_too_small_returns_zero(void) {
  char buf[10];
  size_t n = wire::buildTrigger(buf, sizeof(buf), wire::Role::Start, 1, 1, 1);
  TEST_ASSERT_EQUAL_size_t(0, n);
}

static void test_build_heartbeat_matches_original_format(void) {
  char buf[160];
  size_t n = wire::buildHeartbeat(buf, sizeof(buf), wire::Role::Goal, 123, 42, 0.0);
  TEST_ASSERT_EQUAL_STRING(
      "{\"type\":\"hb\",\"sensor_id\":\"goal\",\"boot_id\":123,"
      "\"seq\":42,\"ntp_offset_ms\":0.0}",
      buf);
  TEST_ASSERT_EQUAL_size_t(strlen(buf), n);
}

static void test_build_heartbeat_nonzero_offset(void) {
  char buf[160];
  wire::buildHeartbeat(buf, sizeof(buf), wire::Role::Start, 1, 1, 12.345);
  // One decimal place, matching the original literal format.
  TEST_ASSERT_EQUAL_STRING(
      "{\"type\":\"hb\",\"sensor_id\":\"start\",\"boot_id\":1,"
      "\"seq\":1,\"ntp_offset_ms\":12.3}",
      buf);
}

static void test_parse_lockout_ms_basic(void) {
  uint32_t ms = 0;
  const char *body = "{\"lockout_sec\":10}";
  TEST_ASSERT_TRUE(wire::parseLockoutMs(body, strlen(body), &ms));
  TEST_ASSERT_EQUAL_UINT32(10000, ms);
}

static void test_parse_lockout_ms_with_type_field_and_fraction(void) {
  uint32_t ms = 0;
  const char *body = "{\"type\":\"config\",\"lockout_sec\":5.5}";
  TEST_ASSERT_TRUE(wire::parseLockoutMs(body, strlen(body), &ms));
  TEST_ASSERT_EQUAL_UINT32(5500, ms);
}

static void test_parse_lockout_ms_rejects_zero(void) {
  uint32_t ms = 999;
  const char *body = "{\"lockout_sec\":0}";
  TEST_ASSERT_FALSE(wire::parseLockoutMs(body, strlen(body), &ms));
  TEST_ASSERT_EQUAL_UINT32(999, ms);  // untouched
}

static void test_parse_lockout_ms_rejects_negative(void) {
  uint32_t ms = 999;
  const char *body = "{\"lockout_sec\":-5}";
  TEST_ASSERT_FALSE(wire::parseLockoutMs(body, strlen(body), &ms));
}

static void test_parse_lockout_ms_missing_field(void) {
  uint32_t ms = 999;
  const char *body = "{\"type\":\"config\"}";
  TEST_ASSERT_FALSE(wire::parseLockoutMs(body, strlen(body), &ms));
}

static void test_parse_lockout_ms_not_null_terminated(void) {
  // Simulate a UDP recv buffer where len is the byte count, not a
  // NUL-terminated C string (the actual on-the-wire shape from udp.read()).
  char raw[] = "{\"lockout_sec\":7}TRAILING_GARBAGE";
  uint32_t ms = 0;
  TEST_ASSERT_TRUE(wire::parseLockoutMs(raw, 17, &ms));  // len up to and including the '}'
  TEST_ASSERT_EQUAL_UINT32(7000, ms);
}

static void test_looks_like_server_json(void) {
  TEST_ASSERT_TRUE(wire::looksLikeServerJson("{\"a\":1}", 7));
  TEST_ASSERT_FALSE(wire::looksLikeServerJson("not json", 8));
  TEST_ASSERT_FALSE(wire::looksLikeServerJson("", 0));
}

int main(int argc, char **argv) {
  UNITY_BEGIN();
  RUN_TEST(test_build_trigger_matches_original_format);
  RUN_TEST(test_build_trigger_goal_role);
  RUN_TEST(test_build_trigger_buffer_too_small_returns_zero);
  RUN_TEST(test_build_heartbeat_matches_original_format);
  RUN_TEST(test_build_heartbeat_nonzero_offset);
  RUN_TEST(test_parse_lockout_ms_basic);
  RUN_TEST(test_parse_lockout_ms_with_type_field_and_fraction);
  RUN_TEST(test_parse_lockout_ms_rejects_zero);
  RUN_TEST(test_parse_lockout_ms_rejects_negative);
  RUN_TEST(test_parse_lockout_ms_missing_field);
  RUN_TEST(test_parse_lockout_ms_not_null_terminated);
  RUN_TEST(test_looks_like_server_json);
  return UNITY_END();
}
