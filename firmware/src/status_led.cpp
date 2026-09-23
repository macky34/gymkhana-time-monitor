#include "status_led.h"

#include <Arduino.h>

void StatusLed::begin(uint8_t gpio, bool activeLow) {
  gpio_ = gpio;
  activeLow_ = activeLow;
  pinMode(gpio_, OUTPUT);
  write(false);
}

void StatusLed::set(LedPattern p) {
  if (p == pattern_) return;
  pattern_ = p;
  phaseInit_ = false;  // restart the blink phase on the next poll()
}

void StatusLed::poll(uint32_t nowMs) {
  if (!phaseInit_) {
    phaseStartMs_ = nowMs;
    phaseInit_ = true;
  }
  switch (pattern_) {
    case LedPattern::Off:
      write(false);
      break;
    case LedPattern::Solid:
      write(true);
      break;
    case LedPattern::BlinkFast:
      // ~4Hz (Sensor-Device wiki §4.1), i.e. faster than BlinkSlow below.
      write(((nowMs - phaseStartMs_) / 125) % 2 == 0);
      break;
    case LedPattern::BlinkSlow:
      write(((nowMs - phaseStartMs_) / 200) % 2 == 0);
      break;
  }
}

void StatusLed::write(bool on) {
  bool level = activeLow_ ? !on : on;
  digitalWrite(gpio_, level ? HIGH : LOW);
}
