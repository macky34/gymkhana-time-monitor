#include "link_frame.h"

#include <cstring>

namespace linkproto {

namespace {
constexpr size_t kHeaderLen = 1;

bool typeMatches(const uint8_t *data, size_t len, FrameType want) {
  return len >= kHeaderLen && data[0] == static_cast<uint8_t>(want);
}
}  // namespace

bool peekType(const uint8_t *data, size_t len, FrameType *outType) {
  if (len < kHeaderLen) return false;
  *outType = static_cast<FrameType>(data[0]);
  return true;
}

size_t encodeDiscover(uint8_t *out, size_t cap, wire::Role clientRole) {
  if (cap < 2) return 0;
  out[0] = static_cast<uint8_t>(FrameType::Discover);
  out[1] = static_cast<uint8_t>(clientRole);
  return 2;
}

bool decodeDiscover(const uint8_t *data, size_t len, wire::Role *outRole) {
  if (len < 2 || !typeMatches(data, len, FrameType::Discover)) return false;
  *outRole = static_cast<wire::Role>(data[1]);
  return true;
}

size_t encodeAnnounce(uint8_t *out, size_t cap, const AnnounceInfo &info) {
  if (cap < 4) return 0;
  out[0] = static_cast<uint8_t>(FrameType::Announce);
  out[1] = info.channel;
  out[2] = static_cast<uint8_t>(info.hostRole);
  out[3] = info.uplinkUp ? 1 : 0;
  return 4;
}

bool decodeAnnounce(const uint8_t *data, size_t len, AnnounceInfo *out) {
  if (len < 4 || !typeMatches(data, len, FrameType::Announce)) return false;
  out->channel = data[1];
  out->hostRole = static_cast<wire::Role>(data[2]);
  out->uplinkUp = data[3] != 0;
  return true;
}

size_t encodeRelay(uint8_t *out, size_t cap, const char *payload, size_t payloadLen) {
  if (cap < kHeaderLen + payloadLen) return 0;
  out[0] = static_cast<uint8_t>(FrameType::Relay);
  memcpy(out + kHeaderLen, payload, payloadLen);
  return kHeaderLen + payloadLen;
}

size_t decodeRelay(const uint8_t *data, size_t len, const char **outPayload) {
  if (!typeMatches(data, len, FrameType::Relay)) return 0;
  *outPayload = reinterpret_cast<const char *>(data + kHeaderLen);
  return len - kHeaderLen;
}

size_t encodeConfig(uint8_t *out, size_t cap, const char *payload, size_t payloadLen) {
  if (cap < kHeaderLen + payloadLen) return 0;
  out[0] = static_cast<uint8_t>(FrameType::Config);
  memcpy(out + kHeaderLen, payload, payloadLen);
  return kHeaderLen + payloadLen;
}

size_t decodeConfig(const uint8_t *data, size_t len, const char **outPayload) {
  if (!typeMatches(data, len, FrameType::Config)) return 0;
  *outPayload = reinterpret_cast<const char *>(data + kHeaderLen);
  return len - kHeaderLen;
}

}  // namespace linkproto
