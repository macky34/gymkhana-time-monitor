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
  TimeReq = 5,   // client -> host: t1 (client's mono clock at send time)
  TimeResp = 6,  // host -> client: t1 echoed back, plus t2rx/t2tx (host's
                 // wall clock at recv/send time) -- the 4-point NTP exchange
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

// 4-point NTP-style exchange (see Sensor-Device wiki page's time sync
// section for the offset/delay formulas). t1/t2rx/t2tx are raw esp_timer /
// wall-clock microsecond readings, not yet combined into an offset.
size_t encodeTimeReq(uint8_t *out, size_t cap, int64_t t1);
bool decodeTimeReq(const uint8_t *data, size_t len, int64_t *outT1);

struct TimeRespInfo {
  int64_t t1;    // echoed back from the TimeReq
  int64_t t2rx;  // host wall clock, recv callback entry
  int64_t t2tx;  // host wall clock, just before esp_now_send()
};
size_t encodeTimeResp(uint8_t *out, size_t cap, const TimeRespInfo &info);
bool decodeTimeResp(const uint8_t *data, size_t len, TimeRespInfo *out);

}  // namespace linkproto
