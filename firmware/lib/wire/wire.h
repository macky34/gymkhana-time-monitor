// wire: the sensor <-> server UDP JSON protocol. This is the single place
// that knows the exact byte layout the server (internal/timing/timing.go)
// expects, so both firmware code and native unit tests share one
// implementation instead of two copies drifting apart.
//
// No Arduino.h dependency: buildable and testable under `pio test -e native`.
#pragma once

#include <cstddef>
#include <cstdint>

namespace wire {

enum class Role : uint8_t { Start = 0, Goal = 1 };

// "start" / "goal", matching the server's validSensorID().
const char *roleName(Role r);

// Writes a trigger JSON packet into out (size cap, NUL-terminated on
// success). Returns the written length excluding the NUL terminator, or 0 if
// cap was too small (nothing is written in that case). Byte-for-byte
// compatible with the original String-concatenation implementation in
// main.cpp, e.g.:
//   {"type":"trigger","sensor_id":"start","boot_id":123,"seq":1,"timestamp_us":456}
size_t buildTrigger(char *out, size_t cap, Role role, uint32_t bootId,
                     uint32_t seq, int64_t tsWallUs);

// Writes a heartbeat JSON packet into out. ntpOffsetMs is formatted with one
// decimal place (matching the original fixed "0.0" literal when the value is
// zero); a real measurement can be passed once one exists (Phase 5).
//   {"type":"hb","sensor_id":"goal","boot_id":123,"seq":42,"ntp_offset_ms":0.0}
size_t buildHeartbeat(char *out, size_t cap, Role role, uint32_t bootId,
                      uint32_t seq, double ntpOffsetMs);

// Scans a "config" reply body for a "lockout_sec":N field, matching the
// original parseLockoutMs()'s tolerant substring-scan behavior (no JSON
// parser, no "type" field check). On success writes the value converted to
// milliseconds (rounded) to *outMs and returns true. Leaves *outMs untouched
// and returns false if the field is missing, unparsable, or <= 0.
bool parseLockoutMs(const char *body, size_t len, uint32_t *outMs);

// True if the first byte of the buffer is '{' (the server's cheap pre-filter
// before attempting a JSON parse; timing.go:246).
bool looksLikeServerJson(const char *p, size_t len);

}  // namespace wire
