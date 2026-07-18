#!/usr/bin/env python3
"""PostToolUse hook: Edit/Write された .go ファイルを gofmt -w で自動整形する。"""
import json
import shutil
import subprocess
import sys

try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

path = (data.get("tool_input") or {}).get("file_path", "")
if path.endswith(".go") and shutil.which("gofmt"):
    subprocess.run(["gofmt", "-w", path], check=False)
