#include <unity.h>

#include <cstring>

#include "link_frame.h"

void setUp(void) {}
void tearDown(void) {}

static void test_discover_round_trip(void) {
  uint8_t buf[8];
  size_t n = linkproto::encodeDiscover(buf, sizeof(buf), wire::Role::Goal);
  TEST_ASSERT_EQUAL_size_t(2, n);

  wire::Role role;
  TEST_ASSERT_TRUE(linkproto::decodeDiscover(buf, n, &role));
  TEST_ASSERT_EQUAL_INT((int)wire::Role::Goal, (int)role);
}

static void test_discover_buffer_too_small_returns_zero(void) {
  uint8_t buf[1];
  size_t n = linkproto::encodeDiscover(buf, sizeof(buf), wire::Role::Start);
  TEST_ASSERT_EQUAL_size_t(0, n);
}

static void test_announce_round_trip(void) {
  uint8_t buf[8];
  linkproto::AnnounceInfo info{6, wire::Role::Start, true};
  size_t n = linkproto::encodeAnnounce(buf, sizeof(buf), info);
  TEST_ASSERT_EQUAL_size_t(4, n);

  linkproto::AnnounceInfo out{};
  TEST_ASSERT_TRUE(linkproto::decodeAnnounce(buf, n, &out));
  TEST_ASSERT_EQUAL_UINT8(6, out.channel);
  TEST_ASSERT_EQUAL_INT((int)wire::Role::Start, (int)out.hostRole);
  TEST_ASSERT_TRUE(out.uplinkUp);
}

static void test_announce_uplink_down(void) {
  uint8_t buf[8];
  linkproto::AnnounceInfo info{1, wire::Role::Goal, false};
  linkproto::encodeAnnounce(buf, sizeof(buf), info);

  linkproto::AnnounceInfo out{};
  TEST_ASSERT_TRUE(linkproto::decodeAnnounce(buf, 4, &out));
  TEST_ASSERT_FALSE(out.uplinkUp);
}

static void test_relay_round_trip_wraps_payload_verbatim(void) {
  const char *payload = "{\"type\":\"hb\",\"sensor_id\":\"start\"}";
  size_t payloadLen = strlen(payload);
  uint8_t buf[64];
  size_t n = linkproto::encodeRelay(buf, sizeof(buf), payload, payloadLen);
  TEST_ASSERT_EQUAL_size_t(1 + payloadLen, n);

  const char *out = nullptr;
  size_t outLen = linkproto::decodeRelay(buf, n, &out);
  TEST_ASSERT_EQUAL_size_t(payloadLen, outLen);
  TEST_ASSERT_EQUAL_MEMORY(payload, out, payloadLen);
}

static void test_relay_buffer_too_small_returns_zero(void) {
  uint8_t buf[4];
  size_t n = linkproto::encodeRelay(buf, sizeof(buf), "abcdefgh", 8);
  TEST_ASSERT_EQUAL_size_t(0, n);
}

static void test_decode_relay_rejects_wrong_type(void) {
  uint8_t buf[8];
  linkproto::encodeConfig(buf, sizeof(buf), "x", 1);

  const char *out = nullptr;
  TEST_ASSERT_EQUAL_size_t(0, linkproto::decodeRelay(buf, 2, &out));
}

static void test_config_round_trip(void) {
  const char *payload = "{\"type\":\"config\",\"lockout_sec\":10}";
  size_t payloadLen = strlen(payload);
  uint8_t buf[64];
  size_t n = linkproto::encodeConfig(buf, sizeof(buf), payload, payloadLen);

  const char *out = nullptr;
  size_t outLen = linkproto::decodeConfig(buf, n, &out);
  TEST_ASSERT_EQUAL_size_t(payloadLen, outLen);
  TEST_ASSERT_EQUAL_MEMORY(payload, out, payloadLen);
}

static void test_peek_type(void) {
  uint8_t buf[8];
  linkproto::encodeAnnounce(buf, sizeof(buf), {1, wire::Role::Start, true});

  linkproto::FrameType t;
  TEST_ASSERT_TRUE(linkproto::peekType(buf, 4, &t));
  TEST_ASSERT_EQUAL_INT((int)linkproto::FrameType::Announce, (int)t);
}

static void test_peek_type_empty_buffer_returns_false(void) {
  linkproto::FrameType t;
  TEST_ASSERT_FALSE(linkproto::peekType(nullptr, 0, &t));
}

int main(int argc, char **argv) {
  UNITY_BEGIN();
  RUN_TEST(test_discover_round_trip);
  RUN_TEST(test_discover_buffer_too_small_returns_zero);
  RUN_TEST(test_announce_round_trip);
  RUN_TEST(test_announce_uplink_down);
  RUN_TEST(test_relay_round_trip_wraps_payload_verbatim);
  RUN_TEST(test_relay_buffer_too_small_returns_zero);
  RUN_TEST(test_decode_relay_rejects_wrong_type);
  RUN_TEST(test_config_round_trip);
  RUN_TEST(test_peek_type);
  RUN_TEST(test_peek_type_empty_buffer_returns_false);
  return UNITY_END();
}
