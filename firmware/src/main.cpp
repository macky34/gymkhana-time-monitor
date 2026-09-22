// timemon sensor firmware (ESP32 + Arduino framework).
//
// Lifecycle (see the Sensor-Device wiki page):
//   WiFi connect -> SNTP sync against chrony@RPi -> fetch lockout config ->
//   ready. Trigger sending is inhibited until the clock is synced; the
//   status LED blinks while unsynced and is solid once ready.
//
// A falling edge on SENSOR_GPIO (beam broken) is timestamped inside the ISR
// with esp_timer_get_time() and pushed to a lock-free queue (EdgeQueue); the
// main loop drains it, applies the debounce lockout (first edge wins), and
// sends the trigger as a 3-packet UDP burst (50 ms apart, non-blocking) so a
// single lost datagram does not lose the timing. The wire format matches the
// Sensor-Device wiki page.
//
// This file is just the setup()/loop() orchestrator; the actual logic lives
// in lib/ (Arduino-independent, unit-tested under `pio test -e native`) and
// the other src/ modules (board/status_led/identity/uplink_wifi).
#include <Arduino.h>

#include "board.h"
#include "config.h"
#include "debug_monitor.h"
#include "edge_queue.h"
#include "identity.h"
#include "link_switch.h"
#include "lockout.h"
#include "mode_switch.h"
#include "pilot_indicator.h"
#include "status_led.h"
#include "uplink.h"
#include "uplink_espnow.h"
#include "uplink_wifi.h"
#include "wire.h"

#ifndef LED_ACTIVE_LOW
#define LED_ACTIVE_LOW 0
#endif

#if defined(DEBUG_FORCE_ROLE_START) && defined(DEBUG_FORCE_ROLE_GOAL)
#error "DEBUG_FORCE_ROLE_START and DEBUG_FORCE_ROLE_GOAL are mutually exclusive"
#endif
#if defined(DEBUG_FORCE_LINK_ESPNOW) && defined(DEBUG_FORCE_LINK_WIFI)
#error "DEBUG_FORCE_LINK_ESPNOW and DEBUG_FORCE_LINK_WIFI are mutually exclusive"
#endif

#ifdef DEBUG_VERBOSE
static DebugMonitor debugMonitor;
#endif

static StatusLed statusLed;
static PilotIndicator pilotLed;
static ModeSwitch modeSwitch;
static LinkSwitch linkSwitch;
static UplinkWifi uplinkWifi;
static UplinkEspNow uplinkEspNow;
static Uplink *uplink = &uplinkWifi;  // picked in setup() based on linkSwitch
static Identity identity;
static Lockout lockout;
static EdgeQueue<8> edgeQueue;
static uint32_t lastTriggerAcceptedMs = 0;  // 0 = never; used to hold off a
                                             // role-change restart mid-run.

void IRAM_ATTR onEdge() {
  // Minimal ISR: just timestamp the edge and hand it to loop() via the
  // lock-free queue. No debounce, no sending here.
  edgeQueue.pushFromIsr(esp_timer_get_time());
}

static LedPattern patternFor(UplinkState s) {
  switch (s) {
    case UplinkState::Init:
    case UplinkState::Connecting:
      return LedPattern::BlinkFast;
    case UplinkState::Syncing:
      return LedPattern::BlinkSlow;
    case UplinkState::Ready:
      return LedPattern::Solid;
    case UplinkState::Failed:
      return LedPattern::Off;
  }
  return LedPattern::Off;
}

static void drainEdges(uint32_t nowMs) {
  lockout.setWindowMs(uplink->lockoutMs());

  int64_t edgeMono;
  while (edgeQueue.pop(&edgeMono)) {
    // Edges are dropped entirely while unsynced (same as the original
    // clockSynced check) -- not even the lockout window is updated, so the
    // first edge after sync still gets sent.
    if (!uplink->timebase().synced()) continue;
    if (!lockout.accept(edgeMono)) continue;  // inside the debounce window

    int64_t tsWallUs = uplink->timebase().toWallUs(edgeMono);
    uint32_t seq = identity.nextTriggerSeq();
    char payload[192];
    size_t len = wire::buildTrigger(payload, sizeof(payload), identity.role(),
                                     identity.bootId(), seq, tsWallUs);
    if (len == 0) continue;  // shouldn't happen; buffer is sized generously
    uplink->sendToServer(payload, len, Redundancy::Burst3);
    pilotLed.flash(nowMs);
    lastTriggerAcceptedMs = nowMs;
    Serial.printf("[trigger] seq=%u ts=%lld\n", seq, (long long)tsWallUs);
  }
}

static void maybeHeartbeat(uint32_t nowMs) {
  static uint32_t lastHbMs = 0;
  if (nowMs - lastHbMs < 5000) return;
  lastHbMs = nowMs;

  uint32_t seq = identity.nextHbSeq();
  char payload[192];
  // ntp_offset_ms: best-effort estimate; 0 is acceptable when we cannot
  // measure it (the server treats it as informational). A WiFi-direct
  // uplink never measures it; an ESP-NOW client will (Phase 5).
  size_t len = wire::buildHeartbeat(payload, sizeof(payload), identity.role(),
                                    identity.bootId(), seq, 0.0);
  if (len == 0) return;
  uplink->sendToServer(payload, len, Redundancy::Once);
}

// Reflects the ESP-NOW client's link state on the pilot lamp's background
// layer; a WiFi-direct device (including a relay host) always shows None.
static void updateLinkBackground() {
  if (uplink != static_cast<Uplink *>(&uplinkEspNow)) {
    pilotLed.setLinkBackground(PilotIndicator::LinkBackground::None);
    return;
  }
  if (uplinkEspNow.roleCollision()) {
    pilotLed.setLinkBackground(PilotIndicator::LinkBackground::RoleCollision);
  } else if (uplinkEspNow.state() != UplinkState::Ready) {
    pilotLed.setLinkBackground(PilotIndicator::LinkBackground::Searching);
  } else if (!uplinkEspNow.hostUplinkUp()) {
    pilotLed.setLinkBackground(PilotIndicator::LinkBackground::UplinkDown);
  } else {
    pilotLed.setLinkBackground(PilotIndicator::LinkBackground::Linked);
  }
}

// Every 5s (matching the hb interval) while ESP-NOW-linked, pulses the
// pilot lamp to show signal strength: 1 pulse = strong, 2 = medium, 3 =
// weak (thresholds are rough dBm bands, not calibrated to any spec).
static void maybeShowRssi(uint32_t nowMs) {
  static uint32_t lastRssiMs = 0;
  if (uplink != static_cast<Uplink *>(&uplinkEspNow)) return;
  if (uplinkEspNow.state() != UplinkState::Ready) return;
  if (nowMs - lastRssiMs < 5000) return;
  lastRssiMs = nowMs;

  int8_t rssi = uplinkEspNow.rssi();
  int pulses = (rssi >= -60) ? 1 : (rssi >= -75) ? 2 : 3;
  pilotLed.startPulses(nowMs, pulses);
}

// Restarts on a confirmed role- or link-switch flip, unless a trigger was
// accepted within the current lockout window (stays pending and retries
// next loop()).
static void maybeRestartOnSwitchChange(uint32_t nowMs) {
  if (!modeSwitch.changed() && !linkSwitch.changed()) return;
  bool recentlyTriggered = lastTriggerAcceptedMs != 0 &&
      (nowMs - lastTriggerAcceptedMs) < lockout.windowMs();
  if (recentlyTriggered) return;
  Serial.println("[mode] role changed, restarting");
  delay(50);
  ESP.restart();
}

void setup() {
  Serial.begin(115200);

  board::earlyInit();  // must run before WiFi.begin()

  statusLed.begin(STATUS_LED_GPIO, LED_ACTIVE_LOW);
  statusLed.set(LedPattern::BlinkFast);
  statusLed.poll(millis());
  pilotLed.begin(PILOT_LED_GPIO);
  modeSwitch.begin(MODE_SWITCH_GPIO);
  linkSwitch.begin(LINK_SWITCH_GPIO);
  wire::Role role = modeSwitch.role();
#if defined(DEBUG_FORCE_ROLE_START)
  role = wire::Role::Start;
#elif defined(DEBUG_FORCE_ROLE_GOAL)
  role = wire::Role::Goal;
#endif
  pilotLed.playRoleIntro(role);

  pinMode(SENSOR_GPIO, INPUT_PULLUP);

  bool espNowEnabled = linkSwitch.espNowEnabled();
#if defined(DEBUG_FORCE_LINK_ESPNOW)
  espNowEnabled = true;
#elif defined(DEBUG_FORCE_LINK_WIFI)
  espNowEnabled = false;
#endif

  uplinkWifi.setRole(role);
  uplinkEspNow.setRole(role);
  uplink = espNowEnabled ? static_cast<Uplink *>(&uplinkEspNow)
                         : static_cast<Uplink *>(&uplinkWifi);
  uplink->begin();
  // Must run after uplink->begin() (which calls WiFi.mode(WIFI_STA)): the RF
  // hardware needs to be initialized for esp_random() to be a true hardware
  // RNG (see identity.h).
  identity.begin(role);

#ifdef DEBUG_VERBOSE
  debugMonitor.begin(&statusLed, &pilotLed, uplink);
#endif

  // Block here (matching the original setup()'s behavior) until the uplink
  // reaches a terminal startup state, driving the same loop-based state
  // machine so the LED updates normally instead of duplicating connect/sync
  // logic here.
  while (uplink->state() != UplinkState::Ready && uplink->state() != UplinkState::Failed) {
    uint32_t now = millis();
    uplink->loop(now);
    statusLed.set(patternFor(uplink->state()));
    statusLed.poll(now);
    delay(1);
  }
  statusLed.set(patternFor(uplink->state()));
  statusLed.poll(millis());

  // Interrupt registration happens after clock sync / config fetch settle,
  // same ordering as the original.
  attachInterrupt(digitalPinToInterrupt(SENSOR_GPIO), onEdge, FALLING);
}

void loop() {
  uint32_t nowMs = millis();

  uplink->loop(nowMs);
  statusLed.set(patternFor(uplink->state()));
  statusLed.poll(nowMs);

  drainEdges(nowMs);
  maybeHeartbeat(nowMs);
  updateLinkBackground();
  maybeShowRssi(nowMs);
  pilotLed.poll(nowMs);

  modeSwitch.poll(nowMs);
  linkSwitch.poll(nowMs);
  maybeRestartOnSwitchChange(nowMs);

#ifdef DEBUG_VERBOSE
  debugMonitor.poll(nowMs);
#endif

  delay(1);
}
