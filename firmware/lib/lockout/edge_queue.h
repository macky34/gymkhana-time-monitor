// EdgeQueue: a lock-free single-producer/single-consumer ring buffer for
// handing edge timestamps from an ISR (producer, pushFromIsr()) to the main
// loop (consumer, pop()). Replaces the previous single-slot
// volatile+critical-section design, which silently overwrote a pending edge
// if a second one arrived before loop() drained it (the header comment
// called it a "ring buffer" but the implementation was a single slot).
//
// Header-only template, no Arduino.h dependency: usable from both firmware
// and native unit tests. Safe to call pushFromIsr() from an ISR because it
// only touches std::atomic<size_t> indices with acquire/release ordering --
// no locks, no blocking.
#pragma once

#include <atomic>
#include <cstddef>
#include <cstdint>

template <size_t N>
class EdgeQueue {
 public:
  // Called from the ISR. Returns false (and counts a drop) if the queue is
  // full; the caller decides whether that's worth logging once back in
  // loop().
  bool pushFromIsr(int64_t monoUs) {
    size_t head = head_.load(std::memory_order_relaxed);
    size_t nextHead = (head + 1) % N;
    if (nextHead == tail_.load(std::memory_order_acquire)) {
      dropped_.fetch_add(1, std::memory_order_relaxed);
      return false;
    }
    buf_[head] = monoUs;
    head_.store(nextHead, std::memory_order_release);
    return true;
  }

  // Called from loop(). Returns false if the queue is empty.
  bool pop(int64_t *monoUs) {
    size_t tail = tail_.load(std::memory_order_relaxed);
    if (tail == head_.load(std::memory_order_acquire)) {
      return false;
    }
    *monoUs = buf_[tail];
    tail_.store((tail + 1) % N, std::memory_order_release);
    return true;
  }

  uint32_t dropped() const { return dropped_.load(std::memory_order_relaxed); }

 private:
  int64_t buf_[N] = {};
  std::atomic<size_t> head_{0};
  std::atomic<size_t> tail_{0};
  std::atomic<uint32_t> dropped_{0};
};
