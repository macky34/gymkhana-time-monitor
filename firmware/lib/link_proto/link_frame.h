// link_frame: the ESP-NOW frame format between a client sensor and the host
// sensor bridging it to the server. No Arduino.h dependency (pio test -e
// native). Frame layout: 1-byte FrameType header, then a type-specific body.
#pragma once

#include <cstddef>
#include <cstdint>

#include "wire.h"

namespace linkproto {

enum class FrameType : uint8_t {
  Discover = 1,  // client -> broadcast: looking for a host
  Announce = 2,  // host -> broadcast, every 2s: host info + status
  Relay = 3,     // client -> host: trigger/hb JSON, forwarded verbatim
  Config = 4,    // host -> client: server's config reply, forwarded verbatim
};

// Reads just the type byte. Returns false if data is empty.
bool peekType(const uint8_t *data, size_t len, FrameType *outType);

// Discover carries the client's own role so a host can detect a role
// collision (both devices claiming to be e.g. "start").
size_t encodeDiscover(uint8_t *out, size_t cap, wire::Role clientRole);
bool decodeDiscover(const uint8_t *data, size_t len, wire::Role *outRole);

struct AnnounceInfo {
  uint8_t channel;
  wire::Role hostRole;
  bool uplinkUp;  // is the host currently connected to the server?
};

size_t encodeAnnounce(uint8_t *out, size_t cap, const AnnounceInfo &info);
bool decodeAnnounce(const uint8_t *data, size_t len, AnnounceInfo *out);

// Relay/Config just wrap a JSON payload verbatim (no copy on decode: the
// returned pointer aliases into `data`, valid only as long as `data` is).
size_t encodeRelay(uint8_t *out, size_t cap, const char *payload, size_t payloadLen);
size_t decodeRelay(const uint8_t *data, size_t len, const char **outPayload);

size_t encodeConfig(uint8_t *out, size_t cap, const char *payload, size_t payloadLen);
size_t decodeConfig(const uint8_t *data, size_t len, const char **outPayload);

}  // namespace linkproto
