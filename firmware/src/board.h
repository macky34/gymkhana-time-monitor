// board: per-chip setup that has to run before WiFi.begin() and doesn't
// belong in the uplink/sensor logic. Currently just the XIAO ESP32C6
// antenna-select GPIOs; the C5 has no antenna switch (fixed onboard u.FL)
// so board::earlyInit() is a no-op there.
#pragma once

namespace board {

// Must run before WiFi.begin(). On the C6, configures the RF switch enable
// and antenna-select GPIOs (see config.h's USE_EXTERNAL_ANTENNA). No-op on
// the C5.
void earlyInit();

}  // namespace board
