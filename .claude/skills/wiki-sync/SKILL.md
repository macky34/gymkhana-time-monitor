---
name: wiki-sync
description: コード・仕様変更後に隣接リポジトリ ../gymkhana-time-monitor.wiki の該当ページを同期更新する。ルート定義・DBスキーマ・SSEトピック・画面構成・CI・ファームウェアのいずれかを変更したら必ずこのスキルを使って対象ページを特定・更新すること。
---

# Wiki 同期

コード変更の内容から更新すべき Wiki ページを特定し、記述が古くなっていないか確認・更新する。Wiki リポジトリはこのリポジトリの隣 `../gymkhana-time-monitor.wiki/` にある。

## 変更箇所 → 対象 Wiki ページ対応表

| 変更したもの | 確認・更新するページ |
|---|---|
| `internal/web/` のルート追加・変更・削除、リクエスト/レスポンス形式 | `API.md`(ルート一覧・認証方式・キャッシュ制御) |
| SSE トピックの追加・変更 (`internal/sse/`, `internal/snapshot/publish.go`) | `API.md`(SSEトピック一覧)、`Architecture.md`(スナップショット/SSEの流れ) |
| DB スキーマ (`internal/store/schema.go`) | `Architecture.md`(DBスキーマ節。テーブルごとに小節がある) |
| ランキング・集計ロジック (`internal/domain/`) | `Architecture.md`(ランキング・集計仕様) |
| 画面構成・UI (`web/templates/*.html`, `web/static/app.js`) | `Pages.md`(ページ/タブ単位の節)、必要なら `Home.md` の概要 |
| 計測・ペアリングロジック (`internal/timing/`) | `Architecture.md`(計測データの流れ)、`Timing-Accuracy.md` |
| センサー UDP プロトコル | `API.md`(センサーUDPプロトコル)、`Sensor-Device.md`、`RPi-Direct-Sensor.md` |
| ファームウェア (`firmware/`) | `Sensor-Device.md` |
| スナップショット/バックアップ (`internal/snapshot/`, hourly VACUUM) | `Architecture.md`(バックアップ節) |
| CI (`.github/workflows/ci.yml`)・ビルド方法 | `CI.md` |
| 起動オプション・デプロイ手順 (`cmd/timemon/main.go`, `defaults.json`) | `Server-Setup.md` |
| イベント運営フロー(イベント作成/終了/アーカイブ) | `Event-Guide.md` |
| パッケージ構成・依存関係 | `Architecture.md`(ディレクトリ構成)、`Home.md`(システム構成図の mermaid) |

## 手順

1. 今回の変更(diff)を確認し、上の表から対象ページを列挙する。
2. 各対象ページの該当節を読み、現状のコードと食い違う記述をすべて洗い出す。
3. ページを更新する。コードに書いていない運用上の背景説明は消さないこと。
4. `Home.md` の mermaid 構成図はパッケージの追加・削除・データフロー変更時のみ更新する。
5. 更新した Wiki ページの一覧と変更概要を報告する。対象ページがない場合も「Wiki 更新不要(理由)」と明示的に報告する。

## 注意

- Wiki は独立した git リポジトリ。コミットはユーザーの指示があった場合のみ行う。
- ページ間の記述重複(例: SSE トピックが API.md と Architecture.md の両方に登場)があるため、片方だけ直して矛盾を作らないこと。
