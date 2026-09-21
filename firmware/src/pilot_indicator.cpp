#include "pilot_indicator.h"

#include <Arduino.h>

void PilotIndicator::begin(uint8_t gpio) {
  gpio_ = gpio;
  pinMode(gpio_, OUTPUT);
  digitalWrite(gpio_, LOW);
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
