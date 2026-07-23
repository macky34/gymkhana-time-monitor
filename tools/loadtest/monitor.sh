#!/bin/bash
# timemonサーバー実機のCPU/メモリ/接続数をSSH経由でリアルタイム監視する
# (性能試験用、開発用ツール)。
#
# 使い方:
#   tools/loadtest/monitor.sh <ssh-target> <出力ファイル> <実行秒数> [ssh鍵パス] [監視ポート]
#
# 例:
#   tools/loadtest/monitor.sh mac@192.168.100.81 /tmp/monitor.log 600 ~/.ssh/id_ed25519_timemon 8080
#
# 出力フォーマット (1行1サンプル、約200-300ms間隔):
#   HH:MM:SS.mmm cpu_u=<utime tick> cpu_s=<stime tick> rss=<KB> conns=<確立接続数> load=<loadavg>
# cpu_u/cpu_sは/proc/<pid>/statの累積tick値なので、瞬間使用率が要る場合は
# 2サンプル分の差分をCLK_TCK(通常100)で割ること。
#
# 対象プロセスのPIDは`ssh <target> pgrep -f timemon-linux`で毎回動的に探す
# (再起動のたびにPIDが変わるため固定値は使わない)。プロセスが見つからない
# 場合は "SSH_UNREACHABLE_OR_PROCESS_NOT_FOUND" を1行出力してリトライを続ける。

set -u
TARGET="$1"
OUT="$2"
DURATION="$3"
KEY="${4:-}"
PORT="${5:-8080}"

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=3)
if [ -n "$KEY" ]; then
  SSH_OPTS+=(-i "$KEY")
fi

END_EPOCH=$(( $(date +%s) + DURATION ))

while [ "$(date +%s)" -lt "$END_EPOCH" ]; do
  timeout 5 ssh "${SSH_OPTS[@]}" "$TARGET" "
    pid=\$(pgrep -f timemon-linux | head -1)
    if [ -z \"\$pid\" ]; then
      echo 'SSH_UNREACHABLE_OR_PROCESS_NOT_FOUND'
      exit 1
    fi
    read u1 s1 < <(awk '{print \$14, \$15}' /proc/\$pid/stat)
    rss1=\$(awk '{print \$2*4}' /proc/\$pid/statm)
    conns=\$(ss -tn state established \"( sport = :$PORT )\" 2>/dev/null | wc -l)
    load=\$(cat /proc/loadavg)
    echo \"\$(date +%H:%M:%S.%3N) cpu_u=\$u1 cpu_s=\$s1 rss=\${rss1}KB conns=\$conns load=\$load\"
  " >> "$OUT" 2>&1
  if [ $? -ne 0 ]; then
    echo "$(date +%H:%M:%S) SSH_UNREACHABLE_OR_PROCESS_NOT_FOUND" >> "$OUT"
  fi
done
