// Identity: this device's role plus the per-boot random ID and the two
// monotonically increasing sequence counters that make up the server's dedup
// key (sensor_id, boot_id, seq) -- see internal/timing (Go) / lib/wire.
#pragma once

#include <cstdint>

#include "wire.h"

class Identity {
 public:
  // Must be called after WiFi.mode(WIFI_STA) (not before): the RF hardware
  // needs to be initialized for esp_random() to be a true hardware RNG
  // rather than a deterministic PRNG. The original main.cpp called
  // esp_random() before WiFi.mode(), which risked the same boot_id being
  // drawn across reboots -- and because (sensor_id, boot_id, seq) is a
  // permanent DB UNIQUE key, a repeat boot_id makes the server silently drop
  // that device's first trigger after reboot (no error, no log; see
  // internal/timing/timing.go's dedup path). Also mixes in the STA MAC's
  // low 32 bits and the current esp_timer reading as extra entropy.
  void begin(wire::Role role);

  wire::Role role() const { return role_; }
  uint32_t bootId() const { return bootId_; }

  uint32_t nextTriggerSeq() { return ++triggerSeq_; }
  uint32_t nextHbSeq() { return ++hbSeq_; }

 private:
  wire::Role role_ = wire::Role::Start;
  uint32_t bootId_ = 0;
  uint32_t triggerSeq_ = 0;
  uint32_t hbSeq_ = 0;
};
