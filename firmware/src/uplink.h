// Uplink: how this device gets trigger/heartbeat packets to the server and
// gets the "config" (lockout_sec) reply back. UplinkWifi (this phase) talks
// to the RPi directly; UplinkEspNow (Phase 4) will relay through a host
// device instead. main.cpp's loop() drives whichever one is active through
// this same interface, so the rest of the firmware (edge detection,
// lockout, LED) doesn't need to know which uplink is in use.
#pragma once

#include <cstddef>
#include <cstdint>

#include "timebase.h"

// Coarse status used to drive the status LED and to gate whether it's safe
// to send triggers (only once synced()'s TimeBase is valid).
enum class UplinkState : uint8_t {
  Init,        // not started yet
  Connecting,  // acquiring the transport (WiFi association / ESP-NOW peer)
  Syncing,     // transport is up, waiting for a wall-clock anchor
  Ready,       // synced; triggers may be sent
  Failed,      // transport is up but sync failed; retried on the next cycle
};

// How many times to send a payload. Burst3 matches the original 3-packet,
// 50ms-apart resend the server's dedup key (sensor_id, boot_id, seq)
// tolerates (internal/timing/timing.go treats the 2nd/3rd copy as a
// duplicate and silently drops it).
enum class Redundancy : uint8_t { Once, Burst3 };

class Uplink {
 public:
  virtual ~Uplink() = default;

  // Starts (or restarts) acquiring the transport. Non-blocking: call loop()
  // afterwards to drive the state machine forward.
  virtual void begin() = 0;

  // Advances the state machine. Must be cheap and non-blocking -- never
  // delay()/block here, since a bridging host (Phase 3+) needs loop() to
  // keep running to service ESP-NOW while this is mid-reconnect.
  virtual void loop(uint32_t nowMs) = 0;

  virtual UplinkState state() const = 0;
  virtual const TimeBase &timebase() const = 0;

  // Current debounce lockout window in milliseconds (server-supplied; see
  // lib/wire's parseLockoutMs).
  virtual uint32_t lockoutMs() const = 0;

  // Queues payload for delivery to the server. Non-blocking: with
  // Redundancy::Burst3, the 2nd/3rd copies are sent on subsequent loop()
  // calls rather than via delay(). Returns false if payload/len don't fit
  // the implementation's send buffer (nothing is queued in that case).
  virtual bool sendToServer(const char *payload, size_t len, Redundancy r) = 0;
};
