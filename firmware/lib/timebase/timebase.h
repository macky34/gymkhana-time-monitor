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
  // Hard-sets the anchor with no correction slewing. Used for the very
  // first sync (nothing to slew away from yet); a later periodic resync
  // should go through reanchorSlewed() instead so it can't step a
  // timestamp mid-run.
  void anchor(int64_t wallUs, int64_t monoUs) {
    wallAnchorUs_ = wallUs;
    monoAnchorUs_ = monoUs;
    slewMonoUs_ = monoUs;
    slewRemainingUs_ = 0;
    synced_ = true;
  }

  // Re-anchors from a fresh (wallUs, monoUs) sample without stepping: the
  // gap versus the current estimate is corrected gradually, at up to
  // kMaxSlewPpm, so toWallUs() stays continuous across a periodic resync
  // even if one lands mid-run. Falls back to a hard anchor() if not
  // already synced() (nothing to slew from).
  void reanchorSlewed(int64_t wallUs, int64_t monoUs) {
    if (!synced_) {
      anchor(wallUs, monoUs);
      return;
    }
    slewRemainingUs_ = wallUs - toWallUs(monoUs);
    slewMonoUs_ = monoUs;
  }

  void invalidate() { synced_ = false; }

  bool synced() const { return synced_; }

  int64_t toWallUs(int64_t monoUs) const {
    int64_t base = wallAnchorUs_ + (monoUs - monoAnchorUs_);
    if (slewRemainingUs_ == 0) return base;

    int64_t elapsedUs = monoUs - slewMonoUs_;
    if (elapsedUs <= 0) return base;

    int64_t maxAppliedUs = elapsedUs * kMaxSlewPpm / 1000000;
    int64_t applied = slewRemainingUs_ >= 0
        ? (maxAppliedUs < slewRemainingUs_ ? maxAppliedUs : slewRemainingUs_)
        : (-maxAppliedUs > slewRemainingUs_ ? -maxAppliedUs : slewRemainingUs_);
    return base + applied;
  }

 private:
  // Correction rate cap for reanchorSlewed(), in parts per million (µs of
  // correction per second of elapsed mono time). A typical resync gap is a
  // few ms, so this converges within several seconds while adding well
  // under 1µs of extra error over any single trigger-to-relay window.
  static constexpr int64_t kMaxSlewPpm = 500;

  int64_t wallAnchorUs_ = 0;
  int64_t monoAnchorUs_ = 0;
  int64_t slewMonoUs_ = 0;
  int64_t slewRemainingUs_ = 0;
  bool synced_ = false;
};
