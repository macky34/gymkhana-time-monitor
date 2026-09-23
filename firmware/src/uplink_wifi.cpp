#include "uplink_wifi.h"

#include <Arduino.h>
#include <HTTPClient.h>
#include <WiFi.h>
#include <cstring>
#include <sys/time.h>

#include "config.h"
#include "wire.h"

namespace {

int64_t nowWallUs() {
  struct timeval tv;
  gettimeofday(&tv, nullptr);
  return (int64_t)tv.tv_sec * 1000000LL + tv.tv_usec;
}

// Backoff schedule for a failed SNTP sync, indexed by (syncFailStreak_ - 1)
// and capped at the last entry for any further failure.
uint32_t syncBackoffMs(uint8_t streak) {
  if (streak <= 1) return 10000;
  if (streak == 2) return 30000;
  return 60000;
}

}  // namespace

void UplinkWifi::begin() {
  lockoutMs_ = DEFAULT_LOCKOUT_MS;
  wifi_provision::getCredentials(ssid_, pass_);

  WiFi.mode(WIFI_STA);
#if defined(CONFIG_IDF_TARGET_ESP32C5)
  // C5 is 2.4/5GHz dual-band; the venue AP is 2.4GHz-only, so pin the band
  // to skip scanning 5GHz (faster connect/reconnect). setBandMode() requires
  // STA to already be started, hence after mode() rather than before.
  WiFi.setBandMode(WIFI_BAND_MODE_2G_ONLY);
#endif
  WiFi.begin(ssid_, pass_);
  // Required for ESP-NOW: modem sleep would otherwise drop received frames
  // while this device is (also) acting as a relay host.
  WiFi.setSleep(false);

  bridge_.begin(role_, &UplinkWifi::relayToServerCallback, this);

  state_ = UplinkState::Connecting;
  // wifiDownSinceMs_ is set on the first loop() call that observes
  // WL_CONNECTED != true, which every fresh WiFi.begin() satisfies at least
  // once -- so this single flag covers both the initial connect and later
  // reconnects (see loop()'s "just (re)connected" branch).
  wifiDownSinceMs_ = 0;
  lastReconnectAttemptMs_ = 0;
  syncFailStreak_ = 0;
  nextSyncAttemptMs_ = 0;
  lastConnectedChannel_ = 0;
}

void UplinkWifi::loop(uint32_t nowMs) {
  if (WiFi.status() != WL_CONNECTED) {
    if (state_ != UplinkState::Connecting) {
      state_ = UplinkState::Connecting;
    }
    if (wifiDownSinceMs_ == 0) wifiDownSinceMs_ = nowMs;

    // Calling WiFi.reconnect() every loop() call (every ~1-50ms) races the
    // previous connect attempt: esp_wifi logs "sta is connecting, return
    // error" and the STA state machine never gets a chance to finish
    // NO_AP_FOUND / STA_LEAVING and settle, so it never recovers on its own
    // (issue #35). Throttle attempts and disconnect first so each attempt
    // starts from a clean STA state.
    if (nowMs - lastReconnectAttemptMs_ > 5000) {
      lastReconnectAttemptMs_ = nowMs;
      Serial.println("[wifi] disconnected, reconnecting...");
      WiFi.disconnect();
      if (lastConnectedChannel_ != 0) {
        // Pin to the channel we were last connected on: WiFi.begin()'s
        // default full-band scan would otherwise move this device's WiFi
        // (and hence its ESP-NOW radio, shared hardware) to whatever
        // channel it scans next, breaking the link to any client relaying
        // through this device as its host.
        WiFi.begin(ssid_, pass_, lastConnectedChannel_);
      } else {
        WiFi.begin(ssid_, pass_);
      }
    }

    // Initial connect gets a shorter timeout (matching the original
    // setup()'s 60s) than a mid-run drop (matching the original loop()'s
    // 5min) -- a full restart re-runs esp_wifi init from scratch, which is
    // more reliable than anything achievable from a stuck STA state.
    uint32_t timeoutMs = everConnected_ ? (5UL * 60UL * 1000UL) : 60UL * 1000UL;
    if (nowMs - wifiDownSinceMs_ > timeoutMs) {
      Serial.println("[wifi] down too long, restarting");
      ESP.restart();
    }
    // ESP-NOW keeps working even while the AP link is down (it doesn't
    // depend on association), so the bridge still answers Discover/relays.
    bridge_.setUplinkUp(false);
    bridge_.loop(nowMs);
    return;
  }

  if (wifiDownSinceMs_ != 0) {
    // Just (re)connected.
    wifiDownSinceMs_ = 0;
    lastConnectedChannel_ = WiFi.channel();
    if (!everConnected_) {
      everConnected_ = true;
      // Bind a fixed local port so we can both send heartbeats/triggers and
      // receive the server's piggybacked "config" reply (issue #18) on the
      // same socket.
      udp_.begin(LOCAL_UDP_PORT);
      Serial.printf("[wifi] connected, ip=%s\n", WiFi.localIP().toString().c_str());
    } else {
      Serial.println("[wifi] reconnected");
    }
    // Re-sync unconditionally on (re)connect: the clock may have drifted
    // while we were disconnected, and re-anchoring also gives the status
    // LED a clean, deterministic state instead of wherever a stale blink
    // phase left it (issue #36 follow-up).
    startSync(nowMs);
  } else if (state_ == UplinkState::Failed) {
    if ((int32_t)(nowMs - nextSyncAttemptMs_) >= 0) startSync(nowMs);
  } else if (state_ != UplinkState::Syncing && nowMs - lastResyncMs_ > 3600UL * 1000UL) {
    startSync(nowMs);
  }

  if (state_ == UplinkState::Syncing) {
    handleSyncing(nowMs);
  }

  pollBurst(nowMs);
  pollConfigReply();

  bridge_.setUplinkUp(state_ == UplinkState::Ready);
  bridge_.loop(nowMs);
}

void UplinkWifi::startSync(uint32_t nowMs) {
  // chrony on the RPi answers SNTP; anchor esp_timer to the synced wall
  // clock once getLocalTime() reports success (see handleSyncing()).
  configTime(0, 0, RPI_HOST);
  syncStartMs_ = nowMs;
  lastResyncMs_ = nowMs;
  state_ = UplinkState::Syncing;
  timebase_.invalidate();
}

void UplinkWifi::handleSyncing(uint32_t nowMs) {
  struct tm tm;
  // ms=0 makes getLocalTime() a single non-blocking poll (it still takes up
  // to ~10ms internally) instead of the original's dedicated up-to-8s
  // blocking wait loop -- loop() is called again on the next iteration to
  // keep polling.
  if (getLocalTime(&tm, 0)) {
    timebase_.anchor(nowWallUs(), esp_timer_get_time());
    state_ = UplinkState::Ready;
    syncFailStreak_ = 0;
    Serial.println("[sync] clock synced");
    fetchConfigOnce();
    return;
  }
  if (nowMs - syncStartMs_ > 8000) {
    if (syncFailStreak_ < 255) syncFailStreak_++;
    if (syncFailStreak_ > kSyncFailRestartCount) {
      Serial.println("[sync] FAILED too many times in a row, restarting");
      delay(50);
      ESP.restart();
    }
    uint32_t backoffMs = syncBackoffMs(syncFailStreak_);
    nextSyncAttemptMs_ = nowMs + backoffMs;
    Serial.printf("[sync] FAILED (retry in %lus)\n", (unsigned long)(backoffMs / 1000));
    state_ = UplinkState::Failed;
    fetchConfigOnce();
  }
}

void UplinkWifi::fetchConfigOnce() {
  if (fetchedConfigOnce_) return;
  fetchedConfigOnce_ = true;

  HTTPClient http;
  String url = String("http://") + RPI_HOST + ":" + String(RPI_HTTP_PORT) +
               "/api/internal/sensor-config";
  http.begin(url);
  int code = http.GET();
  if (code == 200) {
    String body = http.getString();
    uint32_t newMs = 0;
    if (wire::parseLockoutMs(body.c_str(), body.length(), &newMs)) {
      lockoutMs_ = newMs;
    }
    Serial.printf("[config] lockout=%.3f sec\n", lockoutMs_ / 1000.0);
  } else {
    Serial.printf("[config] fetch failed (%d), using default %u\n", code, lockoutMs_);
  }
  http.end();
}

void UplinkWifi::pollConfigReply() {
  // Non-blocking check for the server's "config" reply, piggybacked on our
  // heartbeat (the RPi replies to whichever UDP source port sent the hb).
  // This is how a lockout change made in the admin UI reaches us without a
  // reboot (issue #18): applied within one heartbeat interval (<=5s).
  int packetSize = udp_.parsePacket();
  if (packetSize <= 0) return;
  char buf[128];
  int len = udp_.read(buf, sizeof(buf) - 1);
  if (len <= 0) return;

  bridge_.forwardConfigToClient(buf, (size_t)len);

  uint32_t newMs = 0;
  if (wire::parseLockoutMs(buf, (size_t)len, &newMs)) {
    if (newMs != lockoutMs_) {
      Serial.printf("[config] lockout updated: %.3f -> %.3f sec\n",
                    lockoutMs_ / 1000.0, newMs / 1000.0);
    }
    lockoutMs_ = newMs;
  }
}

bool UplinkWifi::sendToServer(const char *payload, size_t len, Redundancy r) {
  if (len >= sizeof(burst_.payload)) return false;

  if (r != Redundancy::Burst3) {
    // hb: sent directly, never touching burst_ -- otherwise this would
    // overwrite (and cut short) a trigger burst still in flight.
    sendPacketNow(payload, len);
    return true;
  }

  memcpy(burst_.payload, payload, len);
  burst_.payload[len] = '\0';
  burst_.len = len;
  sendPacketNow(burst_.payload, burst_.len);
  burst_.remaining = 2;
  burst_.nextAtMs = millis() + 50;
  burst_.active = true;
  return true;
}

void UplinkWifi::pollBurst(uint32_t nowMs) {
  if (!burst_.active) return;
  if ((int32_t)(nowMs - burst_.nextAtMs) < 0) return;

  sendPacketNow(burst_.payload, burst_.len);
  burst_.remaining--;
  if (burst_.remaining == 0) {
    burst_.active = false;
  } else {
    burst_.nextAtMs = nowMs + 50;
  }
}

bool UplinkWifi::relayToServer(const char *payload, size_t len) {
  if (len >= sizeof(burst_.payload)) return false;
  sendPacketNow(payload, len);
  return true;
}

void UplinkWifi::sendPacketNow(const char *payload, size_t len) {
  udp_.beginPacket(RPI_HOST, RPI_UDP_PORT);
  udp_.write((const uint8_t *)payload, len);
  udp_.endPacket();
}
