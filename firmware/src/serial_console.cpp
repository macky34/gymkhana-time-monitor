#include "serial_console.h"

#include <Arduino.h>
#include <cstring>

namespace {

const char *stateName(UplinkState s) {
  switch (s) {
    case UplinkState::Init: return "init";
    case UplinkState::Connecting: return "connecting";
    case UplinkState::Syncing: return "syncing";
    case UplinkState::Ready: return "ready";
    case UplinkState::Failed: return "failed";
  }
  return "?";
}

}  // namespace

void SerialConsole::begin(wire::Role role, bool espNowEnabled,
                           const Uplink *uplink,
                           const UplinkEspNow *uplinkEspNow) {
  role_ = role;
  espNowEnabled_ = espNowEnabled;
  uplink_ = uplink;
  uplinkEspNow_ = uplinkEspNow;
  lineLen_ = 0;
}

void SerialConsole::poll(uint32_t nowMs) {
  (void)nowMs;
  while (Serial.available()) {
    char c = (char)Serial.read();
    if (c == '\r') continue;
    if (c == '\n') {
      lineBuf_[lineLen_] = '\0';
      handleLine();
      lineLen_ = 0;
      continue;
    }
    if (lineLen_ < sizeof(lineBuf_) - 1) {
      lineBuf_[lineLen_++] = c;
    }
  }
}

void SerialConsole::handleLine() {
  if (lineLen_ == 0) return;
  if (strcmp(lineBuf_, "status") == 0) {
    printStatus();
  } else if (strcmp(lineBuf_, "restart") == 0) {
    Serial.println("[console] restarting...");
    delay(50);
    ESP.restart();
  } else if (strcmp(lineBuf_, "help") == 0) {
    printHelp();
  } else {
    Serial.printf("[console] unknown command: %s (try 'help')\n", lineBuf_);
  }
}

void SerialConsole::printStatus() const {
  Serial.println("=== status ===");
  Serial.printf("role: %s\n", role_ == wire::Role::Start ? "start" : "goal");
  Serial.printf("link: %s\n", espNowEnabled_ ? "espnow-client" : "wifi-direct");
  Serial.printf("uplink state: %s\n", stateName(uplink_->state()));
  Serial.printf("ntp synced: %d\n", uplink_->timebase().synced());
  Serial.printf("ntp offset uncertainty: %.3fms\n", uplink_->ntpOffsetMs());
  if (espNowEnabled_) {
    Serial.printf("rssi: %d dBm\n", uplinkEspNow_->rssi());
    Serial.printf("host channel: %u\n", uplinkEspNow_->hostChannel());
    Serial.printf("role collision: %d\n", uplinkEspNow_->roleCollision());
    Serial.printf("host uplink up: %d\n", uplinkEspNow_->hostUplinkUp());
  }
  Serial.println("==============");
}

void SerialConsole::printHelp() const {
  Serial.println("[console] commands: status, restart, help");
}
