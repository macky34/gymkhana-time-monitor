#include "espnow_radio.h"

#include <Arduino.h>
#include <WiFi.h>
#include <esp_now.h>
#include <freertos/FreeRTOS.h>
#include <freertos/queue.h>
#include <cstring>

const uint8_t kEspNowBroadcastMac[6] = {0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF};

namespace {
QueueHandle_t g_rxQueue = nullptr;

void onRecv(const esp_now_recv_info_t *info, const uint8_t *data, int len) {
  if (!g_rxQueue || len <= 0 || (size_t)len > kEspNowMaxPayload) return;
  EspNowPacket pkt;
  memcpy(pkt.mac, info->src_addr, 6);
  memcpy(pkt.data, data, len);
  pkt.len = (size_t)len;
  pkt.rssi = info->rx_ctrl ? info->rx_ctrl->rssi : 0;
  // Runs on the WiFi task, not an ISR, so a plain (not FromISR) send.
  xQueueSend(g_rxQueue, &pkt, 0);
}
}  // namespace

bool EspNowRadio::begin() {
  if (esp_now_init() != ESP_OK) return false;
  if (!g_rxQueue) {
    g_rxQueue = xQueueCreate(8, sizeof(EspNowPacket));
  }
  esp_now_register_recv_cb(onRecv);
  return addPeer(kEspNowBroadcastMac);
}

bool EspNowRadio::addPeer(const uint8_t mac[6]) {
  if (esp_now_is_peer_exist(mac)) return true;
  esp_now_peer_info_t peer{};
  memcpy(peer.peer_addr, mac, 6);
  peer.channel = 0;  // use whatever channel STA is currently on
  peer.ifidx = WIFI_IF_STA;
  peer.encrypt = false;
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
