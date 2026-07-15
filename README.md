# gymkhana-time-monitor

屋外モータースポーツイベント(ジムカーナ等の自由走行形式)向けの、通過タイム計測・リアルタイム表示サーバーです。Go + SQLite (modernc.org/sqlite、CGO不要) + SSE (Server-Sent Events) で構築された単一バイナリで、テンプレート・静的ファイルはすべてバイナリに埋め込まれています。会場のRaspberry Pi等にバイナリを1つ置くだけで動きます。

計測はESP32光電センサー(スタート/ゴール、UDP JSON)、または運営による手計測のどちらでも運用できます。

## 主な機能

- **リアルタイム表示**: モニター画面(走行中カード・直近リザルト)、ランキング画面(区分・排気量・駆動方式でフィルタ、ドリルダウン)がSSEで即時更新
- **参加者セルフサービス**: マイページから登録・車両追加(相乗り対応)・出走キュー投入・アイコン編集・ログインQR再表示
- **運営(admin)画面**: 出走管理(センサー/手計測切替、PT・ミスコース・取消・巻き戻し)、キューのドラッグ並べ替え、ユーザー/車両管理、ログ編集、CSVエクスポート、センサー状態監視
- **マルチイベント対応**: アクティブなイベントは常に最大1つ。イベントを終了すると閲覧専用アーカイブになり、ユーザー・車両などの資産はイベントをまたいで共有される
- **認証**: パスワードレス。登録時発行のトークンURL(QRコード)+セッションCookieでログイン
- **自動バックアップ**: 1時間毎に `VACUUM INTO` で `snapshots/` にDBスナップショットを書き出し

詳細な設計・運用ドキュメントはWikiにまとめてあります(下記リンク参照)。

## サーバーセットアップ

### ビルド

```sh
go build ./cmd/timemon
```

Go 1.22+ が必要です。外部依存は `modernc.org/sqlite` と `github.com/skip2/go-qrcode` のみで、`CGO_ENABLED=0` でクロスビルド可能です(CIはlinux/amd64・linux/arm64を配布)。

### 起動例

```sh
./timemon -addr 8080 -udp :9999 -db ./event.sqlite3 -base-url http://192.168.1.10:8080
```

| フラグ | デフォルト | 説明 |
|---|---|---|
| `-addr` | `:8080` | HTTP待受アドレス。ポート番号のみの指定も可(`8080` → `:8080`) |
| `-udp` | `:9999` | センサーUDP待受アドレス |
| `-db` | `./event.sqlite3` | イベントDBファイル。拡張子省略時は `.sqlite3` を自動付与、無ければ自動作成 |
| `-base-url` | (省略時自動検出) | 外部から見えるベースURL(Setup URL・QRコード生成に使用)。省略時はLAN IPを自動検出して `http://<IP>:<port>` を組み立てる |

### 初回セットアップ

DBに運営(admin)が1人も存在しない状態で起動すると、起動ログに一度限りのセットアップURLが出力されます。

```
Setup URL: http://192.168.1.10:8080/setup?t=<token>
```

このURLを開くと、イベント名・計測ルール・換算係数・クラス構成・最初の運営者を設定できます。送信すると同時にこの端末が運営者としてログインし、`/admin` に遷移します。以後 `/setup` は常に404になります(運営が1人以上いる間は無効)。

### リバースプロキシ / Cloudflare 利用時の注意

- SSE (`GET /api/stream`) はプロキシ側でレスポンスバッファリングを無効化しないと配信が滞ります(nginxなら `proxy_buffering off`)。
- サーバー自身が `Cache-Control` を出し分けています(HTML/APIは `no-store`、`/static/` は7日キャッシュ、アイコンは `no-cache` + ETag再検証)。Cloudflare等のCDNはオリジンのヘッダーを尊重する設定にしてください。
- 詳細は Wiki の [Server-Setup](https://github.com/macky34/gymkhana-time-monitor/wiki/Server-Setup) を参照してください。

## イベント運用の流れ

1. **初回セットアップ**: `/setup` でイベント作成 + 最初の運営者登録
2. **参加受付**: 参加者が `/register` から自己登録(運営代理登録も可)、車両登録・相乗りもここから
3. **計測**: `/admin` で出走キューを管理し、センサー(または手計測)で計測。参加者は `/mypage`、観客は `/` (モニター) `/ranking` で確認
4. **イベント終了**: 運営が「イベントを終了する」を実行(コース上に車両が残っている間は不可)。以後そのイベントは閲覧専用になる
5. **アーカイブ閲覧**: `/archive` から終了済みイベントのランキング・記録を閲覧可能(認証不要、常に公開)
6. **新規イベント作成**: 「前回イベントの設定を引き継ぐ」を選ぶと、係数・クラス構成・各種ルールを引き継いだまま次のイベントを開始できる。ユーザー・車両などの資産はイベントをまたいで共有される

詳細な操作手順は Wiki の [Event-Guide](https://github.com/macky34/gymkhana-time-monitor/wiki/Event-Guide) を参照してください。

## ドキュメント (Wiki)

- [Home](https://github.com/macky34/gymkhana-time-monitor/wiki) — Wikiトップ・全体構成図
- [Server-Setup](https://github.com/macky34/gymkhana-time-monitor/wiki/Server-Setup) — ビルド・CLIフラグ・systemd化・リバースプロキシ/Cloudflare設定
- [Event-Guide](https://github.com/macky34/gymkhana-time-monitor/wiki/Event-Guide) — イベント運用フロー・運営画面の操作
- [Pages](https://github.com/macky34/gymkhana-time-monitor/wiki/Pages) — 各画面(モニター/ランキング/アーカイブ/マイページ/運営 等)の機能一覧
- [API](https://github.com/macky34/gymkhana-time-monitor/wiki/API) — 全APIルート一覧・SSEトピック・認証方式
- [Architecture](https://github.com/macky34/gymkhana-time-monitor/wiki/Architecture) — ディレクトリ構成・DBスキーマ・マルチイベント設計
- [CI](https://github.com/macky34/gymkhana-time-monitor/wiki/CI) — CI/CD構成・リリース手順
- [Sensor-Device](https://github.com/macky34/gymkhana-time-monitor/wiki/Sensor-Device) / [RPi-Direct-Sensor](https://github.com/macky34/gymkhana-time-monitor/wiki/RPi-Direct-Sensor) / [Timing-Accuracy](https://github.com/macky34/gymkhana-time-monitor/wiki/Timing-Accuracy) — センサー・計測ハードウェア編

## ライセンス

[LICENSE](./LICENSE) を参照してください。
