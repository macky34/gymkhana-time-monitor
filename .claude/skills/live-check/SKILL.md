---
name: live-check
description: 動作確認を一本化したスキル。Phase A (sim) では一時DBで timemon を起動し tools/sensor-sim.py で打刻を流してイベント運営フロー全体をClaudeが自動E2E検証する。Phase B (live) では本番 event.sqlite3 の一時コピーで別ポート起動し、tools/dual-browser.sh でPC/スマホ画面をlight/darkテーマ同時(既定4画面)に開いた上でユーザー自身に実データでの動作確認をしてもらう。本番DB・本番バイナリ・稼働中プロセスには一切触れない。
---

# 動作確認 (sim → live 統合)

変更内容の検証を2段階で行う。

- **Phase A (sim)**: 一時DBで timemon を起動し、センサーシミュレータで打刻を流して Claude 自身がAPIレベルでE2E検証する。UIに影響する変更の場合はブラウザ検証もここで行う(X11転送が使える場合は `tools/dual-browser/phase-a.sh` でPC×light・SP×light・PC×dark・SP×darkの計4窓のheadfulウィンドウを同時に開き対話操作、使えない場合は従来どおり Playwright MCP (headless) でスクリーンショット確認。手順6参照)。
- **Phase B (live)**: 本番 `event.sqlite3` の一時コピーで別ポート起動し、**ユーザー自身に実際に触って確認してもらう**。Claude が行う自動E2E(Playwright)とは別物 — このフェーズの目的はサーバーを起動してユーザーに引き渡すことであり、Claude が代わりに画面確認して済ませてはいけない。

## 引数

- `/live-check`(引数なし): Phase A → Phase B を連続実行する。
- `/live-check sim`: Phase A のみ実行する。
- `/live-check live`: Phase B のみ実行する。
- `/live-check --no-browser`: Phase B の画面起動 (`tools/dual-browser.sh`、既定4窓) を抑止し、URLだけを提示するモードにする(`sim`/`live` と組み合わせ可能。例: `/live-check live --no-browser`)。

## Phase 0: 共通準備

- **残留プロセス掃除**: `ss -tlnp | grep -E ':(1808[0-9])'` で18080番台のリスニングを確認する。見つかった場合、**プロセスのコマンドライン (`ps -p <pid> -o args=`) を確認し、`-db` に本番パス (`/home/mac/src/event.sqlite3` そのもの、コピーではない) を指定しているものは絶対に停止しない**。それ以外(一時DBやコピーDBを指しているもの)は前回ジョブの取り残しの可能性が高いので、ユーザーに一言断ってから停止する。
- **LAN IP解決**: 次のコマンドに統一する(`hostname -I` はこの環境では空を返すため使わない):
  ```sh
  ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -1
  ```
- **ビルド**: Phase A は `go run` のままでよい。Phase B 用ビルドは `go build -o "$TMP/timemon-check" ./cmd/timemon`。

### ポート割当

| 用途 | sim (Phase A) | live (Phase B) |
|---|---|---|
| timemon HTTP | 18080 | 18081 |
| timemon UDP | 19999 | 19998 |

`.claude/launch.json` の `timemon-dev` 設定 (18081/19998) と衝突するため、Phase B とデバッグ起動 (`timemon-dev`) は同時起動不可であることに注意する。

## Phase A (sim)

実サーバー + UDPシミュレータで一連の運営フローを再現する。**本物の `event.sqlite3` は絶対に使わない**こと(必ず一時DB)。このフェーズでは Phase B用の `tools/dual-browser.sh`(PC/SP×light/dark既定4窓常時起動)そのものは使わない(変更禁止のため)。UI確認は `tools/dual-browser/phase-a.sh`(X11転送が使える場合、Phase Bと同じPC×light・SP×light・PC×dark・SP×darkの4窓を同時起動して対話操作)または Playwright MCP (headless、X11が使えない場合のフォールバック)で行う。手順6参照。

### 1. 前提

- リクエスト形式が変わっている可能性があるため、疑わしければ正とするコードを先に確認する:
  `internal/web/setup.go`(セットアップbody)、`internal/web/server.go` の `Routes()`(ルート一覧)、`tools/sensor-sim.py`(送信コマンド)。
- ポートは既定 (8080/9999) を避け、上記の表のとおり HTTP 18080 / UDP 19999 を使う。

### 2. サーバー起動(バックグラウンド)

```sh
TMP=$(mktemp -d)
go run ./cmd/timemon -db "$TMP/sim.sqlite3" -addr 18080 -udp 19999 \
  -base-url http://127.0.0.1:18080
```

起動ログに以下が出る:
- 未セットアップDB: `Setup URL: http://127.0.0.1:18080/setup?t=<token>`
- セットアップ済みDB: `Emergency admin URL: ...`(今回は使わない)

### 3. セットアップ

`POST /api/setup` に、起動ログのトークンを含むJSONを送る。bodyの正確な形は `internal/web/setup.go` の `setupRequest` を参照(event設定・coefficients・displacement_classes・driver/drivetrainクラス・最初の運営者名とそのdriver_class が必要)。成功レスポンスの `Set-Cookie: tm_session=...` を以後のcurlで使う(`-c/-b cookies.txt`)。

### 4. 運営フロー再現

cookie付きで順に叩く(いずれも `Routes()` が正):

1. `GET /api/admin/users` — 運営者の `login_url` を確認
2. `POST /api/admin/users` — 参加ドライバー作成
3. `POST /api/admin/vehicles` — 車両作成
4. `POST /api/admin/queue` — キュー投入
5. `POST /api/admin/course` — 出走(スタート待ち READY 状態にする)

### 5. センサー打刻

```sh
python3 tools/sensor-sim.py run 83.456 --host 127.0.0.1 --port 19999
```

start→(指定秒後)→goal を3連送で送る(重複排除の検証も兼ねる)。個別打刻は `trigger start` / `trigger goal`。

### 6. 検証

- `GET /api/recent` — 走行ログが生成され、タイムが送信値と一致するか
- `GET /api/ranking` — 換算タイム・順位に反映されたか
- SSEを見る場合: `curl -N http://127.0.0.1:18080/api/stream -b cookies.txt`
- キューに誰もいない状態で打刻した場合は orphan 警告(未割当ログ)になるのが正常
- UIに影響する変更(web/templates/・web/static/・internal/web のハンドラやSSEペイロード)の場合は、ここでブラウザ検証を行う。まず `tools/dual-browser/phase-a.sh --base-url http://127.0.0.1:18080 --path /` を実行し、環境に応じて経路を分岐する(いずれも Playwright ベースだが、Phase B用の `tools/dual-browser.sh`/`dual-browser/launch.mjs` とは別物で、この用途専用に **PC×light・SP×light・PC×dark・SP×darkの計4窓** を対話操作するための仕組み。Phase Bの既定4窓構成と同じ考え方):
  - **exit 0(X11転送が使える)**: 標準出力に出る**4行**(`pc-light <PID> <PORT>` / `sp-light <PID> <PORT>` / `pc-dark <PID> <PORT>` / `sp-dark <PID> <PORT>` の順)を全て控える。以後、各組み合わせを操作したい手順では対応するポートを指定して `node tools/dual-browser/phase-a-cmd.mjs <PORT> '<JSON action>'` を1ステップずつ実行する(action例は同スクリプト冒頭のコメント、または後述参照)。4窓とも起動時からテーマが固定されているため、**`emulateMedia` での切り替えはもう不要**。この経路の主旨は、Phase Aは元々Claudeが操作する自動検証であるという前提のもとで、**ヘッドレスで見えない場所で完結させず、可能な場合はユーザーの画面にも実際の4つのウィンドウを表示しながら対話操作する**ことである(Phase Bの「Claudeが代わりに操作して確認を済ませてはいけない」とは主旨が異なる — Phase Aは元々Claude自身が検証して良いフェーズであり、ここでの目的はheadlessで隠れて完結させないことにある)。
    - トークンURLでログイン→画面遷移→ボタン/モーダル操作は、機能検証したい1組み合わせ(例: pc-light)のポートに対して `{"action":"goto",...}` / `{"action":"click","selector":...}` / `{"action":"fill","selector":...,"value":...}` / `{"action":"screenshot","path":...}` / `{"action":"eval","expr":...}` を都度実行して確認する。
    - **SSEによるライブ更新の確認**: いずれか1つの窓(例: pc-light)のポートで運営操作やセンサー打刻などを行った直後に、他の窓(例: sp-light、または残り全ての窓)のポートに対して `{"action":"screenshot","path":...}` を実行し、操作結果が即座に反映されているかを確認する(これが4窓同時起動にした主目的)。
    - 検証が終わったら **4つのPID全て** に対して `kill`(SIGTERM)を送ってウィンドウを閉じる(一部だけ閉じて終わらないよう注意)。
  - **exit 3 / exit 5(X11が使えない)**: 従来どおり Playwright MCP (headless) を使い、`browser_navigate`/`browser_resize`/`browser_run_code_unsafe`(`page.emulateMedia`)/`browser_take_screenshot` でトークンURLログイン→画面遷移→ボタン/モーダル操作→SSEによるライブ更新を確認し、スクリーンショットをチャットに貼って確認する。
  - **exit 4 / exit 6(異常系)**: Phase Bの `tools/dual-browser.sh` と同種のexitコードなので同様に扱う。exit 4はChromiumバイナリが見つからない異常系(Playwright MCPのキャッシュ `~/.cache/ms-playwright/` が壊れていないか確認)、exit 6は `tools/dual-browser/` 配下への `playwright-core` の自動インストール失敗(`npm install --prefix tools/dual-browser` を手動実行)。いずれもフォールバックとして Playwright MCP (headless) で検証を続行する。
  - あわせて **PC(1280x800)×SP(390x844) × light×dark の4通り**を見た目の崩れがないかスイープする。exit 0 経路では4窓それぞれサイズ・テーマが最初から固定で起動しているため、リサイズや `emulateMedia` の切り替え操作は不要。機能検証した1組み合わせ(例: pc-light)と同じ画面遷移を、残り3組み合わせのポートにも `{"action":"goto",...}` で反映させたうえで、それぞれ `{"action":"screenshot","path":...}` を実行するだけでよい。exit 3/5 経路(Playwright MCP)では従来どおり `browser_resize` でビューポートサイズを切り替え、`browser_run_code_unsafe` で `async (page) => { await page.emulateMedia({ colorScheme: 'dark' }); }`(light に戻す場合は `'light'`)を実行してテーマを切り替える手順の目安:
    1. PCサイズに `browser_resize` → `colorScheme: 'light'` にして画面遷移・スクリーンショット
    2. 同じサイズのまま `colorScheme: 'dark'` に切り替えてスクリーンショット
    3. SPサイズに `browser_resize` → 同様に light→dark でスクリーンショット
  - **実際の機能検証(ボタン/モーダル操作・SSEライブ更新の確認など)は1組み合わせ(例: PC×light)だけで行えば十分**で、残り3組み合わせは見た目が崩れていないかのスクリーンショット確認とコンソールエラー有無の確認だけでよい(exit 3/5経路では `browser_console_messages` を使う。exit 0経路は `phase-a-cmd.mjs` にコンソールログ収集アクションがないため、対話操作中にユーザーの画面に表示されているウィンドウで開発者ツールを開いて目視確認するか、疑わしい場合は該当ステップだけ Playwright MCP に切り替えて `browser_console_messages` で確認する)。4通り全部で同じ機能テストを繰り返す必要はない。

### 7. 後始末

サーバープロセスを止め、`$TMP` を削除する。ポートを使い回す連続実行時は前のプロセスの終了を確認してから起動する。

## Phase B (live)

変更後のコードを**実データ**(本番 `event.sqlite3` のコピー)で起動し、**ユーザー自身に実際に触って確認してもらう**。

### 絶対に守ること

- 本番 `event.sqlite3`(既定: `/home/mac/src/event.sqlite3`)への**書き込み・直接オープンは禁止**。必ずコピーに対して起動する。
- 本番バイナリ `/home/mac/src/timemon` を**上書きしない**(ビルド出力は一時ディレクトリへ)。
- 本番サーバーが稼働中でも影響しないよう、**別ポート**(HTTP 18081 / UDP 19998。sim の 18080/19999 とも重複させない)を使う。
- コピーDBの運営トークンは実ユーザーのトークンなので、ログインに使う1件以外は表示しない。

### 手順

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
   起動ログに `store: migrated to version N (...)` の行が出ていないか確認する。スキーマ変更(新しい列・マイグレーション追加)を含む変更では、この行が**出ていること**、かつ `$TMP` 内に `snapshots/premigrate-*.sqlite3` が生成されていることを確認する(生成されていなければ、意図したマイグレーションが実行されていない)。スキーマ変更を含まない変更では、この行が出ない(適用対象なし)のが正常。
4. **運営ログインURLの用意**: コピーDBから運営トークンを1件取得する:
   ```sh
   python3 -c "import sqlite3; print(sqlite3.connect('$TMP/live-copy.sqlite3').execute(\"SELECT token FROM drivers WHERE role='admin' AND is_deleted=0 LIMIT 1\").fetchone()[0])"
   ```
   ユーザーは別マシンのブラウザから開くため、URLは 127.0.0.1 ではなく**ホストのLAN IP**(Phase 0 のコマンドで確認)で組み立てる: `http://<LAN IP>:18081/a/<token>`
5. **人間への引き渡し**:
   まず `tools/dual-browser.sh` を実行して画面同時表示を試みる(`--no-browser` 指定時はこの実行自体をスキップし、URLだけを提示する)。**既定でPC×light・SP×light・PC×dark・SP×darkの計4窓が縦2行に並んで起動する**(内部でPlaywrightの color-scheme emulation を使ってウィンドウを開くため、初回のみ `tools/dual-browser/` 配下でのインストールが走ることがある):
   ```sh
   tools/dual-browser.sh --base-url http://<LAN IP>:18081 \
     --pc-path /a/<token> --sp-path /a/<token>
   ```
   ダーク/ライトどちらか片方だけでよい場合は `--themes light` または `--themes dark` を指定すると1行(PC+SPの2窓)に絞れる。
   - **exit 0**: 「手元の画面にPC窓・スマホ窓がlight/darkそれぞれ開いています」と伝える(標準出力に出るのはNodeラッパープロセスのPID1つ)。
   - **exit 3 / exit 5 (X11が使えない)**: 従来どおり `http://<LAN IP>:18081/a/<token>` と、公開ページ (`/`, `/ranking`) のURLを提示する。あわせて、次節「X11転送を有効にする(RLogin + VcXsrv)」の該当手順を貼って案内する(exit 3 なら手順2のRLogin設定、exit 5 なら手順1のVcXsrv設定を優先して示す)。X11を使わない場合のスマホ確認手段も添える: 手元ブラウザのDevToolsデバイスエミュレーション(F12→Ctrl+Shift+M)、または同じLAN内の実機スマホで同じURLを開く方法(タッチ・ソフトキーボードまで確認したいならこちらが確実)。
   - **exit 4**: Chromiumバイナリが見つからない異常系。Playwright MCPのキャッシュ (`~/.cache/ms-playwright/`) が壊れていないか確認するよう伝える。
   - **exit 6**: `tools/dual-browser/` 配下への `playwright-core` の自動インストールに失敗した異常系。`npm install --prefix tools/dual-browser` を手動実行するよう伝える。

   いずれの場合も、既存どおり**センサー打刻コマンドを絶対パスで提示**し(ユーザーが自分で打刻を試せるよう、そのままコピペで実行できる形で必ず提示する。`! <コマンド>` でこのセッションから実行できることも添える):
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
   あわせて、今回の変更で確認してほしい操作手順(どの画面で何をすると何が起きるはずか)を箇条書きで案内する。実データのコピーなので、**何を操作しても本番には一切反映されない**ことも伝える。
6. **後始末**: ユーザーの確認が取れたら timemon サーバープロセスを停止し、`$TMP` を削除する。`tools/dual-browser.sh` が起動した窓は、標準出力に出た**Nodeラッパーの1つのPIDへ `kill` (SIGTERM) するだけで4窓すべてが道連れで正常closeされる**(全ウィンドウ・プロファイルディレクトリのプロセスを個別に停止する必要はない。プロファイルディレクトリ自体は `$PROFILE_DIR`/一時ディレクトリなので、必要なら合わせて削除する)。問題の指摘があれば修正してからやり直す。**ユーザーの確認を得てからコミットに進む**こと。

### 明記する制約

- X11越しのChromiumは描画が重く、日本語フォントもサーバー側のIPAゴシック等になるためWindows実機のフォント(Meiryo/Yu Gothic)とは字形が異なる。フォント依存のレイアウト崩れの最終判断には使えない。
- **Claudeが起動した画面を代わりに操作して確認を済ませてはいけない**。目的は人間に引き渡すこと。

## 補足

- 実DBのスキーマは起動時に `internal/store/migrate.go` の仕組み(`PRAGMA user_version` ベース)で自動的に前進する(`internal/store/schema.go` の `schemaSQL` は最新形のみを表し、既存DBへの反映はマイグレーションの役目)。適用前に `snapshots/premigrate-*.sqlite3` へバックアップが取られ、失敗時は起動自体が失敗する設計。新しい列や既定値に依存する変更が実データコピーで本当に効くかを人間の目で確認するのが Phase B の目的のひとつ。

## X11転送を有効にする (RLogin + VcXsrv)

`tools/dual-browser.sh` が exit 3 または 5 を返したとき、この手順を該当部分だけ抜粋してユーザーに案内する。手元はWindows+RLoginを想定。RLoginは内蔵Xサーバーを持たないため、VcXsrv等が別途必要。

1. **VcXsrvをXLaunchで起動**(初回のみインストール):
   - Multiple windows / Display number: 0
   - Start no client
   - Extra settings: 「Disable access control」にチェック、「Native opengl」はオフ(オンだとChromiumが起動直後に落ちることがある)
2. **RLoginでX11ポートフォワードを有効化**: メニューバー [表示] → [オプション] → 「プロトコル」→「ポートフォワード」→「X11ポートフォワードを使用する」にチェック(サーバー設定編集ダイアログの「プロトコル」からも同じ設定に入れる)。設定後、接続し直す。
3. 接続後に `echo $DISPLAY` が `localhost:10.0` のように出れば成功。
4. 疎通テスト: `xdpyinfo | head` または `xeyes`。

手元から対象ホストに直接届かない場合は、RLoginのローカルポートフォワード([表示] → [オプション] → 「プロトコル」→「ポートフォワード」→「新規」、Listened: Local/localhost/<ポート>、Connect: 127.0.0.1/<ポート>)で `http://localhost:<ポート>/` から到達できる。X11とは独立した設定。
