// PilotIndicator: external LED2 ("pilot lamp"). Phase 1 implements only the
// trigger flash; later phases add role/link/RSSI layers on top of it.
#pragma once

#include <cstdint>

#include "wire.h"

class PilotIndicator {
 public:
  // Background layer: only meaningful while this device is an ESP-NOW
  // client; a WiFi-direct device stays at None. flash() always overrides
  // whichever of these is showing.
  enum class LinkBackground : uint8_t {
    None,       // WiFi-direct (no ESP-NOW client role) -- always off
    Searching,  // looking for a host: slow blink
    Linked,     // host found and its uplink is up: off
    UplinkDown, // host found but it can't reach the server: fast blink
  };

  void begin(uint8_t gpio);

  // Blocking one-shot blink sequence (Start = one long flash, Goal = two
  // short flashes). Call once at startup, right after begin().
  void playRoleIntro(wire::Role role);

  // Starts (or restarts) a one-shot flash. Call once per accepted trigger.
  void flash(uint32_t nowMs);

  void setLinkBackground(LinkBackground bg) { background_ = bg; }
  LinkBackground linkBackground() const { return background_; }

  // Drives the GPIO; non-blocking. Call every loop() iteration.
  void poll(uint32_t nowMs);

 private:
  static constexpr uint32_t kFlashMs = 120;

  bool backgroundLevel(uint32_t nowMs) const;

  uint8_t gpio_ = 0;
  bool flashing_ = false;
  uint32_t flashStartMs_ = 0;
  LinkBackground background_ = LinkBackground::None;
};
