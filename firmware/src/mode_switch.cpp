#include "mode_switch.h"

#include <Arduino.h>

wire::Role ModeSwitch::read() const {
  return digitalRead(gpio_) == LOW ? wire::Role::Start : wire::Role::Goal;
}

void ModeSwitch::begin(uint8_t gpio) {
  gpio_ = gpio;
  pinMode(gpio_, INPUT_PULLUP);
  delay(5);  // let the pull-up settle before the first read
  role_ = read();
  pendingRole_ = role_;
  pendingInit_ = false;
}

void ModeSwitch::poll(uint32_t nowMs) {
  wire::Role raw = read();
  if (raw != pendingRole_) {
    pendingRole_ = raw;
    pendingStartMs_ = nowMs;
    pendingInit_ = true;
    return;
  }
  if (pendingInit_ && raw != role_ && nowMs - pendingStartMs_ >= kDebounceMs) {
    role_ = raw;
    changed_ = true;
  }
}
