#include "wifi_provision.h"

#include <Arduino.h>
#include <Preferences.h>
#include <cstring>

#include "config.h"

namespace wifi_provision {

namespace {

constexpr const char *kNamespace = "wifi";
constexpr const char *kSsidKey = "ssid";
constexpr const char *kPassKey = "pass";

// Blocks until a non-empty line (echoed back) is read from Serial, CR/LF
// trimmed.
void readLine(char *buf, size_t bufLen) {
  size_t n = 0;
  while (true) {
    if (!Serial.available()) continue;
    int c = Serial.read();
    if (c == '\r') continue;
    if (c == '\n') {
      if (n > 0) break;
      continue;
    }
    if (n < bufLen - 1) {
      buf[n++] = (char)c;
      Serial.write((char)c);
    }
  }
  buf[n] = '\0';
  Serial.println();
}

void promptAndSave(char *ssidOut, size_t ssidLen, char *passOut, size_t passLen) {
  Serial.println();
  Serial.println("[wifi-provision] no saved WiFi credentials, please enter them now.");
  Serial.print("[wifi-provision] SSID: ");
  readLine(ssidOut, ssidLen);
  Serial.print("[wifi-provision] password: ");
  readLine(passOut, passLen);

  Preferences prefs;
  prefs.begin(kNamespace, false);
  prefs.putString(kSsidKey, ssidOut);
  prefs.putString(kPassKey, passOut);
  prefs.end();
  Serial.println("[wifi-provision] saved to NVS.");
}

}  // namespace

void getCredentials(char ssidOut[kMaxSsidLen + 1], char passOut[kMaxPassLen + 1]) {
#if defined(WIFI_SSID) && defined(WIFI_PASS)
  strncpy(ssidOut, WIFI_SSID, kMaxSsidLen);
  ssidOut[kMaxSsidLen] = '\0';
  strncpy(passOut, WIFI_PASS, kMaxPassLen);
  passOut[kMaxPassLen] = '\0';
#else
  Preferences prefs;
  prefs.begin(kNamespace, true);  // read-only
  bool has = prefs.isKey(kSsidKey) && prefs.isKey(kPassKey);
  if (has) {
    prefs.getString(kSsidKey, ssidOut, kMaxSsidLen + 1);
    prefs.getString(kPassKey, passOut, kMaxPassLen + 1);
  }
  prefs.end();
  if (has && ssidOut[0] != '\0') return;

  promptAndSave(ssidOut, kMaxSsidLen + 1, passOut, kMaxPassLen + 1);
#endif
}

}  // namespace wifi_provision
