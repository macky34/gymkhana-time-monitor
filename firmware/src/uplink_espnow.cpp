#include "uplink_espnow.h"

#include <Arduino.h>
#include <WiFi.h>
#include <esp_wifi.h>
#include <cstring>

#include "config.h"
#include "link_frame.h"

void UplinkEspNow::begin() {
  WiFi.mode(WIFI_STA);
#if defined(CONFIG_IDF_TARGET_ESP32C5)
  // Required for esp_wifi_set_protocol() (used by enableLongRange() below)
  // to succeed on this dual-band chip -- it fails under WIFI_BAND_MODE_AUTO.
  WiFi.setBandMode(WIFI_BAND_MODE_2G_ONLY);
#endif
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

  // Announce is the only frame type accepted from a not-yet-locked-in
  // sender (that's how a host is discovered). Once linked, everything --
  // including further Announces -- must come from the same MAC as our
  // locked-in host, or it's ignored: this stops a second, independent
  // sensor pair at the same venue (or a spoofed frame) from disrupting or
  // corrupting this link.
  if (haveHost_ && memcmp(pkt.mac, hostMac_, 6) != 0) return;
  if (!haveHost_ && type != linkproto::FrameType::Announce) return;

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
        radio_.resetSendFailStreak();
        haveHost_ = true;
        Serial.println("[espnow] host found");
      }
      lastAnnounceRxMs_ = nowMs;
      hostUplinkUp_ = info.uplinkUp;
      hostChannel_ = info.channel;
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
    relayBurst_.active = false;  // don't relay a stale burst to a new host
    timebase_.invalidate();
    syncPhase_ = SyncPhase::Idle;
    lastSyncStartMs_ = 0;
    state_ = UplinkState::Connecting;
    pollPendingTrigger(nowMs);
    return;
  }

  if (radio_.sendFailStreak() >= kSendFailStreakLimit) {
    Serial.println("[espnow] too many failed sends in a row, re-sweeping");
    radio_.resetSendFailStreak();
    haveHost_ = false;
    roleCollision_ = false;
    relayBurst_.active = false;  // don't relay a stale burst to a new host
    timebase_.invalidate();
    syncPhase_ = SyncPhase::Idle;
    lastSyncStartMs_ = 0;
    state_ = UplinkState::Connecting;
    pollPendingTrigger(nowMs);
    return;
  }

  if (roleCollision_) {
    state_ = UplinkState::Failed;
  } else if (timebase_.synced()) {
    state_ = UplinkState::Ready;
  } else {
    state_ = UplinkState::Syncing;
  }

  pollTimeSync(nowMs);
  if (syncPhase_ == SyncPhase::Idle && linkUsable() &&
      (lastSyncStartMs_ == 0 || nowMs - lastSyncStartMs_ >= kResyncIntervalMs)) {
    startTimeSync(nowMs);
  }

  pollRelayBurst(nowMs);
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
  (void)nowMs;
  syncPhase_ = SyncPhase::Idle;
  if (!offsetFilter_.hasSample()) {
    Serial.println("[espnow] time sync: no usable sample, will retry at next resync");
    return;
  }

  int64_t nowMono = esp_timer_get_time();
  int64_t nowWall = nowMono + offsetFilter_.bestOffsetUs();
  // Slewed, not stepped: a periodic resync must never jump a timestamp
  // mid-run. reanchorSlewed() hard-anchors on the very first sync (nothing
  // to slew from yet) and gradually corrects on every later one.
  timebase_.reanchorSlewed(nowWall, nowMono);
  ntpOffsetMs_ = offsetFilter_.bestDelayUs() / 2 / 1000.0;
  Serial.printf("[espnow] time synced, offset_uncertainty=%.3fms\n", ntpOffsetMs_);
}

bool UplinkEspNow::sendRelay(const char *payload, size_t len) {
  uint8_t buf[kEspNowMaxPayload];
  size_t n = linkproto::encodeRelay(buf, sizeof(buf), payload, len);
  if (n == 0) return false;
  return radio_.send(hostMac_, buf, n);
}

void UplinkEspNow::pollRelayBurst(uint32_t nowMs) {
  if (!relayBurst_.active) return;
  if ((int32_t)(nowMs - relayBurst_.nextAtMs) < 0) return;

  sendRelay(relayBurst_.payload, relayBurst_.len);
  relayBurst_.remaining--;
  if (relayBurst_.remaining == 0) {
    relayBurst_.active = false;
  } else {
    relayBurst_.nextAtMs = nowMs + 50;
  }
}

void UplinkEspNow::pollPendingTrigger(uint32_t nowMs) {
  if (!pendingTrigger_.active) return;
  if (linkUsable()) {
    sendRelay(pendingTrigger_.payload, pendingTrigger_.len);
    memcpy(relayBurst_.payload, pendingTrigger_.payload, pendingTrigger_.len);
    relayBurst_.len = pendingTrigger_.len;
    relayBurst_.remaining = 2;
    relayBurst_.nextAtMs = nowMs + 50;
    relayBurst_.active = true;
    pendingTrigger_.active = false;
    return;
  }
  if (nowMs - pendingTrigger_.queuedAtMs > kTriggerRetryMs) {
    Serial.println("[espnow] trigger dropped: no usable uplink after 3s retry");
    pendingTrigger_.active = false;
  }
}

bool UplinkEspNow::sendToServer(const char *payload, size_t len, Redundancy r) {
  if (r != Redundancy::Burst3) {
    // hb: single relay, no burst/queue -- the next one is 5s away anyway.
    if (!linkUsable()) return false;
    return sendRelay(payload, len);
  }

  if (len >= sizeof(pendingTrigger_.payload)) return false;

  if (linkUsable()) {
    sendRelay(payload, len);
    memcpy(relayBurst_.payload, payload, len);
    relayBurst_.len = len;
    relayBurst_.remaining = 2;
    relayBurst_.nextAtMs = millis() + 50;
    relayBurst_.active = true;
    return true;
  }

  // Link not usable yet: queue for up to kTriggerRetryMs rather than
  // dropping it immediately, in case the host's uplink recovers in time.
  memcpy(pendingTrigger_.payload, payload, len);
  pendingTrigger_.len = len;
  pendingTrigger_.queuedAtMs = millis();
  pendingTrigger_.active = true;
  return true;
}
