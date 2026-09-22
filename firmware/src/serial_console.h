// SerialConsole: on-site diagnostic commands over the same Serial port used
// for logging. Always active (not gated by DEBUG_VERBOSE) since it's meant
// for field troubleshooting on a deployed device, not just bench debugging.
// Read-only observer of existing module getters, except "restart".
#pragma once

#include <cstddef>
#include <cstdint>

#include "uplink.h"
#include "uplink_espnow.h"
#include "wire.h"

class SerialConsole {
 public:
  void begin(wire::Role role, bool espNowEnabled, const Uplink *uplink,
             const UplinkEspNow *uplinkEspNow);

  // Call every loop() iteration; non-blocking.
  void poll(uint32_t nowMs);

 private:
  void handleLine();
  void printStatus() const;
  void printHelp() const;

  wire::Role role_ = wire::Role::Start;
  bool espNowEnabled_ = false;
  const Uplink *uplink_ = nullptr;
  const UplinkEspNow *uplinkEspNow_ = nullptr;

  char lineBuf_[64];
  size_t lineLen_ = 0;
};
