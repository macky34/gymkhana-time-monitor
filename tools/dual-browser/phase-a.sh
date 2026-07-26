#!/usr/bin/env bash
# phase-a.sh: /live-check Phase A 用のheadfulブラウザ起動スクリプト。
# X11転送が使える環境でのみ成功し、PC×light・SP×light・PC×dark・SP×darkの
# 計4つの headful Chromium ウィンドウを同時に起動して、それぞれ独立した
# Chrome DevTools Protocol (CDP) のリモートデバッグポートを開いたまま待機させる。
# 呼び出し側 (Claude) はこの後 phase-a-cmd.mjs で操作したい窓のポートに接続し、
# 1操作ずつ対話的に実行する(いずれかの窓での操作結果が他の窓に即座に反映される
# 様子を並べて確認できるようにするため、最初から4窓同時起動する設計。
# Phase B の tools/dual-browser.sh と同じ構成)。
#
# tools/dual-browser.sh とは別物(Phase B用の4窓常時起動スクリプトである
# dual-browser.sh / launch.mjs はここでは変更しない)。ただし playwright-core の
# node_modules は共通で再利用する。
#
# Usage:
#   tools/dual-browser/phase-a.sh --base-url URL [--path /] \
#     [--pc-size 1280x800] [--sp-size 390x844] [--gap 24] \
#     [--pc-light-port 9339] [--sp-light-port 9340] \
#     [--pc-dark-port 9341] [--sp-dark-port 9342]
#
# Exit codes:
#   0  headful起動成功(標準出力に「<ラベル> <Nodeラッパーpid> <CDPポート>」を
#      pc-light / sp-light / pc-dark / sp-dark の順で4行出す)
#   2  引数エラー
#   3  DISPLAYが解決できない → 呼び出し側は既存のPlaywright MCP(headless)にフォールバックすべき
#   4  Chromiumが見つからない
#   5  X11サーバーへの疎通に失敗 → 同上、フォールバックすべき
#   6  playwright-core が未インストールで自動インストールにも失敗した

set -uo pipefail

BASE_URL=""
PATH_ARG="/"
PC_SIZE="1280x800"
SP_SIZE="390x844"
GAP="24"
PC_LIGHT_PORT="9339"
SP_LIGHT_PORT="9340"
PC_DARK_PORT="9341"
SP_DARK_PORT="9342"

usage() {
  cat >&2 <<'EOF'
Usage: tools/dual-browser/phase-a.sh --base-url URL [--path /]
                                      [--pc-size 1280x800] [--sp-size 390x844]
                                      [--gap 24]
                                      [--pc-light-port 9339] [--sp-light-port 9340]
                                      [--pc-dark-port 9341] [--sp-dark-port 9342]
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --base-url)
      BASE_URL="$2"
      shift 2
      ;;
    --path)
      PATH_ARG="$2"
      shift 2
      ;;
    --pc-size)
      PC_SIZE="$2"
      shift 2
      ;;
    --sp-size)
      SP_SIZE="$2"
      shift 2
      ;;
    --gap)
      GAP="$2"
      shift 2
      ;;
    --pc-light-port)
      PC_LIGHT_PORT="$2"
      shift 2
      ;;
    --sp-light-port)
      SP_LIGHT_PORT="$2"
      shift 2
      ;;
    --pc-dark-port)
      PC_DARK_PORT="$2"
      shift 2
      ;;
    --sp-dark-port)
      SP_DARK_PORT="$2"
      shift 2
      ;;
    *)
      echo "phase-a.sh: unknown option: $1" >&2
      usage
      exit 2
      ;;
  esac
done

if [ -z "$BASE_URL" ]; then
  echo "phase-a.sh: --base-url is required" >&2
  usage
  exit 2
fi

# 1. DISPLAY / XAUTHORITY 解決
if [ -z "${DISPLAY:-}" ]; then
  eval "$(tmux show-environment -s DISPLAY 2>/dev/null)" || true
fi
if [ -z "${XAUTHORITY:-}" ]; then
  eval "$(tmux show-environment -s XAUTHORITY 2>/dev/null)" || true
fi

if [ -z "${DISPLAY:-}" ]; then
  echo "phase-a.sh: DISPLAY is not set and could not be resolved from tmux" >&2
  exit 3
fi

export DISPLAY
if [ -n "${XAUTHORITY:-}" ]; then
  export XAUTHORITY
fi

# 2. 疎通確認
if ! xdpyinfo >/dev/null 2>&1; then
  echo "phase-a.sh: cannot connect to X server on DISPLAY=${DISPLAY}" >&2
  exit 5
fi

# 3. Chromium探索
CHROME_BIN=""
if [ -n "${CHROME:-}" ] && [ -x "${CHROME:-}" ]; then
  CHROME_BIN="$CHROME"
fi

if [ -z "$CHROME_BIN" ]; then
  # shellcheck disable=SC2012
  candidate="$(ls -dt /home/mac/.cache/ms-playwright/chromium-*/chrome-linux64/chrome 2>/dev/null | head -n 1)"
  if [ -n "$candidate" ] && [ -x "$candidate" ]; then
    CHROME_BIN="$candidate"
  fi
fi

if [ -z "$CHROME_BIN" ]; then
  for name in chromium chromium-browser google-chrome; do
    found="$(command -v "$name" 2>/dev/null || true)"
    if [ -n "$found" ]; then
      CHROME_BIN="$found"
      break
    fi
  done
fi

if [ -z "$CHROME_BIN" ]; then
  echo "phase-a.sh: chromium executable not found" >&2
  exit 4
fi

# 4. サイズ分解
pc_w="${PC_SIZE%x*}"
pc_h="${PC_SIZE#*x}"
sp_w="${SP_SIZE%x*}"
sp_h="${SP_SIZE#*x}"

# 画面幅を超える場合はPC幅を900px下限で縮める(tools/dual-browser.shの
# 画面サイズ調整ロジックを踏襲)。
screen_dim="$(xdpyinfo | awk '/dimensions:/{print $2}')"
screen_w="${screen_dim%x*}"
if [ -n "$screen_w" ]; then
  total_w=$((pc_w + GAP + sp_w))
  if [ "$total_w" -gt "$screen_w" ]; then
    new_pc_w=$((screen_w - GAP - sp_w))
    if [ "$new_pc_w" -lt 900 ]; then
      new_pc_w=900
      echo "phase-a.sh: warning: PC window (900px) + gap (${GAP}px) + SP window (${sp_w}px) exceeds screen width (${screen_w}px); windows may be clipped" >&2
    fi
    pc_w="$new_pc_w"
  fi
fi

# light行・dark行の2行に並べる(Phase Bの tools/dual-browser.sh と同じ考え方)。
row_h=$(( pc_h > sp_h ? pc_h : sp_h ))
sp_x=$((pc_w + GAP))
light_y=0
dark_y=$((row_h + GAP))

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# 5. playwright-core の存在確認と自動install
if [ ! -d "$SCRIPT_DIR/node_modules/playwright-core" ]; then
  echo "phase-a.sh: installing playwright-core (first run)..." >&2
  if ! npm install --prefix "$SCRIPT_DIR" >&2; then
    echo "phase-a.sh: npm install failed. Run manually: npm install --prefix tools/dual-browser" >&2
    exit 6
  fi
fi

if ! command -v node >/dev/null 2>&1; then
  echo "phase-a.sh: node executable not found" >&2
  exit 6
fi

# 6. プロファイルディレクトリ(4窓それぞれ独立)
PROFILE_DIR="$(mktemp -d)"
mkdir -p "$PROFILE_DIR/light-pc" "$PROFILE_DIR/light-sp" "$PROFILE_DIR/dark-pc" "$PROFILE_DIR/dark-sp"

# 6.5 phase-a-cmd.mjs の colorScheme 状態ファイルを事前に投入する。
#
# 実機検証で判明した挙動: Chrome DevTools Protocol の Emulation.setEmulatedMedia
# (launchPersistentContext の colorScheme オプションもこれを使って実装されている)
# は、新しいCDPクライアントがそのページ/ターゲットにアタッチした時点でリセット
# される。phase-a-launch.mjs 自身は起動時に一度だけ colorScheme を設定するが、
# phase-a-cmd.mjs は1操作ごとに新しい connectOverCDP 接続を張る設計のため、
# 何もしないと最初の phase-a-cmd.mjs 呼び出しの時点で colorScheme が失われ、
# 全窓が light 相当で描画されてしまう(実機検証で確認済み)。
# phase-a-cmd.mjs は「接続のたびに、ポートごとの状態ファイルに記録された
# colorScheme を先に再適用してからアクションを実行する」仕組みを既に持っている
# (emulateMedia アクション実行時に自動更新される)ため、ここで4窓分の状態ファイルを
# あらかじめ書いておくことで、`goto`等どのアクションを最初に呼んでも
# 意図したテーマで描画され続けるようにする。
TMPDIR_RESOLVED="${TMPDIR:-/tmp}"
printf '{"colorScheme":"light"}' > "$TMPDIR_RESOLVED/phase-a-cmd-state-${PC_LIGHT_PORT}.json"
printf '{"colorScheme":"light"}' > "$TMPDIR_RESOLVED/phase-a-cmd-state-${SP_LIGHT_PORT}.json"
printf '{"colorScheme":"dark"}' > "$TMPDIR_RESOLVED/phase-a-cmd-state-${PC_DARK_PORT}.json"
printf '{"colorScheme":"dark"}' > "$TMPDIR_RESOLVED/phase-a-cmd-state-${SP_DARK_PORT}.json"

# 7. Node起動(バックグラウンドで即座に返す。標準出力には
#    「<ラベル> PID PORT」を pc-light/sp-light/pc-dark/sp-dark の順で4行出す)

nohup node "$SCRIPT_DIR/phase-a-launch.mjs" \
  --chrome "$CHROME_BIN" \
  --url "${BASE_URL}${PATH_ARG}" \
  --width "$pc_w" \
  --height "$pc_h" \
  --port "$PC_LIGHT_PORT" \
  --profile-dir "$PROFILE_DIR/light-pc" \
  --x 0 \
  --y "$light_y" \
  --color-scheme light \
  >"$PROFILE_DIR/pc-light-node.log" 2>&1 &
pc_light_pid=$!
disown "$pc_light_pid" 2>/dev/null || true

nohup node "$SCRIPT_DIR/phase-a-launch.mjs" \
  --chrome "$CHROME_BIN" \
  --url "${BASE_URL}${PATH_ARG}" \
  --width "$sp_w" \
  --height "$sp_h" \
  --port "$SP_LIGHT_PORT" \
  --profile-dir "$PROFILE_DIR/light-sp" \
  --x "$sp_x" \
  --y "$light_y" \
  --color-scheme light \
  --mobile \
  >"$PROFILE_DIR/sp-light-node.log" 2>&1 &
sp_light_pid=$!
disown "$sp_light_pid" 2>/dev/null || true

nohup node "$SCRIPT_DIR/phase-a-launch.mjs" \
  --chrome "$CHROME_BIN" \
  --url "${BASE_URL}${PATH_ARG}" \
  --width "$pc_w" \
  --height "$pc_h" \
  --port "$PC_DARK_PORT" \
  --profile-dir "$PROFILE_DIR/dark-pc" \
  --x 0 \
  --y "$dark_y" \
  --color-scheme dark \
  >"$PROFILE_DIR/pc-dark-node.log" 2>&1 &
pc_dark_pid=$!
disown "$pc_dark_pid" 2>/dev/null || true

nohup node "$SCRIPT_DIR/phase-a-launch.mjs" \
  --chrome "$CHROME_BIN" \
  --url "${BASE_URL}${PATH_ARG}" \
  --width "$sp_w" \
  --height "$sp_h" \
  --port "$SP_DARK_PORT" \
  --profile-dir "$PROFILE_DIR/dark-sp" \
  --x "$sp_x" \
  --y "$dark_y" \
  --color-scheme dark \
  --mobile \
  >"$PROFILE_DIR/sp-dark-node.log" 2>&1 &
sp_dark_pid=$!
disown "$sp_dark_pid" 2>/dev/null || true

echo "pc-light $pc_light_pid $PC_LIGHT_PORT"
echo "sp-light $sp_light_pid $SP_LIGHT_PORT"
echo "pc-dark $pc_dark_pid $PC_DARK_PORT"
echo "sp-dark $sp_dark_pid $SP_DARK_PORT"

exit 0
