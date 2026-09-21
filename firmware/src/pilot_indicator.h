// PilotIndicator: the external LED2 ("pilot lamp"), a second information
// channel distinct from StatusLed's connection/sync display. Phase 1 only
// implements the trigger flash layer; later phases add startup role blink,
// ESP-NOW link background pattern and RSSI pulses on top, in priority order
// (see the firmware plan's "LED表示設計" section) -- flash() always wins
// over whatever the lower-priority layers would otherwise be showing.
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
