// TimeBase: anchors a monotonic clock reading to a wall-clock (server time
// base) reading, so later monotonic timestamps can be converted to wall-clock
// microseconds without depending on the wall clock being read again (it may
// drift or step between syncs). Matches the original wallAnchorUs/
// monoAnchorUs/clockSynced globals and edgeToWallUs() in main.cpp.
//
// The anchor source differs by uplink: WiFi-direct anchors from SNTP against
// chrony@RPi; an ESP-NOW client anchors from the offset estimated via
// TimeReq/TimeResp exchange with its host (see lib/timebase/offset_filter.h,
// added in Phase 5). Both use this same class.
#pragma once

#include <cstdint>

class TimeBase {
 public:
  void anchor(int64_t wallUs, int64_t monoUs) {
    wallAnchorUs_ = wallUs;
    monoAnchorUs_ = monoUs;
    synced_ = true;
  }

  void invalidate() { synced_ = false; }

  bool synced() const { return synced_; }

  int64_t toWallUs(int64_t monoUs) const {
    return wallAnchorUs_ + (monoUs - monoAnchorUs_);
  }

 private:
  int64_t wallAnchorUs_ = 0;
  int64_t monoAnchorUs_ = 0;
  bool synced_ = false;
};
