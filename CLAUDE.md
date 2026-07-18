# CLAUDE.md

このファイルは本リポジトリで作業する際の開発運用ルールです。

## ブランチ運用

修正・機能追加は `main` に直接コミットせず、**必ず別ブランチを作成して実施すること**(例: `fix/...`, `feat/...`)。

## Wiki同期

コード・仕様を変更した場合は、隣接リポジトリ `../gymkhana-time-monitor.wiki` の該当Wikiページ(Home / Server-Setup / Event-Guide / Pages / API / Architecture / CI / Sensor-Device / RPi-Direct-Sensor / Timing-Accuracy)も**常に**更新すること。ルート定義・DBスキーマ・SSEトピック・画面構成のいずれかを変えたら、対応するWikiページの記述が古くなっていないか必ず確認する。

## メインエージェントがFable/Opusの場合の実装フロー

1. 詳細設計書は**メインエージェント(オーケストレーター)自身が作成する**。サブエージェントに設計書を書かせない。
2. 実装はSonnetクラスのサブエージェントに委譲する。エージェントには仕様書・対象ファイルパス・関連コード抜粋を含む**自己完結の指示書のみ**を渡し、コードベース全体の探索(無差別なGrep/Glob)はさせない。トークン消費を抑えるため、読むべきファイルを具体的に指定する。
3. **委譲は1階層まで。** サブエージェントがさらにサブエージェント(孫エージェント)を起動することは禁止。指示書には「Agentツールでの再委譲は禁止。自分で直接実装すること」を必ず明記する(設計→孫委譲はトークンの二重消費になるため)。
4. エージェントはタスク完了後、実装結果に加えて「確認事項」(判断が必要だった点・懸念点・仕様との食い違い)を必ず報告させる。

例外として、横断的な改修等で10を超えるファイルをサブエージェントに読み込ませる必要がある場合や、逆にごく少量の変更など、トークン削減のためサブエージェントを起動せず直接実行した方がいい場合もありえるため、適宜判断すること。

## ビルド/テスト

変更後は常に以下を通すこと:

```sh
go build ./...
go vet ./...
go test ./...
gofmt -l .   # 出力が空であること
```

## Claude Code 自動化設定 (.claude/)

このリポジトリには以下のフック・スキル・エージェントが設定されている。規約と重複するものはフックが機械的に強制するので、Claude側で意識する必要はないが、存在は把握しておくこと。

- **フック** (`.claude/settings.json` + `.claude/hooks/`):
  - `block_main_commit.py` — main への直接コミットをブロック(ブランチ運用ルールの強制)
  - `gofmt.py` — Edit/Write された .go を自動整形
  - `block_vendored.py` — ベンダー配布物 (`*.min.js` / `*.min.css`) の直接編集をブロック
  - `go_verify.py` — 応答終了時、.go に変更があれば `go build` / `go vet` を検証し失敗なら差し戻す
- **スキル**: `/release`(リリースタグ作成)、`/wiki-sync`(Wiki同期)、`/event-sim`(シミュレータE2E動作確認)、`/new-admin-api`(管理API追加チェックリスト)
- **エージェント**: `implementer`(指示書ベースの実装)、`web-security-reviewer`(XSS・認可監査)、`concurrency-reviewer`(排他・レース監査)。internal/web や web/ を変更したら web-security-reviewer、store/sse/timing の並行性に触れたら concurrency-reviewer でのレビューを検討する。

## 補足

- 外部依存は `modernc.org/sqlite` と `github.com/skip2/go-qrcode` のみ。フロントエンドはVanilla JS/CSSでビルドツール不使用、CDN不使用(`web/static/` にセルフホスト)。
- ユーザー入力のDOM反映は `textContent` 系のみ(`innerHTML` 禁止)。
