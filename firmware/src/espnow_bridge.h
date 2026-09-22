// EspNowBridge: the host side of the ESP-NOW relay. Answers client
// Discover broadcasts, forwards relayed client payloads to the server, and
// sends periodic Announce beacons. UplinkWifi owns one of these
// unconditionally (a WiFi-direct sensor always doubles as a potential
// relay host, per the plan). Phase 3 scope: hb relay + Discover/Announce
// only; multi-client tracking and trigger relay land in Phase 4.
#pragma once

#include <cstddef>
#include <cstdint>

#include "espnow_radio.h"
#include "wire.h"

// C-style callback (not std::function) so EspNowBridge doesn't need to know
// about UplinkWifi's type; ctx is the UplinkWifi instance, cast back inside
// the callback.
using RelayToServerFn = bool (*)(void *ctx, const char *payload, size_t len);

class EspNowBridge {
 public:
  void begin(wire::Role hostRole, RelayToServerFn relayFn, void *relayCtx);

  // Call every loop() iteration: drains the radio's receive queue and sends
  // Announce beacons every 2s.
  void loop(uint32_t nowMs);

  // Keeps Announce's uplinkUp field accurate; call whenever UplinkWifi's
  // connectivity to the server changes.
  void setUplinkUp(bool up);

  // Forwards a server "config" reply (verbatim JSON) to the last client
  // seen (Phase 3: single client only).
  void forwardConfigToClient(const char *payload, size_t len);

 private:
  void handlePacket(const EspNowPacket &pkt);
  void sendAnnounce(const uint8_t *toMac);

  EspNowRadio radio_;
  wire::Role hostRole_ = wire::Role::Start;
  bool uplinkUp_ = false;
  uint32_t lastAnnounceMs_ = 0;
  RelayToServerFn relayFn_ = nullptr;
  void *relayCtx_ = nullptr;

  uint8_t lastClientMac_[6] = {0};
  bool haveClient_ = false;
};
