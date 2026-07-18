---
name: live-check
description: コミット前の人間による最終確認用。バイナリをビルドし、本番 event.sqlite3 の一時コピーで別ポート起動して、ログインURLをユーザーに提示し実データでの動作確認をしてもらう。本番DB・本番バイナリ・稼働中プロセスには一切触れない。
---

# 実データ起動確認 (コミット前・人間による確認)

変更後のコードを**実データ**(本番 `event.sqlite3` のコピー)で起動し、**ユーザー自身に実際に触って確認してもらう**。Claude が行う自動E2E(Playwright)とは別物 — このスキルの目的はサーバーを起動してユーザーに引き渡すことであり、Claude が代わりに画面確認して済ませてはいけない。

## 絶対に守ること

- 本番 `event.sqlite3`(既定: `/home/mac/src/event.sqlite3`)への**書き込み・直接オープンは禁止**。必ずコピーに対して起動する。
- 本番バイナリ `/home/mac/src/timemon` を**上書きしない**(ビルド出力は一時ディレクトリへ)。
- 本番サーバーが稼働中でも影響しないよう、**別ポート**(HTTP 18081 / UDP 19998。event-sim の 18080/19999 とも重複させない)を使う。
- コピーDBの運営トークンは実ユーザーのトークンなので、ログインに使う1件以外は表示しない。

## 手順

1. **ビルド**(本番バイナリとは別パスへ):
   ```sh
   TMP=$(mktemp -d)
   cd /home/mac/src/gymkhana-time-monitor && go build -o "$TMP/timemon-check" ./cmd/timemon
   ```
2. **DBの一貫性コピー**(WAL書き込み中でも安全な backup API を使う。cp は不可):
   ```sh
   python3 - "$TMP" <<'EOF'
   import sqlite3, sys
   src = sqlite3.connect("file:/home/mac/src/event.sqlite3?mode=ro", uri=True)
   dst = sqlite3.connect(sys.argv[1] + "/live-copy.sqlite3")
   src.backup(dst); dst.close(); src.close()
   EOF
   ```
3. **起動**(バックグラウンド)して疎通だけ確認する(`/api/settings` が200):
   ```sh
   "$TMP/timemon-check" -db "$TMP/live-copy.sqlite3" -addr 18081 -udp 19998 -base-url http://<ホストのLAN IP>:18081
   ```
4. **運営ログインURLの用意**: コピーDBから運営トークンを1件取得する:
   ```sh
   python3 -c "import sqlite3; print(sqlite3.connect('$TMP/live-copy.sqlite3').execute(\"SELECT token FROM drivers WHERE role='admin' AND is_deleted=0 LIMIT 1\").fetchone()[0])"
   ```
   ユーザーは別マシンのブラウザから開くため、URLは 127.0.0.1 ではなく**ホストのLAN IP**(`hostname -I` で確認)で組み立てる: `http://<LAN IP>:18081/a/<token>`
5. **ユーザーへの引き渡し**: 以下を伝えて、**ユーザー自身の動作確認を待つ**:
   - 運営ログインURLと、公開ページ(`/`, `/ranking`)のURL(いずれもLAN IPで)
   - 実データのコピーなので、**何を操作しても本番には一切反映されない**こと
   - **センサー打刻コマンドの案内**: ユーザーが自分で打刻を試せるよう、そのままコピペで実行できる形で必ず提示する(`! <コマンド>` でこのセッションから実行できることも添える):
     ```sh
     # スタート / ゴール打刻(1回)
     python3 /home/mac/src/gymkhana-time-monitor/tools/sensor-sim.py trigger start --host 127.0.0.1 --port 19998
     python3 /home/mac/src/gymkhana-time-monitor/tools/sensor-sim.py trigger goal --host 127.0.0.1 --port 19998
     # 走行シミュレーション(start → 指定秒後に goal)
     python3 /home/mac/src/gymkhana-time-monitor/tools/sensor-sim.py run 83.456 --host 127.0.0.1 --port 19998
     # ハートビート(センサー状態パネルに反映)
     python3 /home/mac/src/gymkhana-time-monitor/tools/sensor-sim.py hb --host 127.0.0.1 --port 19998
     ```
     コマンドは実行環境のカレントディレクトリに依存しないよう**必ず絶対パス**で提示する。
   - あわせて、今回の変更で確認してほしい操作手順(どの画面で何をすると何が起きるはずか)を箇条書きで案内する
6. **後始末**: ユーザーの確認が取れたらサーバープロセスを停止し、`$TMP` を削除する。問題の指摘があれば修正してからやり直す。**ユーザーの確認を得てからコミットに進む**こと。

## 補足

- 実DBのスキーマは古い可能性がある(`CREATE TABLE IF NOT EXISTS` のみでマイグレーションしない設計)。新しい列や既定値に依存する変更は実DBでは効かないケースがあり、それを人間の目で発見するのもこのチェックの目的のひとつ。
