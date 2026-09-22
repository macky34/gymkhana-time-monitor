// StatusLed: the external LED1 (wired to what was the onboard STATUS_LED_GPIO
// -- now brought out through the enclosure). Same four states as the
// original setLed() call sites, but as an explicit state machine instead of
// scattered digitalWrite() calls: set() always restarts the blink phase from
// "on", so a pattern change (e.g. reconnect -> ready) can never land on a
// leftover "off" half-cycle from the previous pattern (the root cause of the
// issue #36 "LED sometimes stays dark after reconnect" bug).
#pragma once

#include <cstdint>

enum class LedPattern : uint8_t {
  Off,        // sync failed
  BlinkFast,  // ~250ms period: WiFi connecting / reconnecting
  BlinkSlow,  // ~200ms period: waiting for clock sync
  Solid,      // ready
};

class StatusLed {
 public:
  void begin(uint8_t gpio, bool activeLow);

  // No-op if p is already the current pattern (keeps the running blink phase
  // instead of restarting it every loop() iteration).
  void set(LedPattern p);

  // Drives the GPIO according to the current pattern and nowMs. Call every
  // loop() iteration; non-blocking.
  void poll(uint32_t nowMs);

  LedPattern pattern() const { return pattern_; }

 private:
  void write(bool on);

  uint8_t gpio_ = 0;
  bool activeLow_ = false;
  LedPattern pattern_ = LedPattern::Off;
  uint32_t phaseStartMs_ = 0;
  bool phaseInit_ = false;
};
