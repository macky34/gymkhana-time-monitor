#!/usr/bin/env python3
"""Stop hook: 応答終了時、.go ファイルに未コミット変更がある場合に
go build ./... && go vet ./... を実行し、失敗していたら停止をブロックして
修正を促す (CLAUDE.md のビルド/テスト規約の機械的強制)。"""
import json
import subprocess
import sys

REPO = "/home/mac/src/gymkhana-time-monitor"

try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

# 無限ループ防止: このフックのブロックを受けて続行した応答の停止時は再検査しない
if data.get("stop_hook_active"):
    sys.exit(0)

# .go に変更が無いターン (ドキュメント編集や会話のみ等) では何もしない
try:
    porcelain = subprocess.run(
        ["git", "-C", REPO, "status", "--porcelain"],
        capture_output=True, text=True, check=False,
    ).stdout
except Exception:
    sys.exit(0)
if not any(line.endswith(".go") for line in porcelain.splitlines()):
    sys.exit(0)

for args in (["go", "build", "./..."], ["go", "vet", "./..."]):
    p = subprocess.run(args, cwd=REPO, capture_output=True, text=True, check=False)
    if p.returncode != 0:
        out = (p.stdout + p.stderr).strip()
        print(
            f"`{' '.join(args)}` が失敗しています。停止する前に修正してください:\n"
            + out[:3000],
            file=sys.stderr,
        )
        sys.exit(2)

sys.exit(0)
