#include "espnow_bridge.h"

#include <Arduino.h>
#include <WiFi.h>
#include <cstring>
#include <sys/time.h>

#include "link_frame.h"

namespace {
constexpr uint32_t kAnnounceIntervalMs = 2000;

int64_t nowWallUs() {
  struct timeval tv;
  gettimeofday(&tv, nullptr);
  return (int64_t)tv.tv_sec * 1000000LL + tv.tv_usec;
}
}  // namespace

void EspNowBridge::begin(wire::Role hostRole, RelayToServerFn relayFn, void *relayCtx) {
  hostRole_ = hostRole;
  relayFn_ = relayFn;
  relayCtx_ = relayCtx;
  radio_.begin();
  lastAnnounceMs_ = 0;
  haveClient_ = false;
}

void EspNowBridge::setUplinkUp(bool up) { uplinkUp_ = up; }

void EspNowBridge::sendAnnounce(const uint8_t *toMac) {
  linkproto::AnnounceInfo info{(uint8_t)WiFi.channel(), hostRole_, uplinkUp_};
  uint8_t buf[8];
  size_t n = linkproto::encodeAnnounce(buf, sizeof(buf), info);
  if (n == 0) return;
  radio_.send(toMac, buf, n);
}

void EspNowBridge::handlePacket(const EspNowPacket &pkt) {
  linkproto::FrameType type;
  if (!linkproto::peekType(pkt.data, pkt.len, &type)) return;

  switch (type) {
    case linkproto::FrameType::Discover: {
      wire::Role clientRole;
      if (!linkproto::decodeDiscover(pkt.data, pkt.len, &clientRole)) return;
      radio_.addPeer(pkt.mac);
      memcpy(lastClientMac_, pkt.mac, 6);
      haveClient_ = true;
      sendAnnounce(pkt.mac);
      Serial.println("[espnow] discover -> answered");
      break;
    }
    case linkproto::FrameType::Relay: {
      const char *payload = nullptr;
      size_t len = linkproto::decodeRelay(pkt.data, pkt.len, &payload);
      if (len == 0 || !relayFn_) return;
      radio_.addPeer(pkt.mac);
      memcpy(lastClientMac_, pkt.mac, 6);
      haveClient_ = true;
      bool ok = relayFn_(relayCtx_, payload, len);
      Serial.printf("[espnow] relay -> server: %.*s (%s)\n", (int)len, payload,
                    ok ? "ok" : "FAILED");
      break;
    }
    case linkproto::FrameType::TimeReq: {
      int64_t t1 = 0;
      if (!linkproto::decodeTimeReq(pkt.data, pkt.len, &t1)) return;

      // t2rx is captured in the recv callback (EspNowRadio), as close to
      // wire arrival as this stack gets; t2tx is right before the send.
      linkproto::TimeRespInfo resp{t1, pkt.rxTimestampUs, nowWallUs()};
      uint8_t buf[32];
      size_t n = linkproto::encodeTimeResp(buf, sizeof(buf), resp);
      if (n > 0) radio_.send(pkt.mac, buf, n);
      break;
    }
    default:
      break;
  }
}

void EspNowBridge::loop(uint32_t nowMs) {
  EspNowPacket pkt;
  while (radio_.poll(&pkt)) {
    handlePacket(pkt);
  }

  if (nowMs - lastAnnounceMs_ >= kAnnounceIntervalMs) {
    lastAnnounceMs_ = nowMs;
    sendAnnounce(kEspNowBroadcastMac);
  }
}

void EspNowBridge::forwardConfigToClient(const char *payload, size_t len) {
  if (!haveClient_) return;
  uint8_t buf[kEspNowMaxPayload];
  size_t n = linkproto::encodeConfig(buf, sizeof(buf), payload, len);
  if (n == 0) return;
  radio_.send(lastClientMac_, buf, n);
}
