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

size_t encodeTimeReq(uint8_t *out, size_t cap, int64_t t1) {
  if (cap < kHeaderLen + sizeof(int64_t)) return 0;
  out[0] = static_cast<uint8_t>(FrameType::TimeReq);
  memcpy(out + kHeaderLen, &t1, sizeof(int64_t));
  return kHeaderLen + sizeof(int64_t);
}

bool decodeTimeReq(const uint8_t *data, size_t len, int64_t *outT1) {
  if (len < kHeaderLen + sizeof(int64_t) || !typeMatches(data, len, FrameType::TimeReq)) {
    return false;
  }
  memcpy(outT1, data + kHeaderLen, sizeof(int64_t));
  return true;
}

size_t encodeTimeResp(uint8_t *out, size_t cap, const TimeRespInfo &info) {
  size_t need = kHeaderLen + 3 * sizeof(int64_t);
  if (cap < need) return 0;
  out[0] = static_cast<uint8_t>(FrameType::TimeResp);
  size_t off = kHeaderLen;
  memcpy(out + off, &info.t1, sizeof(int64_t));
  off += sizeof(int64_t);
  memcpy(out + off, &info.t2rx, sizeof(int64_t));
  off += sizeof(int64_t);
  memcpy(out + off, &info.t2tx, sizeof(int64_t));
  return need;
}

bool decodeTimeResp(const uint8_t *data, size_t len, TimeRespInfo *out) {
  size_t need = kHeaderLen + 3 * sizeof(int64_t);
  if (len < need || !typeMatches(data, len, FrameType::TimeResp)) return false;
  size_t off = kHeaderLen;
  memcpy(&out->t1, data + off, sizeof(int64_t));
  off += sizeof(int64_t);
  memcpy(&out->t2rx, data + off, sizeof(int64_t));
  off += sizeof(int64_t);
  memcpy(&out->t2tx, data + off, sizeof(int64_t));
  return true;
}

}  // namespace linkproto
