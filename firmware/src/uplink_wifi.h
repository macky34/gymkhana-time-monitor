// UplinkWifi: talks to the RPi directly over WiFi STA + UDP, matching the
// original main.cpp's WiFi/SNTP/UDP logic. Fully non-blocking: begin() only
// kicks off WiFi.begin() and returns; loop() drives connect -> SNTP sync ->
// ready, reconnects on drop (same throttle/restart policy as the original,
// see the comment on handleDisconnected()), and re-syncs hourly.
#pragma once

#include <WiFiUdp.h>

#include "espnow_bridge.h"
#include "timebase.h"
#include "uplink.h"
#include "wifi_provision.h"

// UplinkWifi always carries an EspNowBridge: a WiFi-direct sensor doubles
// as a potential relay host for an ESP-NOW client sensor (see the plan's
// "OFF=WiFi直結+ESP-NOW常時受信"). setRole() must be called before begin()
// so the bridge can announce this device's role for collision detection.
class UplinkWifi : public Uplink {
 public:
  void setRole(wire::Role role) { role_ = role; }

  void begin() override;
  void loop(uint32_t nowMs) override;

  UplinkState state() const override { return state_; }
  const TimeBase &timebase() const override { return timebase_; }
  uint32_t lockoutMs() const override { return lockoutMs_; }

  bool sendToServer(const char *payload, size_t len, Redundancy r) override;

  // Forwards a payload from an ESP-NOW client to the server verbatim, once
  // (no burst retry -- matches how this device's own hb is sent). Uses a
  // separate send path from sendToServer()'s burst_ so a relay can't
  // collide with this device's own in-flight trigger burst.
  bool relayToServer(const char *payload, size_t len);

 private:
  // C-callback adapter: EspNowBridge holds a plain function pointer (see
  // its header) rather than a C++ member-function pointer.
  static bool relayToServerCallback(void *ctx, const char *payload, size_t len) {
    return static_cast<UplinkWifi *>(ctx)->relayToServer(payload, len);
  }

  wire::Role role_ = wire::Role::Start;
  EspNowBridge bridge_;
  char ssid_[wifi_provision::kMaxSsidLen + 1] = {0};
  char pass_[wifi_provision::kMaxPassLen + 1] = {0};

  void startConnect(uint32_t nowMs);
  void handleConnecting(uint32_t nowMs);
  void handleSyncing(uint32_t nowMs);
  void startSync(uint32_t nowMs);
  void pollBurst(uint32_t nowMs);
  void pollConfigReply();
  void fetchConfigOnce();

  WiFiUDP udp_;
  UplinkState state_ = UplinkState::Init;
  TimeBase timebase_;
  uint32_t lockoutMs_;

  uint32_t wifiStartMs_ = 0;
  uint32_t lastReconnectAttemptMs_ = 0;
  uint32_t wifiDownSinceMs_ = 0;
  bool everConnected_ = false;

  uint32_t syncStartMs_ = 0;
  uint32_t lastResyncMs_ = 0;
  bool fetchedConfigOnce_ = false;

  // Backoff for a failed SNTP sync (handleSyncing()'s 8s timeout), instead
  // of falling through to the unconditional hourly resync check and
  // effectively waiting up to an hour to retry.
  uint8_t syncFailStreak_ = 0;
  uint32_t nextSyncAttemptMs_ = 0;
  static constexpr uint8_t kSyncFailRestartCount = 6;  // ESP.restart() past this

  // WiFi channel of the last successful connection, so a reconnect can pin
  // WiFi.begin() to it and skip a full-band scan (which would otherwise
  // temporarily move this device's WiFi -- and hence ESP-NOW -- channel
  // while a client is linked to it as a relay host). 0 = not yet known.
  uint8_t lastConnectedChannel_ = 0;

  // Non-blocking 3-packet burst sender (replaces the original delay(50)x2
  // in sendTrigger()). Only one burst is ever in flight: sendToServer() is
  // only called from loop() after a lockout window has elapsed, so a new
  // trigger can't arrive mid-burst.
  struct {
    char payload[192];
    size_t len = 0;
    uint8_t remaining = 0;
    uint32_t nextAtMs = 0;
    bool active = false;
  } burst_;

  void sendPacketNow(const char *payload, size_t len);
};
