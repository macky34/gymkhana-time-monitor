#include "pilot_indicator.h"

#include <Arduino.h>

void PilotIndicator::begin(uint8_t gpio) {
  gpio_ = gpio;
  pinMode(gpio_, OUTPUT);
  digitalWrite(gpio_, LOW);
}

void PilotIndicator::playRoleIntro(wire::Role role) {
  int blinks = (role == wire::Role::Start) ? 1 : 2;
  uint32_t onMs = (role == wire::Role::Start) ? 600 : 150;
  for (int i = 0; i < blinks; i++) {
    digitalWrite(gpio_, HIGH);
    delay(onMs);
    digitalWrite(gpio_, LOW);
    delay(150);
  }
}

void PilotIndicator::flash(uint32_t nowMs) {
  flashing_ = true;
  flashStartMs_ = nowMs;
}

void PilotIndicator::poll(uint32_t nowMs) {
  if (flashing_ && nowMs - flashStartMs_ >= kFlashMs) {
    flashing_ = false;
  }
  digitalWrite(gpio_, flashing_ ? HIGH : LOW);
}
