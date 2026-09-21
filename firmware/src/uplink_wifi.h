// UplinkWifi: talks to the RPi directly over WiFi STA + UDP, matching the
// original main.cpp's WiFi/SNTP/UDP logic. Fully non-blocking: begin() only
// kicks off WiFi.begin() and returns; loop() drives connect -> SNTP sync ->
// ready, reconnects on drop (same throttle/restart policy as the original,
// see the comment on handleDisconnected()), and re-syncs hourly.
#pragma once

#include <WiFiUdp.h>

#include "timebase.h"
#include "uplink.h"

class UplinkWifi : public Uplink {
 public:
  void begin() override;
  void loop(uint32_t nowMs) override;

  UplinkState state() const override { return state_; }
  const TimeBase &timebase() const override { return timebase_; }
  uint32_t lockoutMs() const override { return lockoutMs_; }

  bool sendToServer(const char *payload, size_t len, Redundancy r) override;

 private:
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
