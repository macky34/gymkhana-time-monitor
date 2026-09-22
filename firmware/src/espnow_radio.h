// EspNowRadio: thin wrapper around esp_now. NOTE: the receive callback is a
// single C function pointer shared by the whole esp_now driver, so this
// class keeps its receive queue in a file-scope static -- only one
// EspNowRadio is ever instantiated per device (host or client, never both),
// so this is fine in practice despite looking like a global.
#pragma once

#include <cstddef>
#include <cstdint>

// v1.0 hardware payload cap; Relay/Config payloads are well under this.
constexpr size_t kEspNowMaxPayload = 250;
extern const uint8_t kEspNowBroadcastMac[6];

struct EspNowPacket {
  uint8_t mac[6];
  uint8_t data[kEspNowMaxPayload];
  size_t len;
  int8_t rssi;
  // Wall-clock microseconds, captured in the recv callback (as close to
  // wire arrival as this stack gets) -- this is TimeResp's t2rx.
  int64_t rxTimestampUs;
};

class EspNowRadio {
 public:
  // Assumes WiFi.mode(WIFI_STA) has already run. Also registers the
  // broadcast address as a peer (needed for Discover/Announce).
  bool begin();

  bool addPeer(const uint8_t mac[6]);
  bool send(const uint8_t mac[6], const uint8_t *data, size_t len);
  bool sendBroadcast(const uint8_t *data, size_t len);

  // Non-blocking pop from the receive queue. Returns false if empty.
  bool poll(EspNowPacket *out);

  // Adds WIFI_PROTOCOL_LR to the STA protocol bitmap (kept alongside
  // 11b/g/n, not instead of -- LR devices still need to interoperate with
  // any legacy-rate peer). Returns false (protocol left unchanged) if the
  // underlying esp_wifi_set_protocol() call fails; callers should treat
  // that as "stay on normal rate" rather than a fatal error.
  bool enableLongRange();
};
