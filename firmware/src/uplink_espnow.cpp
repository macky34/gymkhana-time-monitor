#include "uplink_espnow.h"

#include <Arduino.h>
#include <WiFi.h>
#include <esp_wifi.h>
#include <cstring>

#include "config.h"
#include "link_frame.h"

namespace {
constexpr uint32_t kDiscoverIntervalMs = 2000;
constexpr uint32_t kAnnounceTimeoutMs = 10000;  // re-discover if the host goes quiet
}  // namespace

void UplinkEspNow::begin() {
  WiFi.mode(WIFI_STA);
  WiFi.persistent(false);
  WiFi.setAutoReconnect(false);
  WiFi.disconnect(false, true);  // kill any saved-credential auto-connect
  WiFi.setSleep(false);

  // Phase 3: fixed channel from config.h; Phase 4 adds the discovery sweep.
  esp_wifi_set_channel(ESPNOW_CHANNEL, WIFI_SECOND_CHAN_NONE);

  radio_.begin();
  haveHost_ = false;
  state_ = UplinkState::Connecting;
  lastDiscoverMs_ = 0;
}

void UplinkEspNow::sendDiscover() {
  uint8_t buf[8];
  size_t n = linkproto::encodeDiscover(buf, sizeof(buf), role_);
  if (n == 0) return;
  radio_.sendBroadcast(buf, n);
}

void UplinkEspNow::handlePacket(const EspNowPacket &pkt, uint32_t nowMs) {
  linkproto::FrameType type;
  if (!linkproto::peekType(pkt.data, pkt.len, &type)) return;

  switch (type) {
    case linkproto::FrameType::Announce: {
      linkproto::AnnounceInfo info;
      if (!linkproto::decodeAnnounce(pkt.data, pkt.len, &info)) return;
      if (!haveHost_) {
        memcpy(hostMac_, pkt.mac, 6);
        radio_.addPeer(hostMac_);
        haveHost_ = true;
        Serial.println("[espnow] host found");
      }
      lastAnnounceRxMs_ = nowMs;
      hostUplinkUp_ = info.uplinkUp;
      break;
    }
    case linkproto::FrameType::Config: {
      const char *payload = nullptr;
      size_t len = linkproto::decodeConfig(pkt.data, pkt.len, &payload);
      if (len == 0) return;
      uint32_t newMs = 0;
      if (wire::parseLockoutMs(payload, len, &newMs)) {
        lockoutMs_ = newMs;
      }
      break;
    }
    default:
      break;
  }
}

void UplinkEspNow::loop(uint32_t nowMs) {
  EspNowPacket pkt;
  while (radio_.poll(&pkt)) {
    handlePacket(pkt, nowMs);
  }

  if (!haveHost_) {
    state_ = UplinkState::Connecting;
    if (nowMs - lastDiscoverMs_ >= kDiscoverIntervalMs) {
      lastDiscoverMs_ = nowMs;
      sendDiscover();
    }
    return;
  }

  if (nowMs - lastAnnounceRxMs_ > kAnnounceTimeoutMs) {
    Serial.println("[espnow] host announce timed out, re-discovering");
    haveHost_ = false;
    state_ = UplinkState::Connecting;
    return;
  }

  state_ = UplinkState::Ready;
}

bool UplinkEspNow::sendToServer(const char *payload, size_t len, Redundancy r) {
  (void)r;  // Phase 3: hb only (sent with Once anyway); trigger relay and
            // burst retry over ESP-NOW land in Phase 4.
  if (!haveHost_) return false;
  uint8_t buf[kEspNowMaxPayload];
  size_t n = linkproto::encodeRelay(buf, sizeof(buf), payload, len);
  if (n == 0) return false;
  return radio_.send(hostMac_, buf, n);
}
