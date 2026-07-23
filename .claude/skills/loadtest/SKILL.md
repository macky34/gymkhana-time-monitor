---
name: loadtest
description: tools/loadtest/ (Go製、外部依存なし)でtimemonの性能試験を行う。読み取り/書き込み/管理API/SSEの4シナリオを直接接続とCloudflare Tunnel経由の両方で段階的に実行し、サーバー実機をSSH監視しながらボトルネックを見る。実サーバーに負荷をかける作業なので、対象が検証用インスタンスであることを確認してから使うこと。
---

# 性能試験 (tools/loadtest)

`tools/loadtest/`はk6ではなくGo標準ライブラリのみで書かれたCLIツール(`read`/`write`/`admin`/`sse`の4サブコマンド)。k6 + xk6-sse拡張を試みたが、拡張が全公開バージョンでレジストリ上vulnerability指摘があり、k6 v2の新モジュールパスに未対応、かつk6のシナリオ強制中断にも対応しておらず接続を無期限にブロックすることが分かったため、この構成に落ち着いた経緯がある。

## 0. 前提条件(必ず確認)

- **対象が検証用インスタンスであること。** 本番イベント中の実データに対して負荷をかけない。書き込み系(`write`/`admin`)は実データを作成・変更する。
- `go build ./tools/loadtest && go vet ./tools/loadtest`が通ること。
- `curl <base-url>/api/settings`で`registration_open: true`(かつ`registration_mode != "staff"`)と、`driver_classes`/`dt_classes`から使う`id`を確認する(`write`サブコマンドで必須)。
- 管理者セッション(`admin`サブコマンド用)。緊急管理者トークン(起動ログの"Emergency admin URL")は`withAdmin`ルート(`/api/admin/queue`, `/api/admin/course/*`等)からは仕様上拒否されるので使えない。緊急管理者の`withUserAdmin`権限(`POST /api/admin/users`→`PUT /api/admin/users/{id}/role`で`role:"admin"`に昇格)で試験専用の管理者アカウントを作るのが確実。
- サーバー実機へのSSHアクセス(直接接続先が別筐体の場合、CPU/メモリを監視するのに必須)。

## 1. 実行方法

```sh
go run ./tools/loadtest read  -url http://<直接IP>:8080 [-workers 300]
go run ./tools/loadtest write -url http://<直接IP>:8080 -driver-class-id <id> -drivetrain-class-id <id> [-workers 50]
go run ./tools/loadtest admin -url http://<直接IP>:8080 -admin-cookie <tm_sessionの値> [-driver-id <id> -vehicle-id <id>] [-workers 100]
go run ./tools/loadtest sse   -url http://<直接IP>:8080 [-workers 200] [-hold 30]
```

- 各サブコマンドは10→50→100→300のようにワーカー数(≒同時接続数)を段階的に増やすramping executorを内蔵している(k6のramping-vus相当、`ramp.go`)。
- `write`はmypage系のIPベースレート制限(10 req/10秒、`internal/web/ratelimit.go`)に配慮し、ワーカーごとに疑似`CF-Connecting-IP`を送って回避しつつ、1イテレーションあたり2.5秒の追加待ちで持続可能レート内に収めている。**この待ち時間はレイテンシ計測から除外済み**(`write.go`の`elapsed`)。
- Cloudflare Tunnel経由(`-url https://<公開ホスト名>`)では`CF-Connecting-IP`の偽装がCloudflare自身に上書きされ通用しないため、`write`はTunnel経由では参考程度に低ワーカー数でのみ実行すること。

## 2. サーバー実機監視

```sh
tools/loadtest/monitor.sh <ssh-target> <出力ファイル> <実行秒数> [ssh鍵パス] [ポート]
# 例:
tools/loadtest/monitor.sh mac@192.168.100.81 /tmp/monitor.log 600 ~/.ssh/id_ed25519_timemon 8080 &
```

対象プロセスのPIDは`pgrep -f timemon-linux`で毎回動的に探すので、再起動でPIDが変わっても固定値の書き換え不要。CPU%は`/proc/<pid>/stat`のutime+stime累積tickなので、瞬間使用率が要る場合は2サンプル間の差分を`CLK_TCK`(通常100)と経過秒数で割ること(`ps`の`%cpu`はプロセス起動からの累積平均になり、瞬間値としては使えない)。

## 3. 安全上の注意 (実際に遭遇した事象)

- **同時SSE接続数を無闇に増やさない。** ある試験で4000同時接続を**間隔を空けず連続実行**(500→1000→2000→4000)した際、各回の一時的なメモリピーク(Goランタイムがヒープをまだ返却していない状態)が回収前に積み重なり、swapなしの4GB機でOOMを起こしてサーバーが完全無応答になった実績がある。**単発では**2000接続まで安全(RSS数百MB、CPU余裕あり)、4000は動くがピークRSSが3〜3.4GBまで達し数十秒〜数分かけて回収される。Tunnel経由は同じ接続数でもcloudflaredの分メモリ消費が重く、2000接続で空きメモリが200MB台まで落ちる(直接接続より先に危険域に入る)。
- **試験の合間に回復を待つこと。** 重い試験の直後は`ssh <target> "ps -o rss= -p \$(pgrep -f timemon-linux)"`でRSSが数十MB程度まで下がるのを確認してから次の試験に進む。
- **admin-apiはSQLite書き込み直列化(`journal_mode=WAL`, `SetMaxOpenConns(8)`, `internal/store/store.go`)の影響で最もCPUを使う。** 100並列で4コア中2コア分程度を使う。複数ワーカーが同じ待機列/コース状態を奪い合うため409(Conflict)は想定内のレースとして扱う(`okStatus`)。
- **Cloudflare Tunnel経由は「同時接続数」より「接続の張り直し頻度」に弱い。** 短命リクエストを高頻度で張り直すread-apiだけがTunnel経由でCPU急増・エラー多発した一方、SSE(少数の長時間接続)はTunnel経由でも軽微だった。Tunnel側の`keepAliveConnections`(オリジンとの接続プール)や`ha-connections`(エッジとの接続数、`systemd`の`ExecStart`に`--ha-connections N`を追加)を調整しても改善しないケースがあり、原因が試験クライアント側(接続プール設定等)にあることもあるため、**変更前後で必ず同条件の再試験をして効果を数値で確認すること**。
- **Cloudflareダッシュボードの設定変更で解決しない場合は、Cloudflare APIで直接読み書きできる**(`GET/PUT https://api.cloudflare.com/client/v4/accounts/{account_id}/cfd_tunnel/{tunnel_id}/configurations`、要Tunnel編集権限のAPIトークン)。**試験目的で変更した設定は必ず元に戻す**(`PUT`で`originRequest`を外した元の設定を書き戻す、`systemd`のunitファイルもバックアップから復元して`daemon-reload && restart`)。

## 4. 参考値 (このリポジトリでの実測、目安)

| シナリオ | 経路 | 目安上限 | 備考 |
|---|---|---|---|
| read-api | 直接接続 | 300並列で0エラー(p95<300ms) | 76000req以上でも無傷 |
| read-api | Tunnel経由 | 300並列で~10%エラー | 短命リクエスト連打に弱い |
| write-api | 直接接続 | 50並列で0エラー | SQLite直列化でp99が数秒に伸びる |
| admin-api | 直接接続 | 100並列で0エラー(409は正常) | CPU最も重い(4コア中2コア分) |
| SSE | 直接接続 | 2000接続まで安全、4000は動くがメモリ限界 | |
| SSE | Tunnel経由 | 1000〜2000が実質上限 | cloudflared分メモリが重い |

これらは実行時点のハードウェア・ネットワーク状況に依存するため鵜呑みにせず、試験のたびに実測すること。
