---
name: event-sim
description: 一時DBで timemon サーバーを起動し、センサーシミュレータ (tools/sensor-sim.py) で打刻を流してイベント運営フロー全体 (セットアップ→登録→出走→計測→ランキング) をE2Eで動作確認する。計測・キュー・SSE・ランキング周りの変更を実機なしで検証したいときに使う。
---

# イベントシミュレーション (E2E動作確認)

実サーバー + UDPシミュレータで一連の運営フローを再現する。**本物の `event.sqlite3` は絶対に使わない**こと(必ず一時DB)。

## 0. 前提

- リクエスト形式が変わっている可能性があるため、疑わしければ正とするコードを先に確認する:
  `internal/web/setup.go`(セットアップbody)、`internal/web/server.go` の `Routes()`(ルート一覧)、`tools/sensor-sim.py`(送信コマンド)。
- ポートは既定 (8080/9999) を避け、他プロセスと衝突しない値を使う(例: HTTP 18080 / UDP 19999)。

## 1. サーバー起動(バックグラウンド)

```sh
TMP=$(mktemp -d)
go run ./cmd/timemon -db "$TMP/sim.sqlite3" -addr 18080 -udp 19999 \
  -base-url http://127.0.0.1:18080
```

起動ログに以下が出る:
- 未セットアップDB: `Setup URL: http://127.0.0.1:18080/setup?t=<token>`
- セットアップ済みDB: `Emergency admin URL: ...`(今回は使わない)

## 2. セットアップ

`POST /api/setup` に、起動ログのトークンを含むJSONを送る。bodyの正確な形は `internal/web/setup.go` の `setupRequest` を参照(event設定・coefficients・displacement_classes・driver/drivetrainクラス・最初の運営者名とそのdriver_class が必要)。成功レスポンスの `Set-Cookie: tm_session=...` を以後のcurlで使う(`-c/-b cookies.txt`)。

## 3. 運営フロー再現

cookie付きで順に叩く(いずれも `Routes()` が正):

1. `GET /api/admin/users` — 運営者の `login_url` を確認
2. `POST /api/admin/users` — 参加ドライバー作成
3. `POST /api/admin/vehicles` — 車両作成
4. `POST /api/admin/queue` — キュー投入
5. `POST /api/admin/course` — 出走(スタート待ち READY 状態にする)

## 4. センサー打刻

```sh
python3 tools/sensor-sim.py run 83.456 --host 127.0.0.1 --port 19999
```

start→(指定秒後)→goal を3連送で送る(重複排除の検証も兼ねる)。個別打刻は `trigger start` / `trigger goal`。

## 5. 検証

- `GET /api/recent` — 走行ログが生成され、タイムが送信値と一致するか
- `GET /api/ranking` — 換算タイム・順位に反映されたか
- SSEを見る場合: `curl -N http://127.0.0.1:18080/api/stream -b cookies.txt`
- キューに誰もいない状態で打刻した場合は orphan 警告(未割当ログ)になるのが正常

## 6. 後始末

サーバープロセスを止め、`$TMP` を削除する。ポートを使い回す連続実行時は前のプロセスの終了を確認してから起動する。
