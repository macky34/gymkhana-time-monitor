// DebugMonitor (Issue #44): read-only observer of existing modules' public
// state, printed to Serial when DEBUG_VERBOSE is defined (config.h). Never
// writes to any module it watches. LED pattern/link-background changes are
// logged as they happen; RSSI/NTP status is logged on a fixed interval.
#pragma once

#include <cstdint>

#include "pilot_indicator.h"
#include "status_led.h"
#include "uplink.h"
#include "uplink_espnow.h"

class DebugMonitor {
 public:
  // uplinkEspNow is the concrete ESP-NOW client instance that main.cpp
  // always constructs (even when WiFi-direct is active); its RSSI/link
  // fields are simply not logged when uplink != uplinkEspNow.
  void begin(const StatusLed *statusLed, const PilotIndicator *pilotLed,
             const Uplink *uplink, const UplinkEspNow *uplinkEspNow);

  // Call every loop() iteration.
  void poll(uint32_t nowMs);

 private:
  const StatusLed *statusLed_ = nullptr;
  const PilotIndicator *pilotLed_ = nullptr;
  const Uplink *uplink_ = nullptr;
  const UplinkEspNow *uplinkEspNow_ = nullptr;

  LedPattern lastLedPattern_ = LedPattern::Off;
  bool haveLastLedPattern_ = false;
  PilotIndicator::LinkBackground lastLinkBackground_ = PilotIndicator::LinkBackground::None;
  bool haveLastLinkBackground_ = false;

  uint32_t lastStatusMs_ = 0;
  static constexpr uint32_t kStatusIntervalMs = 1000;
};
