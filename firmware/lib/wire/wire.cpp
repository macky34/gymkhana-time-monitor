#include "wire.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>

namespace wire {

const char *roleName(Role r) { return r == Role::Start ? "start" : "goal"; }

size_t buildTrigger(char *out, size_t cap, Role role, uint32_t bootId,
                     uint32_t seq, int64_t tsWallUs) {
  int n = snprintf(out, cap,
                    "{\"type\":\"trigger\",\"sensor_id\":\"%s\","
                    "\"boot_id\":%u,\"seq\":%u,\"timestamp_us\":%lld}",
                    roleName(role), bootId, seq, (long long)tsWallUs);
  if (n < 0 || (size_t)n >= cap) return 0;
  return (size_t)n;
}

size_t buildHeartbeat(char *out, size_t cap, Role role, uint32_t bootId,
                      uint32_t seq, double ntpOffsetMs) {
  int n = snprintf(out, cap,
                    "{\"type\":\"hb\",\"sensor_id\":\"%s\","
                    "\"boot_id\":%u,\"seq\":%u,\"ntp_offset_ms\":%.1f}",
                    roleName(role), bootId, seq, ntpOffsetMs);
  if (n < 0 || (size_t)n >= cap) return 0;
  return (size_t)n;
}

bool parseLockoutMs(const char *body, size_t len, uint32_t *outMs) {
  static const char kNeedle[] = "lockout_sec";
  const size_t needleLen = sizeof(kNeedle) - 1;
  const char *end = body + len;

  const char *found = nullptr;
  for (const char *p = body; p + needleLen <= end; p++) {
    if (memcmp(p, kNeedle, needleLen) == 0) {
      found = p;
      break;
    }
  }
  if (!found) return false;

  const char *colon = (const char *)memchr(found, ':', end - found);
  if (!colon) return false;

  const char *numStart = colon + 1;
  size_t avail = (size_t)(end - numStart);
  if (avail == 0) return false;

  // strtod() needs a NUL-terminated (or otherwise bounded-by-content) string;
  // body may not be NUL-terminated, so copy the remainder into a bounded
  // scratch buffer. A lockout value never needs more than a handful of
  // digits, so a small fixed buffer is plenty.
  char buf[64];
  size_t n = avail < sizeof(buf) - 1 ? avail : sizeof(buf) - 1;
  memcpy(buf, numStart, n);
  buf[n] = '\0';

  char *strEnd = nullptr;
  double sec = strtod(buf, &strEnd);
  if (strEnd == buf) return false;  // no digits consumed
  if (sec <= 0) return false;

  *outMs = (uint32_t)(sec * 1000.0 + 0.5);
  return true;
}

bool looksLikeServerJson(const char *p, size_t len) {
  return len > 0 && p[0] == '{';
}

}  // namespace wire
