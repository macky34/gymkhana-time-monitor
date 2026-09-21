#include "board.h"

#include <Arduino.h>

#include "config.h"

#ifndef USE_EXTERNAL_ANTENNA
#define USE_EXTERNAL_ANTENNA 0
#endif

namespace board {

void earlyInit() {
#if defined(CONFIG_IDF_TARGET_ESP32C6)
  // XIAO ESP32C6 antenna select (Seeed Wiki): GPIO3 enables the RF switch
  // (active low), GPIO14 picks onboard chip antenna (LOW) vs external u.FL
  // (HIGH). Must run before WiFi.begin().
  pinMode(3, OUTPUT);
  digitalWrite(3, LOW);
  pinMode(14, OUTPUT);
  digitalWrite(14, USE_EXTERNAL_ANTENNA ? HIGH : LOW);
#endif
  // C5 has no GPIO-controlled antenna switch (fixed onboard u.FL antenna),
  // so USE_EXTERNAL_ANTENNA is a no-op there.
}

}  // namespace board
