---
name: release
description: バージョンタグを作成して push し、CI のリリースジョブ(マルチアーチビルド + GitHub Release)を起動する。引数で v1.2.3 形式のバージョンを指定可能。省略時は変更内容から次バージョンを提案する。
disable-model-invocation: true
---

# リリース手順

タグ `v*` を push すると CI (`.github/workflows/ci.yml`) が verify → linux amd64/arm64 ビルド → GitHub Release 作成まで自動実行する。このスキルはその起点となるタグ作成を安全に行う。

引数: `$ARGUMENTS`(例: `v1.2.3`。省略可)

## 手順

1. **前提確認**(1つでも満たさなければ中断して報告):
   - `git -C . status --porcelain` が空(未コミット変更なし)
   - 現在のブランチが `main` で、`git fetch` 後に `origin/main` と一致している
   - 直近の main の CI が成功している: `gh run list --branch main --limit 1`
2. **バージョン決定**:
   - 引数があればそれを使う(`v` 始まりの semver 形式であることを検証)。
   - なければ `git describe --tags --abbrev=0` で最新タグを取得し、前回タグ以降のコミット (`git log <tag>..HEAD --oneline`) を要約して patch/minor どちらを上げるべきか提案し、ユーザーの承認を得る。
3. **タグ作成と push**:
   ```sh
   git tag -a vX.Y.Z -m "Release vX.Y.Z"
   git push origin vX.Y.Z
   ```
4. **CI 監視**: `gh run watch` またはポーリングでリリースジョブの完了を確認し、`gh release view vX.Y.Z` で成果物(timemon-linux-amd64 / arm64)が添付されたことを確認して報告する。
5. 失敗時はタグを安易に削除せず、失敗ログ (`gh run view --log-failed`) を報告して指示を仰ぐ。

## 注意

- タグの push は取り消しが面倒な外向きの操作。手順 3 の実行前に、決定したバージョンと前回タグからの変更概要を必ず表示すること。
- ファームウェア (`firmware/`) はリリース資産に含まれない(CI の firmware ジョブは変更時のみビルドされる artifact)。
