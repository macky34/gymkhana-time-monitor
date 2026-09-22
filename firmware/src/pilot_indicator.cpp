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

bool PilotIndicator::backgroundLevel(uint32_t nowMs) const {
  switch (background_) {
    case LinkBackground::None:
    case LinkBackground::Linked:
      return false;
    case LinkBackground::Searching:
      return (nowMs / 500) % 2 == 0;
    case LinkBackground::UplinkDown:
      return (nowMs / 150) % 2 == 0;
    case LinkBackground::RoleCollision:
      return (nowMs / 80) % 2 == 0;
  }
  return false;
}

void PilotIndicator::startPulses(uint32_t nowMs, int count) {
  if (count <= 0) return;
  pulsing_ = true;
  pulseOn_ = true;
  pulsesRemaining_ = count;
  pulsePhaseStartMs_ = nowMs;
}

void PilotIndicator::updatePulse(uint32_t nowMs) {
  uint32_t phaseMs = pulseOn_ ? kPulseOnMs : kPulseGapMs;
  if (nowMs - pulsePhaseStartMs_ < phaseMs) return;
  pulsePhaseStartMs_ = nowMs;
  if (pulseOn_) {
    pulseOn_ = false;
    pulsesRemaining_--;
    if (pulsesRemaining_ <= 0) pulsing_ = false;
  } else {
    pulseOn_ = true;
  }
}

void PilotIndicator::poll(uint32_t nowMs) {
  if (flashing_ && nowMs - flashStartMs_ >= kFlashMs) {
    flashing_ = false;
  }
  if (flashing_) {
    digitalWrite(gpio_, HIGH);
    return;
  }

  if (pulsing_) {
    updatePulse(nowMs);
    digitalWrite(gpio_, pulsing_ && pulseOn_ ? HIGH : LOW);
    return;
  }

  digitalWrite(gpio_, backgroundLevel(nowMs));
}
