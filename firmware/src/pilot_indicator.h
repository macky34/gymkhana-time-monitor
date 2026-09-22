// PilotIndicator: external LED2 ("pilot lamp"). Phase 1 implements only the
// trigger flash; later phases add role/link/RSSI layers on top of it.
#pragma once

#include <cstdint>

class PilotIndicator {
 public:
  void begin(uint8_t gpio);

  // Starts (or restarts) a one-shot flash. Call once per accepted trigger.
  void flash(uint32_t nowMs);

  // Drives the GPIO; non-blocking. Call every loop() iteration.
  void poll(uint32_t nowMs);

 private:
  static constexpr uint32_t kFlashMs = 120;

  uint8_t gpio_ = 0;
  bool flashing_ = false;
  uint32_t flashStartMs_ = 0;
};
