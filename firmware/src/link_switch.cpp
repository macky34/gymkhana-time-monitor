#include "link_switch.h"

#include <Arduino.h>

bool LinkSwitch::read() const { return digitalRead(gpio_) != LOW; }

void LinkSwitch::begin(uint8_t gpio) {
  gpio_ = gpio;
  pinMode(gpio_, INPUT_PULLUP);
  enabled_ = read();
  pendingEnabled_ = enabled_;
  pendingInit_ = false;
}

void LinkSwitch::poll(uint32_t nowMs) {
  bool raw = read();
  if (raw != pendingEnabled_) {
    pendingEnabled_ = raw;
    pendingStartMs_ = nowMs;
    pendingInit_ = true;
    return;
  }
  if (pendingInit_ && raw != enabled_ && nowMs - pendingStartMs_ >= kDebounceMs) {
    enabled_ = raw;
    changed_ = true;
  }
}
