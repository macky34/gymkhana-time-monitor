// timemon sensor firmware (ESP32 + Arduino framework).
//
// Lifecycle (DESIGN.md §11):
//   WiFi connect -> SNTP sync against chrony@RPi -> fetch lockout config ->
//   ready. Trigger sending is inhibited until the clock is synced; the status
//   LED blinks while unsynced and is solid once ready.
//
// A falling edge on SENSOR_GPIO (beam broken) is timestamped inside the ISR
// with esp_timer_get_time() and pushed to a ring buffer; the main loop drains
// it, applies the debounce lockout (first edge wins), and sends the trigger
// as a 3-packet UDP burst (50 ms apart) so a single lost datagram does not
// lose the timing. The wire format matches docs/CONTRACTS.md §4.5.
#include <Arduino.h>
#include <WiFi.h>
#include <WiFiUdp.h>
#include <HTTPClient.h>
#include <time.h>
#include "config.h"

// Back-compat fallback defaults for boards (e.g. esp32dev) whose config.h
// predates these macros.
#ifndef LED_ACTIVE_LOW
#define LED_ACTIVE_LOW 0
#endif
#ifndef USE_EXTERNAL_ANTENNA
#define USE_EXTERNAL_ANTENNA 0
#endif

static WiFiUDP udp;
static uint32_t bootID = 0; // random per boot; part of the dedup key
static uint32_t triggerSeq = 0;
static uint32_t hbSeq = 0;
static uint32_t lockoutMs = DEFAULT_LOCKOUT_MS;
static volatile int64_t lastEdgeUs = 0;
static int64_t lastAcceptedUs = 0; // debounce reference (main-loop side)

// --- ISR: timestamp the edge and hand it to the main loop ------------------
// Kept minimal: read the monotonic clock and store it. Debounce and sending
// happen in loop() so the ISR never blocks.
static volatile int64_t pendingEdgeUs = 0;
static portMUX_TYPE edgeMux = portMUX_INITIALIZER_UNLOCKED;

void IRAM_ATTR onEdge() {
  int64_t t = esp_timer_get_time();
  portENTER_CRITICAL_ISR(&edgeMux);
  pendingEdgeUs = t;
  portEXIT_CRITICAL_ISR(&edgeMux);
}

// Wall-clock microseconds in the chrony/SNTP time base (what the server pairs
// on). esp_timer is only a monotonic uptime clock, so we anchor it to the
// synced wall clock captured at sync time.
static int64_t wallAnchorUs = 0;   // wall-clock us at anchor
static int64_t monoAnchorUs = 0;   // esp_timer us at anchor
static bool clockSynced = false;

static int64_t nowWallUs() {
  struct timeval tv;
  gettimeofday(&tv, nullptr);
  return (int64_t)tv.tv_sec * 1000000LL + tv.tv_usec;
}

// Convert an esp_timer edge timestamp to wall-clock us using the anchor, so
// the value we send is in the server's time base with monotonic precision.
static int64_t edgeToWallUs(int64_t edgeMonoUs) {
  return wallAnchorUs + (edgeMonoUs - monoAnchorUs);
}

static void sendPacket(const String &json) {
  udp.beginPacket(RPI_HOST, RPI_UDP_PORT);
  udp.write((const uint8_t *)json.c_str(), json.length());
  udp.endPacket();
}

static void sendTrigger(int64_t tsWallUs) {
  triggerSeq++;
  String json = String("{\"type\":\"trigger\",\"sensor_id\":\"") + SENSOR_ID +
                "\",\"boot_id\":" + String(bootID) +
                ",\"seq\":" + String(triggerSeq) +
                ",\"timestamp_us\":" + String((long long)tsWallUs) + "}";
  for (int i = 0; i < 3; i++) { // 3-packet burst, 50 ms apart
    sendPacket(json);
    if (i < 2) delay(50);
  }
  Serial.printf("[trigger] seq=%u ts=%lld\n", triggerSeq, (long long)tsWallUs);
}

static void sendHeartbeat() {
  hbSeq++;
  // ntp_offset_ms: best-effort estimate; 0 is acceptable when we cannot
  // measure it (the server treats it as informational).
  String json = String("{\"type\":\"hb\",\"sensor_id\":\"") + SENSOR_ID +
                "\",\"boot_id\":" + String(bootID) +
                ",\"seq\":" + String(hbSeq) +
                ",\"ntp_offset_ms\":0.0}";
  sendPacket(json);
}

static void setLed(bool on) {
  bool level = LED_ACTIVE_LOW ? !on : on;
  digitalWrite(STATUS_LED_GPIO, level ? HIGH : LOW);
}

static void syncClock() {
  // chrony on the RPi answers SNTP; anchor esp_timer to the synced wall clock.
  configTime(0, 0, RPI_HOST);
  struct tm tm;
  clockSynced = false;
  for (int i = 0; i < 40; i++) { // up to ~8s waiting for first sync
    if (getLocalTime(&tm, 200)) {
      wallAnchorUs = nowWallUs();
      monoAnchorUs = esp_timer_get_time();
      clockSynced = true;
      Serial.println("[sync] clock synced");
      return;
    }
    setLed(i % 2); // blink while syncing
    delay(200);
  }
  Serial.println("[sync] FAILED (will retry)");
}

static void fetchConfig() {
  HTTPClient http;
  String url = String("http://") + RPI_HOST + ":" + String(RPI_HTTP_PORT) +
               "/api/internal/sensor-config";
  http.begin(url);
  int code = http.GET();
  if (code == 200) {
    String body = http.getString();
    int idx = body.indexOf("lockout_ms");
    if (idx >= 0) {
      int colon = body.indexOf(':', idx);
      if (colon >= 0) {
        long v = body.substring(colon + 1).toInt();
        if (v > 0) lockoutMs = (uint32_t)v;
      }
    }
    Serial.printf("[config] lockout_ms=%u\n", lockoutMs);
  } else {
    Serial.printf("[config] fetch failed (%d), using default %u\n", code, lockoutMs);
  }
  http.end();
}

void setup() {
  Serial.begin(115200);

#if defined(CONFIG_IDF_TARGET_ESP32C6)
  // XIAO ESP32C6 antenna select (Seeed Wiki): GPIO3 enables the RF switch
  // (active low), GPIO14 picks onboard chip antenna (LOW) vs external u.FL
  // (HIGH). Must run before WiFi.begin().
  pinMode(3, OUTPUT);
  digitalWrite(3, LOW);
  pinMode(14, OUTPUT);
  digitalWrite(14, USE_EXTERNAL_ANTENNA ? HIGH : LOW);
#endif

  pinMode(STATUS_LED_GPIO, OUTPUT);
  pinMode(SENSOR_GPIO, INPUT_PULLUP);
  setLed(false);

  bootID = esp_random();

  WiFi.mode(WIFI_STA);
  WiFi.begin(WIFI_SSID, WIFI_PASS);
  while (WiFi.status() != WL_CONNECTED) {
    setLed(millis() / 250 % 2); // fast blink while connecting
    delay(50);
  }
  Serial.printf("[wifi] connected, ip=%s boot_id=%u\n",
                WiFi.localIP().toString().c_str(), bootID);

  syncClock();
  fetchConfig();

  attachInterrupt(digitalPinToInterrupt(SENSOR_GPIO), onEdge, FALLING);
  setLed(clockSynced); // solid when ready
}

void loop() {
  static uint32_t lastHbMs = 0;
  static uint32_t lastResyncMs = 0;
  uint32_t nowMs = millis();

  // Periodic re-sync (hourly) and WiFi recovery.
  if (WiFi.status() != WL_CONNECTED) {
    setLed(nowMs / 250 % 2);
    WiFi.reconnect();
    delay(200);
    return;
  }
  if (nowMs - lastResyncMs > 3600UL * 1000UL) {
    lastResyncMs = nowMs;
    syncClock();
    setLed(clockSynced);
  }

  // Drain a pending edge (if any) and apply debounce lockout.
  int64_t edge = 0;
  portENTER_CRITICAL(&edgeMux);
  if (pendingEdgeUs != 0) {
    edge = pendingEdgeUs;
    pendingEdgeUs = 0;
  }
  portEXIT_CRITICAL(&edgeMux);

  if (edge != 0 && clockSynced) {
    if (lastAcceptedUs == 0 || (edge - lastAcceptedUs) >= (int64_t)lockoutMs * 1000) {
      lastAcceptedUs = edge;
      sendTrigger(edgeToWallUs(edge)); // first edge wins; timestamp is the edge
    }
    // edges inside the lockout window are dropped (debounce)
  }

  if (nowMs - lastHbMs >= 5000) { // heartbeat every 5s
    lastHbMs = nowMs;
    sendHeartbeat();
  }

  delay(1);
}
