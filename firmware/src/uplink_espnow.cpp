#include "uplink_espnow.h"

#include <Arduino.h>
#include <WiFi.h>
#include <esp_wifi.h>
#include <cstring>

#include "config.h"
#include "link_frame.h"

void UplinkEspNow::begin() {
  WiFi.mode(WIFI_STA);
  WiFi.persistent(false);
  WiFi.setAutoReconnect(false);
  WiFi.disconnect(false, true);  // kill any saved-credential auto-connect
  WiFi.setSleep(false);

  currentChannel_ = 1;
  esp_wifi_set_channel(currentChannel_, WIFI_SECOND_CHAN_NONE);
  channelSwitchMs_ = 0;

  radio_.begin();
  haveHost_ = false;
  roleCollision_ = false;
  state_ = UplinkState::Connecting;
}

void UplinkEspNow::sendDiscover() {
  uint8_t buf[8];
  size_t n = linkproto::encodeDiscover(buf, sizeof(buf), role_);
  if (n == 0) return;
  radio_.sendBroadcast(buf, n);
}

void UplinkEspNow::sweepChannel(uint32_t nowMs) {
  if (channelSwitchMs_ != 0 && nowMs - channelSwitchMs_ < kChannelDwellMs) return;
  channelSwitchMs_ = nowMs;
  sendDiscover();
  currentChannel_ = (currentChannel_ % 13) + 1;  // wrap 1..13
  esp_wifi_set_channel(currentChannel_, WIFI_SECOND_CHAN_NONE);
}

void UplinkEspNow::handlePacket(const EspNowPacket &pkt, uint32_t nowMs) {
  linkproto::FrameType type;
  if (!linkproto::peekType(pkt.data, pkt.len, &type)) return;

  switch (type) {
    case linkproto::FrameType::Announce: {
      linkproto::AnnounceInfo info;
      if (!linkproto::decodeAnnounce(pkt.data, pkt.len, &info)) return;

      if (info.hostRole == role_) {
        if (!roleCollision_) {
          Serial.println("[espnow] ROLE COLLISION: host announced the same "
                          "role as this client, refusing to link");
        }
        roleCollision_ = true;
        return;
      }
      roleCollision_ = false;

      if (!haveHost_) {
        memcpy(hostMac_, pkt.mac, 6);
        radio_.addPeer(hostMac_);
        haveHost_ = true;
        Serial.println("[espnow] host found");
      }
      lastAnnounceRxMs_ = nowMs;
      hostUplinkUp_ = info.uplinkUp;
      lastRssi_ = pkt.rssi;
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

bool UplinkEspNow::linkUsable() const {
  return haveHost_ && !roleCollision_ && hostUplinkUp_;
}

void UplinkEspNow::loop(uint32_t nowMs) {
  EspNowPacket pkt;
  while (radio_.poll(&pkt)) {
    handlePacket(pkt, nowMs);
  }

  if (!haveHost_) {
    state_ = UplinkState::Connecting;
    sweepChannel(nowMs);
    pollPendingTrigger(nowMs);
    return;
  }

  if (nowMs - lastAnnounceRxMs_ > kAnnounceTimeoutMs) {
    Serial.println("[espnow] host announce timed out, re-sweeping");
    haveHost_ = false;
    roleCollision_ = false;
    state_ = UplinkState::Connecting;
    pollPendingTrigger(nowMs);
    return;
  }

  state_ = roleCollision_ ? UplinkState::Failed : UplinkState::Ready;
  pollPendingTrigger(nowMs);
}

bool UplinkEspNow::sendRelay(const char *payload, size_t len) {
  uint8_t buf[kEspNowMaxPayload];
  size_t n = linkproto::encodeRelay(buf, sizeof(buf), payload, len);
  if (n == 0) return false;
  return radio_.send(hostMac_, buf, n);
}

void UplinkEspNow::pollPendingTrigger(uint32_t nowMs) {
  if (!pendingTrigger_.active) return;
  if (linkUsable()) {
    sendRelay(pendingTrigger_.payload, pendingTrigger_.len);
    pendingTrigger_.active = false;
    return;
  }
  if (nowMs - pendingTrigger_.queuedAtMs > kTriggerRetryMs) {
    Serial.println("[espnow] trigger dropped: no usable uplink after 3s retry");
    pendingTrigger_.active = false;
  }
}

bool UplinkEspNow::sendToServer(const char *payload, size_t len, Redundancy r) {
  if (linkUsable()) return sendRelay(payload, len);

  // hb: don't queue, the next one is 5s away anyway.
  if (r != Redundancy::Burst3) return false;

  // trigger: queue for up to kTriggerRetryMs rather than dropping it
  // immediately, in case the host's uplink recovers in time.
  if (len >= sizeof(pendingTrigger_.payload)) return false;
  memcpy(pendingTrigger_.payload, payload, len);
  pendingTrigger_.len = len;
  pendingTrigger_.queuedAtMs = millis();
  pendingTrigger_.active = true;
  return true;
}
