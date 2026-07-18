#!/usr/bin/env python3
"""PreToolUse hook: セルフホストしたベンダー配布物 (*.min.js / *.min.css) への
Edit/Write をブロックする。これらは手編集せず、アップストリームの新版ファイル
での差し替えのみを許す (差分が消える事故の防止)。"""
import json
import sys

try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

path = (data.get("tool_input") or {}).get("file_path", "")
if path.endswith((".min.js", ".min.css")):
    print(
        "ブロック: ベンダー配布物 (*.min.js/*.min.css) の直接編集は禁止です。"
        "更新はアップストリームの配布ファイルでの差し替え (Bash の cp 等) で行ってください。",
        file=sys.stderr,
    )
    sys.exit(2)

sys.exit(0)
