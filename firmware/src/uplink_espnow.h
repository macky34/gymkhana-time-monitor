// UplinkEspNow: ESP-NOW client. Sweeps channels 1-13 (150ms dwell each)
// broadcasting Discover until a host's Announce is heard, then locks onto
// that channel. Detects a role collision (host announcing the same role as
// this client) and refuses to link. A trigger (Burst3) queues for up to 3s
// if the link isn't usable yet, then is dropped; hb (Once) is never queued.
// Time sync via TimeReq/TimeResp lands in Phase 5, so timebase() never
// syncs here -- trigger send is naturally gated off by drainEdges()'s
// existing "drop while unsynced" check in main.cpp, independent of the
// queueing here (which exists for when Phase 5 makes triggers flow).
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

  // True once we've heard a host announcing the same role as us -- a
  // configuration mistake (both sensors set to e.g. "start"). We refuse to
  // link with that host.
  bool roleCollision() const { return roleCollision_; }

  // RSSI of the last Announce heard from our host (dBm; 0 if none yet).
  int8_t rssi() const { return lastRssi_; }

 private:
  void handlePacket(const EspNowPacket &pkt, uint32_t nowMs);
  void sendDiscover();
  void sweepChannel(uint32_t nowMs);
  bool sendRelay(const char *payload, size_t len);
  void pollPendingTrigger(uint32_t nowMs);
  bool linkUsable() const;

  EspNowRadio radio_;
  wire::Role role_ = wire::Role::Start;
  UplinkState state_ = UplinkState::Init;
  TimeBase timebase_;
  uint32_t lockoutMs_ = 10000;

  uint8_t hostMac_[6] = {0};
  bool haveHost_ = false;
  bool hostUplinkUp_ = false;
  bool roleCollision_ = false;
  int8_t lastRssi_ = 0;
  uint32_t lastAnnounceRxMs_ = 0;

  uint8_t currentChannel_ = 1;
  uint32_t channelSwitchMs_ = 0;
  static constexpr uint32_t kChannelDwellMs = 150;
  static constexpr uint32_t kAnnounceTimeoutMs = 10000;  // re-sweep if host goes quiet

  // Holds a single trigger payload (Burst3 only) while the link isn't
  // usable yet, retrying for up to kTriggerRetryMs before giving up.
  struct {
    char payload[192];
    size_t len = 0;
    uint32_t queuedAtMs = 0;
    bool active = false;
  } pendingTrigger_;
  static constexpr uint32_t kTriggerRetryMs = 3000;
};
