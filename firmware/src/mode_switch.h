// ModeSwitch: start/goal role switch. INPUT_PULLUP; GND-shorted = Goal,
// OPEN = Start (safe default). Confirms a role only after the raw reading
// has been stable for kDebounceMs, to reject vibration-induced chatter.
#pragma once

#include <cstdint>

#include "wire.h"

class ModeSwitch {
 public:
  void begin(uint8_t gpio);

  // Call every loop() iteration.
  void poll(uint32_t nowMs);

  wire::Role role() const { return role_; }

  // True once a confirmed role change has happened since the last
  // clearChanged() call.
  bool changed() const { return changed_; }
  void clearChanged() { changed_ = false; }

 private:
  static constexpr uint32_t kDebounceMs = 1000;

  wire::Role read() const;

  uint8_t gpio_ = 0;
  wire::Role role_ = wire::Role::Start;
  wire::Role pendingRole_ = wire::Role::Start;
  uint32_t pendingStartMs_ = 0;
  bool pendingInit_ = false;
  bool changed_ = false;
};
