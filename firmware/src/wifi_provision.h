// WifiProvision: resolves the WiFi SSID/password to connect with.
//
// If config.h #defines WIFI_SSID/WIFI_PASS, those are used directly (a
// development build embeds its own credentials for convenience). A
// distribution build omits both macros so the binary never contains a
// password; in that case this reads previously-saved credentials from NVS,
// or -- on first boot -- blocks on a one-time interactive Serial prompt and
// saves the result to NVS before returning.
#pragma once

#include <cstddef>

namespace wifi_provision {

constexpr size_t kMaxSsidLen = 32;  // IEEE 802.11 SSID max
constexpr size_t kMaxPassLen = 64;  // WPA2 passphrase max

// Fills ssid/pass with null-terminated credentials. May block (Serial
// prompt) only on a distribution build's first-ever boot.
void getCredentials(char ssidOut[kMaxSsidLen + 1], char passOut[kMaxPassLen + 1]);

}  // namespace wifi_provision
