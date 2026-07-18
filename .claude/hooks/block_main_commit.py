#!/usr/bin/env python3
"""PreToolUse hook: gymkhana-time-monitor リポジトリが main ブランチのとき
git commit をブロックする(CLAUDE.md のブランチ運用ルールの強制)。"""
import json
import os
import subprocess
import sys

REPO = "/home/mac/src/gymkhana-time-monitor"

try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

cmd = (data.get("tool_input") or {}).get("command", "")
if "git commit" not in cmd:
    sys.exit(0)

# このリポジトリが対象になりうるコマンドかを判定:
# プロジェクトディレクトリがリポジトリ内、またはコマンド文字列がリポジトリに言及。
# Wiki (gymkhana-time-monitor.wiki) は独立リポジトリで、GitHub Wiki はデフォルト
# ブランチしか公開されないため直コミットが正規運用 — 言及扱いから除外する。
proj = os.environ.get("CLAUDE_PROJECT_DIR", os.getcwd())
cmd_sans_wiki = cmd.replace("gymkhana-time-monitor.wiki", "")
involved = proj.startswith(REPO) or "gymkhana-time-monitor" in cmd_sans_wiki
if not involved:
    sys.exit(0)

try:
    branch = subprocess.run(
        ["git", "-C", REPO, "branch", "--show-current"],
        capture_output=True, text=True, check=False,
    ).stdout.strip()
except Exception:
    sys.exit(0)

if branch == "main":
    print(
        "ブロック: main ブランチへの直接コミットは禁止です (CLAUDE.md)。"
        "先に作業ブランチを作成してください (例: git switch -c fix/xxx)。",
        file=sys.stderr,
    )
    sys.exit(2)
