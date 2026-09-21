// Lockout: first-edge-wins debounce, matching the original inline logic in
// main.cpp's loop() (lastAcceptedUs==0 sentinel replaced with an explicit
// `have_` flag -- same behavior, clearer intent).
#pragma once

#include <cstdint>

class Lockout {
 public:
  void setWindowMs(uint32_t ms) { windowMs_ = ms; }
  uint32_t windowMs() const { return windowMs_; }

  // Returns true if the edge should be accepted (sent) -- i.e. it's the
  // first edge ever, or at least windowMs() has elapsed since the last
  // accepted edge. Edges inside the window are dropped (debounce) and this
  // returns false.
  bool accept(int64_t edgeMonoUs) {
    if (!have_ || (edgeMonoUs - lastAcceptedUs_) >= (int64_t)windowMs_ * 1000) {
      have_ = true;
      lastAcceptedUs_ = edgeMonoUs;
      return true;
    }
    return false;
  }

  void reset() {
    have_ = false;
    lastAcceptedUs_ = 0;
  }

 private:
  bool have_ = false;
  int64_t lastAcceptedUs_ = 0;
  uint32_t windowMs_ = 10000;
};
