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
};
