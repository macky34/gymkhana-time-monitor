#include "espnow_radio.h"

#include <Arduino.h>
#include <WiFi.h>
#include <esp_now.h>
#include <esp_wifi.h>
#include <freertos/FreeRTOS.h>
#include <freertos/queue.h>
#include <atomic>
#include <cstring>
#include <sys/time.h>

#include "config.h"

#ifdef ESPNOW_LMK
static_assert(sizeof(ESPNOW_LMK) - 1 == 16, "ESPNOW_LMK must be exactly 16 bytes");
#endif

const uint8_t kEspNowBroadcastMac[6] = {0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF};

namespace {
QueueHandle_t g_rxQueue = nullptr;
std::atomic<uint32_t> g_sendFailStreak{0};

void onSent(const esp_now_send_info_t *txInfo, esp_now_send_status_t status) {
  (void)txInfo;
  if (status == ESP_NOW_SEND_SUCCESS) {
    g_sendFailStreak.store(0, std::memory_order_relaxed);
  } else {
    g_sendFailStreak.fetch_add(1, std::memory_order_relaxed);
  }
}

void onRecv(const esp_now_recv_info_t *info, const uint8_t *data, int len) {
  if (!g_rxQueue || len <= 0 || (size_t)len > kEspNowMaxPayload) return;
  EspNowPacket pkt;
  memcpy(pkt.mac, info->src_addr, 6);
  memcpy(pkt.data, data, len);
  pkt.len = (size_t)len;
  pkt.rssi = info->rx_ctrl ? info->rx_ctrl->rssi : 0;
  struct timeval tv;
  gettimeofday(&tv, nullptr);
  pkt.rxTimestampUs = (int64_t)tv.tv_sec * 1000000LL + tv.tv_usec;
  // Runs on the WiFi task, not an ISR, so a plain (not FromISR) send.
  xQueueSend(g_rxQueue, &pkt, 0);
}
}  // namespace

bool EspNowRadio::begin() {
  if (esp_now_init() != ESP_OK) return false;
#ifdef ESPNOW_LMK
  esp_now_set_pmk(reinterpret_cast<const uint8_t *>(ESPNOW_LMK));
#endif
  if (!g_rxQueue) {
    g_rxQueue = xQueueCreate(8, sizeof(EspNowPacket));
  }
  esp_now_register_recv_cb(onRecv);
  esp_now_register_send_cb(onSent);
  g_sendFailStreak.store(0, std::memory_order_relaxed);
  return addPeer(kEspNowBroadcastMac);  // broadcast: always unencrypted
}

bool EspNowRadio::addPeer(const uint8_t mac[6], bool encrypt) {
  if (esp_now_is_peer_exist(mac)) return true;
  esp_now_peer_info_t peer{};
  memcpy(peer.peer_addr, mac, 6);
  peer.channel = 0;  // use whatever channel STA is currently on
  peer.ifidx = WIFI_IF_STA;
#ifdef ESPNOW_LMK
  peer.encrypt = encrypt;
  if (encrypt) {
    memcpy(peer.lmk, ESPNOW_LMK, 16);
  }
#else
  peer.encrypt = false;
#endif
  return esp_now_add_peer(&peer) == ESP_OK;
}

bool EspNowRadio::send(const uint8_t mac[6], const uint8_t *data, size_t len) {
  return esp_now_send(mac, data, len) == ESP_OK;
}

bool EspNowRadio::sendBroadcast(const uint8_t *data, size_t len) {
  return send(kEspNowBroadcastMac, data, len);
}

bool EspNowRadio::poll(EspNowPacket *out) {
  if (!g_rxQueue) return false;
  return xQueueReceive(g_rxQueue, out, 0) == pdTRUE;
}

uint32_t EspNowRadio::sendFailStreak() const {
  return g_sendFailStreak.load(std::memory_order_relaxed);
}
void EspNowRadio::resetSendFailStreak() {
  g_sendFailStreak.store(0, std::memory_order_relaxed);
}

bool EspNowRadio::enableLongRange() {
  uint8_t bitmap = WIFI_PROTOCOL_11B | WIFI_PROTOCOL_11G | WIFI_PROTOCOL_11N | WIFI_PROTOCOL_LR;
  return esp_wifi_set_protocol(WIFI_IF_STA, bitmap) == ESP_OK;
}
