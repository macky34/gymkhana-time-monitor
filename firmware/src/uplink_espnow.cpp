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
#ifdef ESPNOW_LR_MODE
  if (!radio_.enableLongRange()) {
    Serial.println("[espnow] LR mode request failed, staying at normal rate");
  }
#endif
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
        radio_.addPeer(hostMac_, true);
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
    case linkproto::FrameType::TimeResp: {
      linkproto::TimeRespInfo info;
      if (!linkproto::decodeTimeResp(pkt.data, pkt.len, &info)) return;
      if (syncPhase_ != SyncPhase::Exchanging) return;

      int64_t t3 = esp_timer_get_time();
      offsetFilter_.addSample(info.t1, info.t2rx, info.t2tx, t3);

      syncExchangesDone_++;
      if (syncExchangesDone_ >= kSyncExchangeCount) {
        finishTimeSync(nowMs);
      } else {
        syncExchangeStartMs_ = nowMs;
        sendTimeReq(nowMs);
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
    // Without this, a role collision leaves state() stuck at Connecting forever.
    state_ = roleCollision_ ? UplinkState::Failed : UplinkState::Connecting;
    sweepChannel(nowMs);
    pollPendingTrigger(nowMs);
    return;
  }

  if (nowMs - lastAnnounceRxMs_ > kAnnounceTimeoutMs) {
    Serial.println("[espnow] host announce timed out, re-sweeping");
    haveHost_ = false;
    roleCollision_ = false;
    timebase_.invalidate();
    syncPhase_ = SyncPhase::Idle;
    lastSyncStartMs_ = 0;
    state_ = UplinkState::Connecting;
    pollPendingTrigger(nowMs);
    return;
  }

  state_ = roleCollision_ ? UplinkState::Failed : UplinkState::Ready;

  pollTimeSync(nowMs);
  if (syncPhase_ == SyncPhase::Idle && linkUsable() &&
      (lastSyncStartMs_ == 0 || nowMs - lastSyncStartMs_ >= kResyncIntervalMs)) {
    startTimeSync(nowMs);
  }

  pollPendingTrigger(nowMs);
}

void UplinkEspNow::startTimeSync(uint32_t nowMs) {
  offsetFilter_.reset();
  syncPhase_ = SyncPhase::Exchanging;
  syncExchangesDone_ = 0;
  syncExchangeStartMs_ = nowMs;
  lastSyncStartMs_ = nowMs;
  sendTimeReq(nowMs);
}

void UplinkEspNow::sendTimeReq(uint32_t nowMs) {
  (void)nowMs;
  int64_t t1 = esp_timer_get_time();
  uint8_t buf[16];
  size_t n = linkproto::encodeTimeReq(buf, sizeof(buf), t1);
  if (n == 0) return;
  radio_.send(hostMac_, buf, n);
}

void UplinkEspNow::pollTimeSync(uint32_t nowMs) {
  if (syncPhase_ != SyncPhase::Exchanging) return;
  if (nowMs - syncExchangeStartMs_ <= kSyncExchangeTimeoutMs) return;

  // No TimeResp within the timeout: count this exchange as done (dropped)
  // and move on rather than stalling the whole sync indefinitely.
  syncExchangesDone_++;
  if (syncExchangesDone_ >= kSyncExchangeCount) {
    finishTimeSync(nowMs);
  } else {
    syncExchangeStartMs_ = nowMs;
    sendTimeReq(nowMs);
  }
}

void UplinkEspNow::finishTimeSync(uint32_t nowMs) {
  syncPhase_ = SyncPhase::Idle;
  if (!offsetFilter_.hasSample()) {
    Serial.println("[espnow] time sync: no usable sample, will retry at next resync");
    return;
  }

  // Avoid a mid-run timestamp jump: skip re-anchoring (but keep the
  // existing anchor) if a trigger was accepted within the current lockout
  // window.
  bool recentlyTriggered = lastTriggerAcceptedMs_ != 0 &&
      (nowMs - lastTriggerAcceptedMs_) < lockoutMs_;
  if (timebase_.synced() && recentlyTriggered) {
    Serial.println("[espnow] time sync: deferring re-anchor (recent trigger)");
    return;
  }

  int64_t nowMono = esp_timer_get_time();
  int64_t nowWall = nowMono + offsetFilter_.bestOffsetUs();
  timebase_.anchor(nowWall, nowMono);
  ntpOffsetMs_ = offsetFilter_.bestDelayUs() / 2 / 1000.0;
  Serial.printf("[espnow] time synced, offset_uncertainty=%.3fms\n", ntpOffsetMs_);
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
