#include "identity.h"

#include <Arduino.h>
#include <esp_wifi.h>

void Identity::begin(wire::Role role) {
  role_ = role;

  uint8_t mac[6] = {0};
  esp_wifi_get_mac(WIFI_IF_STA, mac);
  uint32_t macLow32 = ((uint32_t)mac[2] << 24) | ((uint32_t)mac[3] << 16) |
                       ((uint32_t)mac[4] << 8) | (uint32_t)mac[5];

  bootId_ = esp_random() ^ macLow32 ^ (uint32_t)esp_timer_get_time();
}
