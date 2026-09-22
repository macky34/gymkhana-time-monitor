#!/usr/bin/env python3
"""Generates a distribution-build config.h from config.h.example on stdin.

Strips WIFI_SSID/WIFI_PASS entirely -- a distribution build never embeds a
password, so the device always falls back to the Serial+NVS provisioning
flow (wifi_provision.cpp) on first boot. For the C6 target, also swaps the
active C5 board-specific #defines for the commented-out C6 ones.

Usage: gen_dist_config.py <seeed_xiao_esp32c5|seeed_xiao_esp32c6> < config.h.example > config.h
"""
import re
import sys


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ("seeed_xiao_esp32c5", "seeed_xiao_esp32c6"):
        sys.stderr.write("usage: gen_dist_config.py <seeed_xiao_esp32c5|seeed_xiao_esp32c6>\n")
        sys.exit(1)
    env = sys.argv[1]

    content = sys.stdin.read()

    content = re.sub(r'^#define WIFI_SSID.*\n', '', content, flags=re.MULTILINE)
    content = re.sub(r'^#define WIFI_PASS.*\n', '', content, flags=re.MULTILINE)

    if env == "seeed_xiao_esp32c6":
        c6_marker = content.index('// --- Seeed Studio XIAO ESP32C6')
        head = content[:c6_marker]
        c6_block = content[c6_marker:]

        # Only the macro names the C6 block overrides need swapping --
        # everything else before the marker (DEFAULT_LOCKOUT_MS,
        # ESPNOW_LR_MODE, DEBUG_*, ...) is shared between boards and must
        # stay active.
        c6_names = re.findall(r'^//   #define (\S+)', c6_block, flags=re.MULTILINE)
        for name in c6_names:
            head = re.sub(rf'^(#define {re.escape(name)}\b.*)$', r'// \1', head, flags=re.MULTILINE)

        # Uncomment the C6 board-specific #defines ("//   #define ..." -> "#define ...").
        c6_block = re.sub(r'^//   (#define \S.*)$', r'\1', c6_block, flags=re.MULTILINE)

        content = head + c6_block

    sys.stdout.write(content)


if __name__ == "__main__":
    main()
