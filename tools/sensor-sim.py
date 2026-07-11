#!/usr/bin/env python3
"""ESP32センサーシミュレータ (開発用)。

CONTRACTS.md §4.5 のUDPワイヤーフォーマットで timemon (UDP :9999) にパケットを送る。

使い方:
  python tools/sensor-sim.py trigger start          # start打刻を3連送
  python tools/sensor-sim.py trigger goal           # goal打刻を3連送
  python tools/sensor-sim.py run 83.456             # start→(83.456秒後)→goal を一連で送る
  python tools/sensor-sim.py hb                     # start/goal両方のハートビートを5秒毎に送り続ける
オプション:
  --host 127.0.0.1  --port 9999
"""
import argparse
import json
import random
import socket
import time

def now_us() -> int:
    return time.time_ns() // 1000

class Sensor:
    def __init__(self, sensor_id: str, host: str, port: int):
        self.sensor_id = sensor_id
        self.boot_id = random.randrange(1, 2**32)
        self.seq = 0
        self.addr = (host, port)
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)

    def trigger(self, ts_us: int | None = None):
        self.seq += 1
        pkt = json.dumps({
            "type": "trigger", "sensor_id": self.sensor_id,
            "boot_id": self.boot_id, "seq": self.seq,
            "timestamp_us": ts_us if ts_us is not None else now_us(),
        }).encode()
        for i in range(3):  # 3連送50ms間隔 (重複排除の検証も兼ねる)
            self.sock.sendto(pkt, self.addr)
            if i < 2:
                time.sleep(0.05)
        print(f"[{self.sensor_id}] trigger seq={self.seq} boot={self.boot_id}")

    def heartbeat(self):
        self.seq += 1
        pkt = json.dumps({
            "type": "hb", "sensor_id": self.sensor_id,
            "boot_id": self.boot_id, "seq": self.seq,
            "ntp_offset_ms": round(random.uniform(-1, 1), 2),
        }).encode()
        self.sock.sendto(pkt, self.addr)

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["trigger", "run", "hb"])
    ap.add_argument("arg", nargs="?", help="trigger: start|goal / run: 走行秒数(float)")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=9999)
    a = ap.parse_args()

    if a.mode == "trigger":
        sid = a.arg or "start"
        Sensor(sid, a.host, a.port).trigger()
    elif a.mode == "run":
        secs = float(a.arg or "10")
        start = Sensor("start", a.host, a.port)
        goal = Sensor("goal", a.host, a.port)
        t0 = now_us()
        start.trigger(t0)
        print(f"waiting {secs}s ...")
        time.sleep(secs)
        goal.trigger(t0 + int(secs * 1_000_000))  # 打刻差=正確にsecs (chrony同期の模擬)
        print(f"raw would be {int(secs*1000)} ms")
    else:  # hb
        start = Sensor("start", a.host, a.port)
        goal = Sensor("goal", a.host, a.port)
        print("heartbeat loop (Ctrl-C to stop)")
        while True:
            start.heartbeat()
            goal.heartbeat()
            time.sleep(5)

if __name__ == "__main__":
    main()
