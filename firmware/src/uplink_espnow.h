// UplinkEspNow: Phase 3 minimal ESP-NOW client. Fixed host MAC + fixed
// channel (config.h) -- no discovery sweep yet (that's Phase 4). Relays hb
// only; trigger send is naturally gated off since timebase() never syncs
// here (time sync via TimeReq/TimeResp lands in Phase 5), matching
// drainEdges()'s existing "drop while unsynced" behavior in main.cpp.
#pragma once

#include "espnow_radio.h"
#include "uplink.h"
#include "wire.h"

class UplinkEspNow : public Uplink {
 public:
  void setRole(wire::Role role) { role_ = role; }

  void begin() override;
  void loop(uint32_t nowMs) override;

  UplinkState state() const override { return state_; }
  const TimeBase &timebase() const override { return timebase_; }
  uint32_t lockoutMs() const override { return lockoutMs_; }

  bool sendToServer(const char *payload, size_t len, Redundancy r) override;

  // Whether the last Announce from our host reported its own uplink to the
  // server as up. Drives the pilot lamp's UplinkDown background pattern.
  bool hostUplinkUp() const { return hostUplinkUp_; }

 private:
  void handlePacket(const EspNowPacket &pkt, uint32_t nowMs);
  void sendDiscover();

  EspNowRadio radio_;
  wire::Role role_ = wire::Role::Start;
  UplinkState state_ = UplinkState::Init;
  TimeBase timebase_;
  uint32_t lockoutMs_ = 10000;

  uint8_t hostMac_[6] = {0};
  bool haveHost_ = false;
  bool hostUplinkUp_ = false;
  uint32_t lastDiscoverMs_ = 0;
  uint32_t lastAnnounceRxMs_ = 0;
};
