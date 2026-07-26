#!/usr/bin/env bash
# dual-browser.sh: X11転送環境でChromiumをPCサイズ・スマホサイズの窓を
# light/darkテーマ同時に起動する(既定で4窓: PC×light, SP×light, PC×dark, SP×dark)。
#
# Usage:
#   tools/dual-browser.sh --base-url http://127.0.0.1:18081 \
#     --pc-path /a/<token> --sp-path /a/<token> \
#     [--pc-size 1280x800] [--sp-size 390x844] [--gap 24] [--profile-dir /path] \
#     [--themes light,dark]
#
# --themes は既定で "light,dark"(4窓: テーマごとにPC窓・SP窓の1行が縦に並ぶ)。
# --themes light のように単一テーマを指定すると1行(2窓)に減らせる。
# 内部では Playwright (Node + playwright-core) の color-scheme emulation
# (launchPersistentContext の colorScheme オプション)を使ってウィンドウを起動する。
#
# Exit codes:
#   0  正常起動(標準出力にNodeラッパープロセスのPIDを1行出す)
#   2  引数エラー(未知オプション・必須引数欠如・不正なテーマ名など)
#   3  DISPLAYが解決できない
#   4  Chromiumが見つからない
#   5  X11サーバーへの疎通に失敗
#   6  playwright-core が未インストールで自動インストールにも失敗した

set -uo pipefail

BASE_URL=""
PC_PATH="/a/<token>"
SP_PATH="/a/<token>"
PC_SIZE="1280x800"
SP_SIZE="390x844"
GAP="24"
PROFILE_DIR=""
THEMES="light,dark"

usage() {
  cat >&2 <<'EOF'
Usage: tools/dual-browser.sh --base-url URL [--pc-path PATH] [--sp-path PATH]
                              [--pc-size WxH] [--sp-size WxH] [--gap N]
                              [--profile-dir DIR] [--themes light,dark]
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --base-url)
      BASE_URL="$2"
      shift 2
      ;;
    --pc-path)
      PC_PATH="$2"
      shift 2
      ;;
    --sp-path)
      SP_PATH="$2"
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
    --profile-dir)
      PROFILE_DIR="$2"
      shift 2
      ;;
    --themes)
      THEMES="$2"
      shift 2
      ;;
    *)
      echo "dual-browser.sh: unknown option: $1" >&2
      usage
      exit 2
      ;;
  esac
done

if [ -z "$BASE_URL" ]; then
  echo "dual-browser.sh: --base-url is required" >&2
  usage
  exit 2
fi

if [ -z "$THEMES" ]; then
  echo "dual-browser.sh: --themes must not be empty" >&2
  usage
  exit 2
fi

IFS=',' read -ra THEME_ARR <<< "$THEMES"
for theme in "${THEME_ARR[@]}"; do
  case "$theme" in
    light|dark) ;;
    *)
      echo "dual-browser.sh: unknown theme: $theme" >&2
      usage
      exit 2
      ;;
  esac
done

# 1. DISPLAY / XAUTHORITY 解決
if [ -z "${DISPLAY:-}" ]; then
  eval "$(tmux show-environment -s DISPLAY 2>/dev/null)" || true
fi
if [ -z "${XAUTHORITY:-}" ]; then
  eval "$(tmux show-environment -s XAUTHORITY 2>/dev/null)" || true
fi

if [ -z "${DISPLAY:-}" ]; then
  echo "dual-browser.sh: DISPLAY is not set and could not be resolved from tmux" >&2
  exit 3
fi

export DISPLAY
if [ -n "${XAUTHORITY:-}" ]; then
  export XAUTHORITY
fi

# 2. 疎通確認
if ! xdpyinfo >/dev/null 2>&1; then
  echo "dual-browser.sh: cannot connect to X server on DISPLAY=${DISPLAY}" >&2
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
  echo "dual-browser.sh: chromium executable not found" >&2
  exit 4
fi

# 4. 画面サイズに応じた調整
pc_w="${PC_SIZE%x*}"
pc_h="${PC_SIZE#*x}"
sp_w="${SP_SIZE%x*}"
sp_h="${SP_SIZE#*x}"

screen_dim="$(xdpyinfo | awk '/dimensions:/{print $2}')"
screen_w="${screen_dim%x*}"

if [ -n "$screen_w" ]; then
  total_w=$((pc_w + GAP + sp_w))
  if [ "$total_w" -gt "$screen_w" ]; then
    new_pc_w=$((screen_w - GAP - sp_w))
    if [ "$new_pc_w" -lt 900 ]; then
      new_pc_w=900
      echo "dual-browser.sh: warning: PC window (900px) + gap (${GAP}px) + SP window (${sp_w}px) exceeds screen width (${screen_w}px); windows may be clipped" >&2
    fi
    pc_w="$new_pc_w"
  fi
fi

# 5. プロファイルディレクトリ
if [ -z "$PROFILE_DIR" ]; then
  PROFILE_DIR="$(mktemp -d)"
fi
mkdir -p "$PROFILE_DIR"

# 6. レイアウト計算 + JSON仕様ファイル組み立て(python3でエスケープを安全に行う)
row_h=$(( pc_h > sp_h ? pc_h : sp_h ))
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SPEC_FILE="$(mktemp)"

if ! python3 - "$SPEC_FILE" "$CHROME_BIN" "$BASE_URL" "$PC_PATH" "$SP_PATH" \
  "$PROFILE_DIR" "$pc_w" "$pc_h" "$sp_w" "$sp_h" "$GAP" "$row_h" "$THEMES" <<'PYEOF'
import json
import sys

(spec_file, chrome_bin, base_url, pc_path, sp_path, profile_dir,
 pc_w, pc_h, sp_w, sp_h, gap, row_h, themes) = sys.argv[1:14]
pc_w, pc_h, sp_w, sp_h, gap, row_h = (
    int(pc_w), int(pc_h), int(sp_w), int(sp_h), int(gap), int(row_h)
)
theme_list = themes.split(",")

windows = []
for row, theme in enumerate(theme_list):
    y = row * (row_h + gap)
    windows.append({
        "userDataDir": f"{profile_dir}/{theme}-pc",
        "url": base_url + pc_path,
        "x": 0, "y": y, "width": pc_w, "height": pc_h,
        "colorScheme": theme,
        "isMobile": False, "hasTouch": False, "deviceScaleFactor": 1,
    })
    windows.append({
        "userDataDir": f"{profile_dir}/{theme}-sp",
        "url": base_url + sp_path,
        "x": pc_w + gap, "y": y, "width": sp_w, "height": sp_h,
        "colorScheme": theme,
        "isMobile": True, "hasTouch": True, "deviceScaleFactor": 2,
    })

with open(spec_file, "w") as f:
    json.dump({"chromePath": chrome_bin, "windows": windows}, f)
PYEOF
then
  echo "dual-browser.sh: failed to build launch spec" >&2
  exit 2
fi

# 7. playwright-core の存在確認と自動install
if [ ! -d "$SCRIPT_DIR/dual-browser/node_modules/playwright-core" ]; then
  echo "dual-browser.sh: installing playwright-core (first run)..." >&2
  if ! npm install --prefix "$SCRIPT_DIR/dual-browser" >&2; then
    echo "dual-browser.sh: npm install failed. Run manually: npm install --prefix tools/dual-browser" >&2
    exit 6
  fi
fi

if ! command -v node >/dev/null 2>&1; then
  echo "dual-browser.sh: node executable not found (required to launch themed windows)" >&2
  exit 6
fi

# 8. Node起動(バックグラウンドで即座に返す。標準出力にはNodeラッパーのPIDのみ出す)
nohup node "$SCRIPT_DIR/dual-browser/launch.mjs" "$SPEC_FILE" >"$PROFILE_DIR/node.log" 2>&1 &
node_pid=$!
disown "$node_pid" 2>/dev/null || true
echo "$node_pid"

exit 0
