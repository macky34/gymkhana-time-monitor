// LinkSwitch: ESP-NOW enable switch. INPUT_PULLUP; GND-shorted = WiFi-direct
// (safe default), OPEN = ESP-NOW client. Same debounce approach as
// ModeSwitch, kept separate since it toggles a plain bool rather than a
// wire::Role.
#pragma once

#include <cstdint>

class LinkSwitch {
 public:
  void begin(uint8_t gpio);

  // Call every loop() iteration.
  void poll(uint32_t nowMs);

  bool espNowEnabled() const { return enabled_; }

  bool changed() const { return changed_; }
  void clearChanged() { changed_ = false; }

 private:
  static constexpr uint32_t kDebounceMs = 1000;

  bool read() const;

  uint8_t gpio_ = 0;
  bool enabled_ = false;
  bool pendingEnabled_ = false;
  uint32_t pendingStartMs_ = 0;
  bool pendingInit_ = false;
  bool changed_ = false;
};
