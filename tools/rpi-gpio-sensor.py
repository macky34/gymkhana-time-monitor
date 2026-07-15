#!/usr/bin/env python3
"""RPi GPIO -> timemon UDP bridge (photoelectric sensor wired directly to the Pi).

Reads falling edges (beam broken, NPN open-collector output pulled low) on a
GPIO line via libgpiod v2, applies the same debounce lockout as the ESP32
firmware (first edge wins, later edges inside the window are dropped), and
sends the trigger in the exact UDP wire format of docs/CONTRACTS.md §4.5:

  {"type":"trigger","sensor_id":"start","boot_id":...,"seq":...,"timestamp_us":...}

Triggers are sent as a 3-packet burst 50 ms apart; a heartbeat is sent every
5 seconds. timestamp_us is wall-clock (time.time() base) microseconds -- the
same time base the server pairs on -- NOT a monotonic clock. When the kernel
supports it (5.11+), the edge is timestamped in kernel interrupt context with
CLOCK_REALTIME, so Python scheduling jitter does not affect the timestamp;
otherwise we fall back to stamping with time.time() when the event is read.

Usage:
  python3 tools/rpi-gpio-sensor.py --sensor-id start --gpio 17
  python3 tools/rpi-gpio-sensor.py --sensor-id goal  --gpio 27 --host 127.0.0.1 --port 9999

Dependencies: standard library + gpiod (libgpiod v2 Python bindings,
e.g. `pip install gpiod`). The apt package python3-libgpiod on Debian
Bookworm is the old v1 API and will NOT work.

The bridge never exits on runtime errors: GPIO and network failures are
logged and retried so a transient glitch does not take the sensor down
mid-event.
"""

import argparse
import json
import random
import socket
import sys
import time

try:
    import gpiod
    from gpiod.line import Bias, Clock, Direction, Edge
except ImportError:
    sys.stderr.write(
        "error: gpiod v2 python bindings not found. Install with: pip install gpiod\n"
        "(python3-libgpiod from apt is the old v1 API and is not supported)\n"
    )
    sys.exit(1)

HEARTBEAT_INTERVAL_S = 5.0
BURST_COUNT = 3
BURST_GAP_S = 0.05
RETRY_DELAY_S = 1.0


def now_us() -> int:
    """Wall-clock microseconds (same base as chrony / the server)."""
    return time.time_ns() // 1000


class UdpSender:
    """Sends trigger/hb packets; mirrors the ESP32 firmware counters:
    boot_id is a per-process random uint32, trigger and hb keep independent
    seq counters (the server dedup key is (sensor_id, boot_id, seq) of
    triggers only)."""

    def __init__(self, sensor_id: str, host: str, port: int):
        self.sensor_id = sensor_id
        self.addr = (host, port)
        self.boot_id = random.randrange(1, 2**32)
        self.trigger_seq = 0
        self.hb_seq = 0
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)

    def _send(self, payload: dict) -> None:
        try:
            self.sock.sendto(json.dumps(payload).encode(), self.addr)
        except OSError as e:
            # Network hiccup: log and carry on. Triggers are burst x3 and the
            # server keeps orphan handling, so a lost packet is survivable.
            print(f"[udp] send failed: {e}", file=sys.stderr)

    def trigger(self, ts_us: int) -> None:
        self.trigger_seq += 1
        pkt = {
            "type": "trigger",
            "sensor_id": self.sensor_id,
            "boot_id": self.boot_id,
            "seq": self.trigger_seq,
            "timestamp_us": ts_us,
        }
        for i in range(BURST_COUNT):  # 3-packet burst, 50 ms apart
            self._send(pkt)
            if i < BURST_COUNT - 1:
                time.sleep(BURST_GAP_S)
        print(f"[trigger] seq={self.trigger_seq} ts={ts_us}")

    def heartbeat(self) -> None:
        self.hb_seq += 1
        # ntp_offset_ms is informational; the bridge runs on the server's own
        # clock so 0.0 is the honest value.
        self._send({
            "type": "hb",
            "sensor_id": self.sensor_id,
            "boot_id": self.boot_id,
            "seq": self.hb_seq,
            "ntp_offset_ms": 0.0,
        })


def open_request(chip: str, gpio: int):
    """Request the GPIO line for falling-edge events with the internal
    pull-up enabled. Returns (request, realtime) where realtime says whether
    the kernel stamps events with CLOCK_REALTIME (preferred) or we must stamp
    them ourselves when reading."""
    base = dict(
        direction=Direction.INPUT,
        edge_detection=Edge.FALLING,
        bias=Bias.PULL_UP,
    )
    try:
        req = gpiod.request_lines(
            chip,
            consumer="timemon-sensor",
            config={gpio: gpiod.LineSettings(event_clock=Clock.REALTIME, **base)},
        )
        return req, True
    except OSError:
        # Kernel too old for REALTIME event clock: fall back to the default
        # monotonic clock and stamp with time.time() at read time instead.
        req = gpiod.request_lines(
            chip,
            consumer="timemon-sensor",
            config={gpio: gpiod.LineSettings(**base)},
        )
        return req, False


def run(args: argparse.Namespace) -> None:
    sender = UdpSender(args.sensor_id, args.host, args.port)
    lockout_us = args.lockout_ms * 1000
    last_accepted_us = 0
    last_hb = 0.0

    print(
        f"[start] sensor_id={args.sensor_id} gpio={args.gpio} chip={args.chip} "
        f"target={args.host}:{args.port} lockout_ms={args.lockout_ms} "
        f"boot_id={sender.boot_id}"
    )

    while True:  # outer retry loop: never die on GPIO errors
        try:
            request, realtime = open_request(args.chip, args.gpio)
            if not realtime:
                print("[gpio] kernel lacks REALTIME event clock; "
                      "stamping at read time (slightly less accurate)")
            with request:
                while True:
                    now = time.monotonic()
                    if now - last_hb >= HEARTBEAT_INTERVAL_S:
                        last_hb = now
                        sender.heartbeat()

                    # Short wait so the heartbeat keeps its 5 s cadence.
                    if not request.wait_edge_events(0.5):
                        continue
                    for event in request.read_edge_events():
                        if realtime:
                            ts_us = event.timestamp_ns // 1000
                        else:
                            ts_us = now_us()
                        # Debounce lockout, same rule as the firmware:
                        # first edge wins, edges inside the window are dropped.
                        if last_accepted_us and ts_us - last_accepted_us < lockout_us:
                            continue
                        last_accepted_us = ts_us
                        sender.trigger(ts_us)
        except KeyboardInterrupt:
            print("\n[stop] interrupted")
            return
        except OSError as e:
            print(f"[gpio] error: {e} -- retrying in {RETRY_DELAY_S}s", file=sys.stderr)
            time.sleep(RETRY_DELAY_S)


def main() -> None:
    ap = argparse.ArgumentParser(
        description="Bridge a directly-wired photoelectric sensor on a RPi GPIO "
                    "to the timemon UDP sensor protocol.")
    ap.add_argument("--sensor-id", required=True, choices=["start", "goal"],
                    help="which sensor this GPIO represents")
    ap.add_argument("--gpio", type=int, required=True,
                    help="BCM GPIO line number the sensor output is wired to")
    ap.add_argument("--chip", default="/dev/gpiochip0",
                    help="gpiochip device (default: /dev/gpiochip0; "
                         "older RPi 5 kernels expose the header as gpiochip4)")
    ap.add_argument("--host", default="127.0.0.1", help="timemon UDP host")
    ap.add_argument("--port", type=int, default=9999, help="timemon UDP port")
    ap.add_argument("--lockout-ms", type=int, default=800,
                    help="debounce lockout in ms (default 800, same as firmware)")
    run(ap.parse_args())


if __name__ == "__main__":
    main()
