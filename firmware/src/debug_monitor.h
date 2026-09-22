// DebugMonitor: DEBUG_VERBOSE-only Serial logging. A read-only observer of
// existing module getters -- never mutates state. Logs LED pattern / link
// background transitions when they happen, and a periodic NTP sync status
// line.
#pragma once

#include <cstdint>

#include "pilot_indicator.h"
#include "status_led.h"
#include "uplink.h"

class DebugMonitor {
 public:
  void begin(const StatusLed *statusLed, const PilotIndicator *pilotLed,
             const Uplink *uplink);

  // Call every loop() iteration.
  void poll(uint32_t nowMs);

 private:
  static constexpr uint32_t kStatusIntervalMs = 1000;

  const StatusLed *statusLed_ = nullptr;
  const PilotIndicator *pilotLed_ = nullptr;
  const Uplink *uplink_ = nullptr;

  LedPattern lastPattern_ = LedPattern::Off;
  PilotIndicator::LinkBackground lastBackground_ =
      PilotIndicator::LinkBackground::None;
  uint32_t lastStatusMs_ = 0;
};
