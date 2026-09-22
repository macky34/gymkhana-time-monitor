#include "debug_monitor.h"

#include <Arduino.h>

namespace {

const char *ledPatternName(LedPattern p) {
  switch (p) {
    case LedPattern::Off:
      return "Off";
    case LedPattern::BlinkFast:
      return "BlinkFast";
    case LedPattern::BlinkSlow:
      return "BlinkSlow";
    case LedPattern::Solid:
      return "Solid";
  }
  return "?";
}

const char *linkBackgroundName(PilotIndicator::LinkBackground bg) {
  switch (bg) {
    case PilotIndicator::LinkBackground::None:
      return "None";
    case PilotIndicator::LinkBackground::Searching:
      return "Searching";
    case PilotIndicator::LinkBackground::Linked:
      return "Linked";
    case PilotIndicator::LinkBackground::UplinkDown:
      return "UplinkDown";
    case PilotIndicator::LinkBackground::RoleCollision:
      return "RoleCollision";
  }
  return "?";
}

}  // namespace

void DebugMonitor::begin(const StatusLed *statusLed, const PilotIndicator *pilotLed,
                          const Uplink *uplink, const UplinkEspNow *uplinkEspNow) {
  statusLed_ = statusLed;
  pilotLed_ = pilotLed;
  uplink_ = uplink;
  uplinkEspNow_ = uplinkEspNow;
}

void DebugMonitor::poll(uint32_t nowMs) {
  LedPattern p = statusLed_->pattern();
  if (!haveLastLedPattern_ || p != lastLedPattern_) {
    Serial.printf("[debug] LED1 pattern -> %s\n", ledPatternName(p));
    lastLedPattern_ = p;
    haveLastLedPattern_ = true;
  }

  PilotIndicator::LinkBackground bg = pilotLed_->linkBackground();
  if (!haveLastLinkBackground_ || bg != lastLinkBackground_) {
    Serial.printf("[debug] LED2 link background -> %s\n", linkBackgroundName(bg));
    lastLinkBackground_ = bg;
    haveLastLinkBackground_ = true;
  }

  if (nowMs - lastStatusMs_ < kStatusIntervalMs) return;
  lastStatusMs_ = nowMs;

  bool synced = uplink_->timebase().synced();
  bool isEspNow = uplink_ == static_cast<const Uplink *>(uplinkEspNow_);
  if (isEspNow) {
    Serial.printf(
        "[debug] rssi=%ddBm ntp_synced=%s ntp_offset_ms=%.3f role_collision=%s "
        "host_uplink_up=%s\n",
        uplinkEspNow_->rssi(), synced ? "yes" : "no", uplink_->ntpOffsetMs(),
        uplinkEspNow_->roleCollision() ? "yes" : "no",
        uplinkEspNow_->hostUplinkUp() ? "yes" : "no");
  } else {
    Serial.printf("[debug] ntp_synced=%s\n", synced ? "yes" : "no");
  }
}
