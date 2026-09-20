#!/usr/bin/env bash
# rpi-auto-update.sh: GitHub Releaseの最新版をチェックし、現在稼働中の
# timemonバイナリと異なれば自動的にダウンロード・差し替え・再起動する。
# RPi上でsystemdタイマー(例: 1時間毎)から定期実行することを想定。
# イベント運用中(GET /api/settings の event_status=="active")は計測を
# 中断させないよう適用を見送り、次回の定期実行で再チェックする(設定手順は
# Server-Setup wikiページ「自動アップデート」を参照)。
#
# Usage:
#   tools/rpi-auto-update.sh [--repo owner/repo] [--bin-dir /opt/timemon]
#                             [--bin-name timemon-linux-arm64] [--service timemon]
#                             [--health-url http://127.0.0.1:8080/api/settings]
#
# 動作:
#   1. 現在のバイナリの --version と GitHub Releases API の最新タグを比較
#   2. 差異が無ければ何もせず終了(exit 0)
#   3. 差異があっても event_status=="active"(イベント運用中)なら見送って
#      終了(exit 0)。次回の定期実行で再チェックされる
#   4. それ以外なら新バイナリをダウンロードして --version で検証し、
#      既存バイナリをバックアップした上でsystemdサービスを再起動
#   5. 再起動後にヘルスチェックが失敗したら旧バイナリへ即ロールバック
#
# Exit codes:
#   0  正常終了(更新なし・イベント運用中で見送り・または更新成功)
#   1  引数エラー
#   2  最新リリース情報の取得に失敗
#   3  新バイナリのダウンロード・検証に失敗(既存バイナリには一切触れていない)
#   4  更新後のヘルスチェックに失敗し、ロールバックした(ロールバック自体は成功)
#   5  ロールバックにも失敗(手動対応が必要な異常系)
#
# 依存: curl, python3 (JSON解析。RPi OSに標準で入っている)
set -uo pipefail

REPO="macky34/gymkhana-time-monitor"
BIN_DIR="/opt/timemon"
BIN_NAME="timemon-linux-arm64"
SERVICE="timemon"
HEALTH_URL="http://127.0.0.1:8080/api/settings"
HEALTH_RETRIES=10
HEALTH_INTERVAL=1
KEEP_BACKUPS=5

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

usage() {
  cat >&2 <<'EOF'
Usage: tools/rpi-auto-update.sh [--repo owner/repo] [--bin-dir DIR]
                                 [--bin-name NAME] [--service NAME]
                                 [--health-url URL]
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) REPO="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --bin-name) BIN_NAME="$2"; shift 2 ;;
    --service) SERVICE="$2"; shift 2 ;;
    --health-url) HEALTH_URL="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

BIN_PATH="$BIN_DIR/$BIN_NAME"

if [ ! -x "$BIN_PATH" ]; then
  echo "binary not found or not executable: $BIN_PATH" >&2
  exit 1
fi

CURRENT_VERSION="$("$BIN_PATH" --version 2>/dev/null || true)"
if [ -z "$CURRENT_VERSION" ]; then
  echo "failed to read current version from $BIN_PATH --version" >&2
  exit 1
fi

LATEST_JSON="$(curl -sf "https://api.github.com/repos/$REPO/releases/latest" || true)"
if [ -z "$LATEST_JSON" ]; then
  echo "failed to fetch latest release info from GitHub API" >&2
  exit 2
fi
LATEST_VERSION="$(echo "$LATEST_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tag_name",""))' 2>/dev/null || true)"
if [ -z "$LATEST_VERSION" ]; then
  echo "failed to parse tag_name from release info" >&2
  exit 2
fi

if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ]; then
  log "up to date (version=$CURRENT_VERSION)"
  exit 0
fi

log "update available: $CURRENT_VERSION -> $LATEST_VERSION"

# Don't interrupt an in-progress event: applying (which stops/restarts the
# service) mid-timing would drop connections and could lose a trigger.
# GET /api/settings returns {"event":null} (no event_status field at all)
# when no event is active, and {..., "event_status":"active", ...} while
# one is (there is no "closed"-but-current-event case: GetActiveEvent only
# ever returns the one row with status='active', see internal/snapshot).
SETTINGS_JSON="$(curl -sf "$HEALTH_URL" 2>/dev/null || true)"
EVENT_STATUS="$(echo "$SETTINGS_JSON" | python3 -c 'import json,sys
try:
    print(json.load(sys.stdin).get("event_status") or "")
except Exception:
    print("")' 2>/dev/null || true)"
if [ "$EVENT_STATUS" = "active" ]; then
  log "event is active, deferring update to next scheduled run"
  exit 0
fi

DOWNLOAD_URL="https://github.com/$REPO/releases/download/$LATEST_VERSION/$BIN_NAME"
TMP_BIN="$(mktemp)"
trap 'rm -f "$TMP_BIN"' EXIT

if ! curl -sfL -o "$TMP_BIN" "$DOWNLOAD_URL"; then
  echo "failed to download $DOWNLOAD_URL" >&2
  exit 3
fi
chmod 755 "$TMP_BIN"

DOWNLOADED_VERSION="$("$TMP_BIN" --version 2>/dev/null || true)"
if [ "$DOWNLOADED_VERSION" != "$LATEST_VERSION" ]; then
  echo "downloaded binary reports version '$DOWNLOADED_VERSION', expected '$LATEST_VERSION' (corrupt download?)" >&2
  exit 3
fi

BACKUP_PATH="$BIN_DIR/$BIN_NAME.bak-$(date +%s)"
log "backing up current binary to $BACKUP_PATH"
cp "$BIN_PATH" "$BACKUP_PATH"

log "stopping $SERVICE"
sudo systemctl stop "$SERVICE"

log "installing $LATEST_VERSION"
cp "$TMP_BIN" "$BIN_PATH"
chmod 755 "$BIN_PATH"

log "starting $SERVICE"
sudo systemctl start "$SERVICE"

ok=0
for i in $(seq 1 "$HEALTH_RETRIES"); do
  if curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep "$HEALTH_INTERVAL"
done

if [ "$ok" != "1" ]; then
  log "health check failed after update, rolling back to $CURRENT_VERSION"
  sudo systemctl stop "$SERVICE"
  cp "$BACKUP_PATH" "$BIN_PATH"
  sudo systemctl start "$SERVICE"
  for i in $(seq 1 "$HEALTH_RETRIES"); do
    if curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
      log "rollback succeeded, service healthy on $CURRENT_VERSION again"
      exit 4
    fi
    sleep "$HEALTH_INTERVAL"
  done
  echo "rollback failed: service not healthy even after restoring $CURRENT_VERSION - manual intervention needed" >&2
  exit 5
fi

log "update succeeded: now running $LATEST_VERSION"

# Keep only the most recent KEEP_BACKUPS backups so /opt/timemon doesn't grow
# without bound over many releases.
ls -1t "$BIN_DIR/$BIN_NAME".bak-* 2>/dev/null | tail -n "+$((KEEP_BACKUPS + 1))" | while read -r old; do
  log "pruning old backup $old"
  rm -f "$old"
done

exit 0
