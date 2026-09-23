// UplinkEspNow: ESP-NOW client. Sweeps channels 1-13 (150ms dwell each)
// broadcasting Discover until a host's Announce is heard, then locks onto
// that channel and that host's MAC -- packets from any other sender are
// ignored from then on. Detects a role collision (host announcing the same
// role as this client) and refuses to link. Once linked, a trigger
// (Burst3) is relayed as a 3-packet burst (50ms apart) and also queues for
// up to 3s if the link isn't usable yet; hb (Once) is sent at most once and
// never queued. Re-sweeps if the host's Announce goes quiet for 10s or 5
// relay sends in a row fail at the radio layer.
//
// Time sync: once linked, exchanges 5 TimeReq/TimeResp round trips (see
// link_frame.h), keeps the lowest-delay sample (lib/timebase's
// OffsetFilter, an NTP minimum filter), and anchors timebase() from it.
// Re-syncs every 5 minutes; a resync is slewed (TimeBase::reanchorSlewed),
// never stepped, so it can't jump a timestamp mid-run.
#pragma once

#include "espnow_radio.h"
#include "offset_filter.h"
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
  double ntpOffsetMs() const override { return ntpOffsetMs_; }

  // Whether the last Announce from our host reported its own uplink to the
  // server as up. Drives the pilot lamp's UplinkDown background pattern.
  bool hostUplinkUp() const { return hostUplinkUp_; }

  // True once we've heard a host announcing the same role as us -- a
  // configuration mistake (both sensors set to e.g. "start"). We refuse to
  // link with that host.
  bool roleCollision() const { return roleCollision_; }

  // RSSI of the last Announce heard from our host (dBm; 0 if none yet).
  int8_t rssi() const { return lastRssi_; }

  // WiFi channel our host is on, from its last Announce (0 if none yet).
  // Diagnostic only (see `status`'s printout) -- the client can't act on a
  // channel change it hears about after the fact, since a channel switch is
  // itself why the Announce carrying it would go unheard.
  uint8_t hostChannel() const { return hostChannel_; }

 private:
  void handlePacket(const EspNowPacket &pkt, uint32_t nowMs);
  void sendDiscover();
  void sweepChannel(uint32_t nowMs);
  bool sendRelay(const char *payload, size_t len);
  void pollPendingTrigger(uint32_t nowMs);
  bool linkUsable() const;

  void startTimeSync(uint32_t nowMs);
  void sendTimeReq(uint32_t nowMs);
  void pollTimeSync(uint32_t nowMs);
  void finishTimeSync(uint32_t nowMs);

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
  uint8_t hostChannel_ = 0;
  uint32_t lastAnnounceRxMs_ = 0;

  uint8_t currentChannel_ = 1;
  uint32_t channelSwitchMs_ = 0;
  static constexpr uint32_t kChannelDwellMs = 150;
  static constexpr uint32_t kAnnounceTimeoutMs = 10000;  // re-sweep if host goes quiet
  static constexpr uint32_t kSendFailStreakLimit = 5;  // re-sweep if reached

  // Non-blocking 3-packet relay burst for Redundancy::Burst3 (trigger),
  // separate from pendingTrigger_ (which only applies before the link is
  // usable at all): mirrors UplinkWifi's burst_, so a lost ESP-NOW frame
  // doesn't lose the timing. hb (Once) is never queued here.
  struct {
    char payload[192];
    size_t len = 0;
    uint8_t remaining = 0;
    uint32_t nextAtMs = 0;
    bool active = false;
  } relayBurst_;
  void pollRelayBurst(uint32_t nowMs);

  // Holds a single trigger payload (Burst3 only) while the link isn't
  // usable yet, retrying for up to kTriggerRetryMs before giving up.
  struct {
    char payload[192];
    size_t len = 0;
    uint32_t queuedAtMs = 0;
    bool active = false;
  } pendingTrigger_;
  static constexpr uint32_t kTriggerRetryMs = 3000;

  // 4-point time sync (TimeReq/TimeResp).
  enum class SyncPhase : uint8_t { Idle, Exchanging };
  OffsetFilter offsetFilter_;
  SyncPhase syncPhase_ = SyncPhase::Idle;
  int syncExchangesDone_ = 0;
  uint32_t syncExchangeStartMs_ = 0;
  uint32_t lastSyncStartMs_ = 0;  // 0 = never yet synced
  double ntpOffsetMs_ = 0.0;
  static constexpr int kSyncExchangeCount = 5;
  static constexpr uint32_t kSyncExchangeTimeoutMs = 200;
  static constexpr uint32_t kResyncIntervalMs = 5UL * 60UL * 1000UL;  // 5 min
};
