// OffsetFilter: NTP minimum filter over 4-point exchange samples (see
// link_frame.h's TimeReq/TimeResp). Keeps only the lowest-delay sample --
// that one has the least asymmetric-path/queuing distortion, so its offset
// estimate is the most trustworthy. Samples with delay outside [0,
// kMaxDelayUs] are rejected outright.
#pragma once

#include <cstdint>

class OffsetFilter {
 public:
  static constexpr int64_t kMaxDelayUs = 20000;  // 20ms

  void reset();

  // Feeds one sample: t1 (client mono, send), t2rx/t2tx (host wall,
  // recv/send), t3 (client mono, recv). Returns false if delay is negative
  // or exceeds kMaxDelayUs (sample rejected, best-so-far unchanged).
  bool addSample(int64_t t1, int64_t t2rx, int64_t t2tx, int64_t t3);

  bool hasSample() const { return haveBest_; }

  // Offset (add to a client mono timestamp to get the host's wall time)
  // and round-trip delay of the best (lowest-delay) sample since reset().
  int64_t bestOffsetUs() const { return bestOffsetUs_; }
  int64_t bestDelayUs() const { return bestDelayUs_; }

 private:
  bool haveBest_ = false;
  int64_t bestDelayUs_ = 0;
  int64_t bestOffsetUs_ = 0;
};
