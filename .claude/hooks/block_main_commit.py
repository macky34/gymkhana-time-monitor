#!/usr/bin/env python3
"""PreToolUse hook: gymkhana-time-monitor リポジトリ(またはその worktree)が
main ブランチのとき git commit をブロックする(CLAUDE.md のブランチ運用
ルールの強制)。

git worktree はメインチェックアウトとは別のブランチを独立に持つため、
判定は常に「このコマンドが実際に実行される場所(cwd)」のブランチを見る。
メインチェックアウトへのパスをハードコードして参照すると、worktree 内で
作業中でも常にメイン側の(main のままの)ブランチを見てしまい誤検知する
(実際に一度これで正しい作業ブランチでのコミットが誤ブロックされた)。

環境変数 CLAUDE_PROJECT_DIR は worktree セッションに切り替わっても常に
メインチェックアウトの固定パスを指すため(実測で確認済み)、この判定には
使えない。os.getcwd() はフックプロセス自身についても実際に呼び出しが
行われる worktree のディレクトリを正しく返すので、こちらを唯一の情報源
とする(以前は CLAUDE_PROJECT_DIR を優先していたため、この修正自体が
worktree での誤ブロックを再現していた)。"""
import json
import os
import subprocess
import sys


def repo_root(path):
    try:
        r = subprocess.run(
            ["git", "-C", path, "rev-parse", "--show-toplevel"],
            capture_output=True, text=True, check=False,
        )
    except Exception:
        return None
    return r.stdout.strip() if r.returncode == 0 else None


try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

cmd = (data.get("tool_input") or {}).get("command", "")
if "git commit" not in cmd:
    sys.exit(0)

# Wiki (gymkhana-time-monitor.wiki) は独立リポジトリで、GitHub Wiki はデフォルト
# ブランチしか公開されないため直コミットが正規運用。Wikiだけを対象にした
# コマンド(本体リポジトリへの言及なし)はブロック対象外とする。
cmd_sans_wiki = cmd.replace("gymkhana-time-monitor.wiki", "")
if "gymkhana-time-monitor.wiki" in cmd and "gymkhana-time-monitor" not in cmd_sans_wiki:
    sys.exit(0)

# このリポジトリ(のいずれかの worktree)が対象になりうるコマンドかを判定:
# cwd がリポジトリ/worktree内、またはコマンド文字列がリポジトリに言及。
proj = os.getcwd()
root = repo_root(proj)
involved = (root is not None and "gymkhana-time-monitor" in root) or (
    "gymkhana-time-monitor" in cmd_sans_wiki
)
if not involved:
    sys.exit(0)

# メインチェックアウトへの固定パスではなく、実行場所(cwd)自身のブランチを
# 見る — worktree ではこれがメインチェックアウトのブランチと異なる。
branch = subprocess.run(
    ["git", "-C", proj, "branch", "--show-current"],
    capture_output=True, text=True, check=False,
).stdout.strip()

if branch == "main":
    print(
        "ブロック: main ブランチへの直接コミットは禁止です (CLAUDE.md)。"
        "先に作業ブランチを作成してください (例: git switch -c fix/xxx)。",
        file=sys.stderr,
    )
    sys.exit(2)
