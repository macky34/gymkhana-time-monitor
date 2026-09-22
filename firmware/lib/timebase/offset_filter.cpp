#include "offset_filter.h"

void OffsetFilter::reset() {
  haveBest_ = false;
  bestDelayUs_ = 0;
  bestOffsetUs_ = 0;
}

bool OffsetFilter::addSample(int64_t t1, int64_t t2rx, int64_t t2tx, int64_t t3) {
  int64_t delay = (t3 - t1) - (t2tx - t2rx);
  if (delay < 0 || delay > kMaxDelayUs) return false;

  int64_t offset = ((t2rx - t1) + (t2tx - t3)) / 2;

  if (!haveBest_ || delay < bestDelayUs_) {
    haveBest_ = true;
    bestDelayUs_ = delay;
    bestOffsetUs_ = offset;
  }
  return true;
}
