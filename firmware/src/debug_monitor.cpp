#include "debug_monitor.h"

#include <Arduino.h>

namespace {

const char *patternName(LedPattern p) {
  switch (p) {
    case LedPattern::Off: return "off";
    case LedPattern::BlinkFast: return "blink_fast";
    case LedPattern::BlinkSlow: return "blink_slow";
    case LedPattern::Solid: return "solid";
  }
  return "?";
}

const char *backgroundName(PilotIndicator::LinkBackground bg) {
  switch (bg) {
    case PilotIndicator::LinkBackground::None: return "none";
    case PilotIndicator::LinkBackground::Searching: return "searching";
    case PilotIndicator::LinkBackground::Linked: return "linked";
    case PilotIndicator::LinkBackground::UplinkDown: return "uplink_down";
    case PilotIndicator::LinkBackground::RoleCollision: return "role_collision";
  }
  return "?";
}

}  // namespace

void DebugMonitor::begin(const StatusLed *statusLed,
                          const PilotIndicator *pilotLed,
                          const Uplink *uplink,
                          const UplinkEspNow *uplinkEspNow) {
  statusLed_ = statusLed;
  pilotLed_ = pilotLed;
  uplink_ = uplink;
  uplinkEspNow_ = uplinkEspNow;
  lastPattern_ = statusLed_->pattern();
  lastBackground_ = pilotLed_->linkBackground();
  lastStatusMs_ = 0;
}

void DebugMonitor::poll(uint32_t nowMs) {
  LedPattern pattern = statusLed_->pattern();
  if (pattern != lastPattern_) {
    Serial.printf("[debug] led1 pattern -> %s\n", patternName(pattern));
    lastPattern_ = pattern;
  }

  PilotIndicator::LinkBackground background = pilotLed_->linkBackground();
  if (background != lastBackground_) {
    Serial.printf("[debug] led2 link_background -> %s\n",
                  backgroundName(background));
    lastBackground_ = background;
  }

  if (nowMs - lastStatusMs_ < kStatusIntervalMs) return;
  lastStatusMs_ = nowMs;
  Serial.printf("[debug] ntp_synced=%d ntp_offset_ms=%.3f\n",
                uplink_->timebase().synced(), uplink_->ntpOffsetMs());
  if (uplink_ == static_cast<const Uplink *>(uplinkEspNow_)) {
    Serial.printf("[debug] rssi=%d role_collision=%d\n", uplinkEspNow_->rssi(),
                  uplinkEspNow_->roleCollision());
  }
}
