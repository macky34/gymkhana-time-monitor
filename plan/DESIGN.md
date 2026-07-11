# タイムモニターシステム 設計書 v2 (実装用)

屋外モータースポーツイベント(ジムカーナ等の自由走行形式)向けの、通過タイム記録・リアルタイム表示システム。
本書は実装着手可能な完全版。UIの見た目・挙動は同梱の `docs/timemon-mockup.html` (クリッカブルモック) を正とする。

---

## 0. 実装上の絶対制約

- **Backend:** Go 1.22+。Webフレームワーク不使用。`net/http` の `ServeMux` (メソッド+パスパターン、`r.PathValue`) を使う。
- **許可する外部依存はこの2つのみ**: `modernc.org/sqlite` (CGO不要ドライバ)、`github.com/skip2/go-qrcode` (QR PNG生成)。それ以外は標準ライブラリで書く。`go mod vendor` でvendor同梱、`CGO_ENABLED=0`。
- **Frontend:** Vanilla JS / Vanilla CSS。ビルドツール(Node/npm/webpack)不使用。CSSはHTMLの `<style>` に直書き。外部CDN不使用 (Cropper.jsは `/static/` にセルフホスト)。
- **DB:** SQLite3。1イベント = 1 DBファイル (使い捨て)。
- **リアルタイム:** SSE。WebSocket不使用。
- **配布:** `go:embed` で静的ファイル・デフォルト設定を焼き込んだLinuxシングルバイナリ (arm64=RPi5本番 / amd64=開発)。
- ユーザー入力のDOM反映は必ず `textContent` 系 (innerHTML禁止、XSS対策)。
- 帯域制約 (上り1.5Mbps/実効180KB/s) が最上位の設計制約。ペイロード最小化を常に優先。

## 1. リポジトリ構成

```
/cmd/timemon/main.go
/internal/web/        HTTPハンドラ・SSEハブ・認証ミドルウェア・テンプレート
/internal/timing/     UDPトリガー受信・重複排除・ペアリング・手計測打刻 (webを知らない)
/internal/domain/     換算cc・クラス判定・タイブレーク・順位付け・final_ms (純関数のみ、I/Oなし)
/internal/store/      SQLiteアクセス (唯一のwriter)
/web/static/          cropper.min.js 等 (go:embed)
/web/templates/       各画面HTML (go:embed)
/firmware/            ESP32 (PlatformIO)
/docs/timemon-mockup.html
/defaults.json        (embed用。実行Dirに同名ファイルがあれば優先)
/.gitlab-ci.yml
```

依存方向: web → timing/domain/store、timing → domain/store。domainは何にも依存しない。

## 2. データベース

起動時に無ければ自動作成 (`-db path.sqlite3` フラグ、default `./event.sqlite3`)。
接続毎PRAGMA: `journal_mode=WAL; busy_timeout=5000; foreign_keys=ON`。書き込みは store 内の単一コネクション(またはmutex)に直列化。

```sql
-- 単一行。イベント作成 = この行のINSERT (defaults.jsonの値をシード)
CREATE TABLE settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  event_name TEXT NOT NULL,
  timing_mode TEXT NOT NULL DEFAULT 'sensor',      -- 'sensor' | 'manual' (イベント途中の切替可)
  pt_mode TEXT NOT NULL DEFAULT 'add',             -- 'add' | 'invalidate'
  pt_penalty_ms INTEGER NOT NULL DEFAULT 5000,
  heat_ranking INTEGER NOT NULL DEFAULT 0,         -- bool。※ヒート「数の上限」機能は意図的に非搭載 (自由走行主眼。運用で制御)
  registration_mode TEXT NOT NULL DEFAULT 'public',-- 'public' | 'staff'
  registration_open INTEGER NOT NULL DEFAULT 1,    -- bool (受付終了で0)
  queue_self_entry INTEGER NOT NULL DEFAULT 1,     -- bool
  max_course_time_sec INTEGER NOT NULL DEFAULT 180,
  sensor_lockout_ms INTEGER NOT NULL DEFAULT 800,
  coefficients TEXT NOT NULL,                      -- JSON: {"turbo_gasoline":1.7,"turbo_diesel":1.5,"rotary":1.7,"supercharger":1.7}
  displacement_classes TEXT NOT NULL               -- JSON: [{"label":"~660cc","max_cc":660},{"label":"~1600cc","max_cc":1600},{"label":"無制限","max_cc":null}]
);

-- クラスラベル (driver軸・drivetrain軸のみ。排気量クラスはsettingsから導出)
CREATE TABLE class_defs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  axis TEXT NOT NULL,                              -- 'driver' | 'drivetrain'
  label TEXT NOT NULL,
  sort_order INTEGER NOT NULL
);

-- ドライバー = ユーザー (認証主体)
CREATE TABLE drivers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  driver_class_id INTEGER NOT NULL REFERENCES class_defs(id),
  icon BLOB,                                       -- 128x128 JPEG。NULL可
  token TEXT NOT NULL UNIQUE,                      -- ログイントークン平文 (base64url)。システム発行の使い捨てランダム値であり
                                                   -- ユーザー由来の秘密ではないため平文保存 (セッションID保存と同等の扱い)
  role TEXT NOT NULL DEFAULT 'user',               -- 'user' | 'admin'
  main_vehicle_id INTEGER REFERENCES vehicles(id),
  is_deleted INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE vehicles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  number INTEGER NOT NULL,                         -- 号車番号 (表示「＃3 アルトワークス」)。重複はDB制約で禁止しない — 登録・編集UIで警告表示のみ (運用ミスだが登録を詰まらせない)
  name TEXT NOT NULL,
  engine_type TEXT NOT NULL,                       -- 'gasoline' | 'diesel' | 'rotary' | 'ev'
  displacement_cc INTEGER,                         -- EVはNULL
  forced_induction INTEGER NOT NULL DEFAULT 0,     -- bool (EVは常に0)
  drivetrain_class_id INTEGER NOT NULL REFERENCES class_defs(id),
  is_deleted INTEGER NOT NULL DEFAULT 0
);

-- 紐づけ (N:M、相乗り=マルチエントリー)
CREATE TABLE entries (
  driver_id INTEGER NOT NULL REFERENCES drivers(id),
  vehicle_id INTEGER NOT NULL REFERENCES vehicles(id),
  PRIMARY KEY (driver_id, vehicle_id)
);

-- 出走キュー + コース状態 (状態機械の実体)
CREATE TABLE queue (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  driver_id INTEGER NOT NULL REFERENCES drivers(id),
  vehicle_id INTEGER NOT NULL REFERENCES vehicles(id),
  position REAL,                                   -- waiting内の並び。挿入は間の実数。隣接差が1e-9未満になったらwaiting全体を1.0刻みでリナンバー (稀)
  status TEXT NOT NULL DEFAULT 'waiting',          -- 'waiting' | 'on_course' | 'done' | 'canceled'
  t_start_us INTEGER,                              -- スタート打刻(μs)。on_courseでNULL=READY(センサー待ち)
  pt_count INTEGER NOT NULL DEFAULT 0,             -- 走行中付与分。ログ生成時に引き継ぐ
  mc_flag INTEGER NOT NULL DEFAULT 0,              -- ミスコース予約
  created_by INTEGER REFERENCES drivers(id)        -- 自己投入の監査用
);

CREATE TABLE logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  driver_id INTEGER REFERENCES drivers(id),        -- NULL = 未割当ログ
  vehicle_id INTEGER REFERENCES vehicles(id),
  raw_ms INTEGER NOT NULL,                         -- 計測タイム(ms、切り捨て)
  pt_count INTEGER NOT NULL DEFAULT 0,
  is_mc INTEGER NOT NULL DEFAULT 0,
  timestamp_ms INTEGER NOT NULL,                   -- 記録時刻(UNIX ms)。並び順の正はこれ (log_idは同刻タイブレークのみ)
  source TEXT NOT NULL,                            -- 'sensor' | 'manual'
  edited_at INTEGER,                               -- 運営編集時刻(ms)。NULL=未編集
  is_deleted INTEGER NOT NULL DEFAULT 0
);

-- 生トリガー全件保存 (ペアリングと独立のセーフティネット)
CREATE TABLE sensor_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  sensor_id TEXT NOT NULL,                         -- 'start' | 'goal'
  boot_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  timestamp_us INTEGER NOT NULL,                   -- ESP32打刻 (chrony時刻系)
  received_at INTEGER NOT NULL,
  UNIQUE (sensor_id, boot_id, seq)                 -- 3連送の重複排除
);

-- 運営操作の監査ログ
CREATE TABLE audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at_ms INTEGER NOT NULL,
  driver_id INTEGER,                               -- 操作者
  action TEXT NOT NULL,                            -- 'log.edit' 'queue.launch' 'user.reissue' 等
  detail TEXT                                      -- JSON
);
```

バックアップはアプリ外レイヤー (Nextcloudクライアント等) が担当。アプリは `POST /api/admin/backup` 相当は持たないが、定期 `VACUUM INTO './snapshots/{ts}.sqlite3'` を1時間毎に実行するgoroutineだけ持つ。

## 3. defaults.json

`go:embed` で焼き込み、実行ディレクトリに `defaults.json` があればそちらを優先読込。
**イベント作成フォームの初期値にすぎない**。作成時にsettings/class_defsへシードされ、以後の真実はDB側。ファイルを後から変えても既存イベントに波及しない (競技の公平性のため)。

```json
{
  "event": {
    "timing_mode": "sensor",
    "pt_mode": "add",
    "pt_penalty_ms": 5000,
    "heat_ranking": false,
    "registration_mode": "public",
    "queue_self_entry": true,
    "max_course_time_sec": 180,
    "sensor_lockout_ms": 800
  },
  "coefficients": {
    "turbo_gasoline": 1.7,
    "turbo_diesel": 1.5,
    "rotary": 1.7,
    "supercharger": 1.7
  },
  "displacement_classes": [
    { "label": "~660cc",  "max_cc": 660 },
    { "label": "~1600cc", "max_cc": 1600 },
    { "label": "無制限",  "max_cc": null }
  ],
  "classes": {
    "driver": ["現役", "学内OB", "社会人"],
    "drivetrain": ["2WD", "4WD"]
  }
}
```

注: ロータリー係数はJAF公認規則を大会前に要再確認 (税制の×1.5と混同しやすい)。値の差替はこのファイルのみで完結すること。

## 4. ドメインロジック (internal/domain、全部純関数 + テーブル駆動テスト必須)

### 4.1 換算排気量とクラス判定

```
convertedCC(cc, engineType, forcedInduction, coef):
  ev       → nil (EVは換算なし)
  rotary   → cc × coef.rotary × (fi ? coef.turbo_gasoline : 1)
  gasoline → cc × (fi ? coef.turbo_gasoline : 1)
  diesel   → cc × (fi ? coef.turbo_diesel : 1)
  端数は切り捨てで整数化
```

- 排気量クラス = convertedCC を displacement_classes に昇順で当て、`converted <= max_cc` の最初のクラス (max_cc=null は無制限受け皿)。
- **EVは車両が存在するときだけ「EV」クラスが出現する** (クラス一覧は登録車両からの導出値として動的生成。フィルタUI候補も同じ導出結果から作る)。
- 保存しない。常に導出。諸元編集で順位・CSVすべて自動追従。

### 4.2 タイムとペナルティ

```
finalMS(raw, ptCount, isMC, settings):
  invalid = isMC || (pt_mode == 'invalidate' && ptCount > 0)
  final   = pt_mode == 'add' ? raw + ptCount × pt_penalty_ms : raw
  → (final, invalid)
```

- 表示は 1/1000 固定 (`1:23.456`)。丸めは受信時切り捨てのみで表示丸めなし。
- 走行中のリアルタイム表示のみ 1/10 秒。

### 4.3 順位付けとタイブレーク

集計単位 = **(driver_id, vehicle_id) の組み合わせ** (logsのdistinct組み合わせが基準)。クラス値は drivers/vehicles 行から直接参照する — entries は紐づけ管理 (相乗り・メイン車両・キュー補完) 専用でランキング計算には登場しない。
各組み合わせについて有効走行の final_ms を昇順ソートしたリストを持つ。比較:

1. ベスト (`sorted[0]`) が小さい方が上位。
2. 同値 → セカンド (`sorted[1]`)。**セカンドが無い者はセカンド持ちより下位**。それも同値なら3へ。
3. 同値 → 換算排気量の小さい順。**EV(換算なし)は+∞扱い** (内燃機関に負ける)。
4. 同値 → 先出し (ベストタイムを記録した走行の timestamp_ms が早い方)。

- 有効タイムを1本も持たない組み合わせは末尾グループ (順位なし、グレーアウト表示)。
- **順位計算はサーバー側のこの1箇所のみ**。クライアントとCSVはソート済み行の「絞り込み+連番振り直し」だけを行う (フィルタ内独自順位。ソート順は不変なので順位定義の複製が発生しない)。

### 4.4 ヒート番号 (導出値、保存しない)

組み合わせ毎に timestamp_ms 順の連番 (`ROW_NUMBER() OVER (PARTITION BY driver_id, vehicle_id ORDER BY timestamp_ms, id)`)。MC走行も番号を消費 (欠番なし)。削除・編集・事後入力で常に自動振り直し。
heat_ranking 有効時は「各組み合わせのヒートn同士」で 4.3 と同じ比較。UIはヒート選択フィルタが出現。

### 2.1 初回ブートストラップ (セットアップ)

新規DB (settings行なし) で起動した場合:
1. `crypto/rand` でセットアップトークンを生成し、**stdoutに `Setup URL: https://<host>/setup?t=<token>` を出力** (systemdログ経由で運営が確認)。
2. `GET /setup?t=…` (トークン一致時のみ200、それ以外404): イベント名入力 + defaults.json 由来の初期値確認・編集フォーム + 最初の運営者のドライバー登録 (名前・区分)。
3. 送信で settings/class_defs をシード、登録者を `role='admin'` で作成、通常のログインクッキー発行 → `/admin` へ。セットアップトークンは即失効。
4. settings行が存在する間、`/setup` は常に404。

## 5. 認証

- **パスワードは一切預からない。登録時発行のランダムトークン方式。**
- 発行: `crypto/rand` 24バイト → base64url。DBに平文保存 (システム発行の使い捨てランダム値であり、ユーザーの使い回しパスワードを預かるリスクとは別物。QR/URL再表示機能の実装に平文が必要)。
- `GET /a/{token}`: 定数時間比較で照合 → 一致で httpOnly + SameSite=Lax クッキー発行 (**Secure属性は接続がTLSのときのみ付与** — `r.TLS != nil` または `X-Forwarded-Proto: https`。LAN直=http でスタート係端末がログインできる必要があるため) → `/my` へ302。不一致は**一律404**。
- 登録 (`POST /api/register`) 成功時は即クッキー発行 → クライアントは `/my` へ遷移 (QR画面は挟まない)。ログインURL/QRはマイページからいつでも再表示。
- role: 'user' | 'admin'。**運営API = requireAuth + requireRole("admin") の合成ミドルウェア**。roleはDB属性なので剥奪は即時有効 (再発行不要)。最後のadminの自己剥奪は409。
- 再発行 (運営専権): 新トークン生成・旧hash上書き → 旧URL即失効。
- 経路防御: Cloudflare Access を `/admin` と `/api/admin/*` に設定 (インフラ側作業、アプリ外)。アプリ側検査はLAN直アクセスにも効く本命。
- CSRF: 変更系全APIで `Sec-Fetch-Site` ヘッダ検査。**ヘッダが存在する場合のみ** `cross-site`/`same-site` を403で拒否 (`same-origin`/`none` は許可)。ヘッダ欠如 (curl等の非ブラウザクライアント) は許容 — CSRFはブラウザ経由攻撃なのでこれで防御目的は満たし、運用スクリプトを殺さない。
- レート制限: `POST /api/register`、`GET /a/{token}`、`/api/my/*` に CF-Connecting-IP (無ければRemoteAddr) 基準のトークンバケット。
- アイコン: 受信Base64を `image/jpeg` デコード検証 → 128x128に再エンコードしてBLOB保存。配信は `GET /api/drivers/{id}/icon` でJPEGバイナリ + 長期Cache-Control + ETag。

## 6. SSE (リアルタイム配信)

`GET /api/stream?topics=ranking,on_course,queue,sensor_status,orphan,settings,time`

- **スナップショット方式**: 差分を積ませない。変更があったら該当トピックの完全な現在値を再送、クライアントは丸ごと差し替え。切断復帰時の取りこぼしが構造的に発生しない。
- サーバーはトピック毎の最新スナップショットをメモリ保持し、**新規接続時に購読トピックの現在値を即時送出** (初期化と再接続が同一パス)。
- **gzip必須** (`Content-Encoding: gzip`)。イベント毎のフラッシュは**2段**: `gzip.Writer.Flush()` (圧縮バッファ掃き出し) → `http.Flusher.Flush()` (ソケット送出)。片方だけだとイベントが届かない。CFの圧縮はエッジ→閲覧者区間のみで、mineo上りはGoが圧縮しない限り生で通る。バースト対策 (50接続×5KBが250KB→60KB程度になる)。
- 書き込みブロックするクライアントは送信バッファ溢れで即切断 (1台の不調が全体を止めない)。
- keep-alive: `time` イベントが兼務。

トピック定義:

| event | data | 発火 |
|---|---|---|
| `ranking` | 順位付け済み行の配列 (§4.3出力: driver{id,name}, vehicle{id,number,name,converted_cc,classes}, driver_class, best_ms, second_ms, best_log_id, runs, valid_runs, invalid) | ログ増減・編集・設定変更時 |
| `on_course` | on_course行の配列 {queue_id, driver, vehicle, t_start_us(null=READY), pt_count, mc_flag, finish(確定演出用: fin_ms, until_ms)} | 出走/打刻/PT/MC/確定/取消 |
| `queue` | waiting行の配列 (position順) | 追加/削除/並替/消費 |
| `sensor_status` | [{sensor_id, last_seen_ms, loss_rate, ntp_offset_ms}] | ハートビート受信毎(間引き5s) |
| `orphan` | 未割当ログ・孤児トリガーの警告配列 | 発生/解消時 (admin購読) |
| `settings` | 公開設定サブセット | 運営が設定変更時 |
| `time` | {"server_ms": …} | **25〜30秒毎 (keep-alive兼務)** |

クライアント時刻同期: `time` 受信毎に `offset = server_ms - Date.now()` を更新 (余裕があれば直近5サンプルの最小遅延側採用)。走行中タイマーは `(Date.now()+offset) - t_start` を rAF 描画、表示1/10秒。`visibilitychange` 復帰時に再描画。

購読分け: monitor=`ranking,on_course,queue,time` / ranking画面=`ranking,time` / my=`queue,on_course,time` / admin=全部。

明細 (ドリルダウン・ログ一覧) はSSEに載せず **オンデマンドREST**。`ranking` 受信 (対象組み合わせのruns増) を再取得トリガーに使う。

## 7. 計測 (internal/timing)

### 7.1 センサーモード

- ESP32 (start/goal 2台) が**発火瞬間を自機で打刻**し、UDPで `{sensor_id, boot_id, seq, timestamp_us}` を **同一パケット3連送 (50ms間隔)**。RPi側は `(sensor_id,boot_id,seq)` で重複排除、**全件 sensor_events に保存**してからペアリング。
- 時刻系: RPi上のchronyにESP32がSNTP同期 (±1ms級)。ゆえに無線遅延・再送・順序入替は計測誤差にならない。
- ペアリング (FIFO):
  - startトリガー → on_course のうち t_start_us 未設定の最古に打刻。該当なし=孤児スタート警告 (orphanトピック)。
  - goalトリガー → t_start_us 設定済みの最古に紐付け `raw_ms = (t_goal-t_start)/1000` 切り捨て → ログ生成(source=sensor) → done。該当なし=孤児ゴール警告。
  - on_course は複数保持可 (2台運行対応。追い抜きなし前提のFIFO)。
- ハートビート: 5秒毎 {seq, ntp_offset}。RPiは last_seen / seq欠番からloss率 / offset を sensor_status で配信。
- `max_course_time_sec` 超過は表示上の赤警告のみ (自動canceledにしない)。
- 出走ボタン=紐付け宣言 (タイミング非依存)。UIは長押し0.5秒。**セルフ出走可** (waiting先頭本人、§8)。

### 7.2 手計測モード

- 出走ボタンタップ=スタート打刻、大型ゴールボタン(走行中先頭FIFO) or カード個別[完走]=ゴール打刻。source=manual。
- **打刻はクライアント補正時刻**: クライアントは `Date.now()+offset` (SSE time由来) を `client_ms` としてbodyで送る。サーバーは妥当性検査 (受信時刻との差が±2秒超なら受信時刻へフォールバック) のみ。上りジッタが計測から消える。**初回 `time` 受信前で offset 未確定の場合、クライアントは client_ms を省略し、サーバーは受信時刻を採用する。**
- 出走ボタンは即時タップ (打刻そのものなので長押き禁止)。セルフ出走は不可 (運営専権)。

### 7.3 切り戻し (両モード共通)

- スタート取消: on_course → waiting先頭へ復帰。打刻・PT・MC破棄。READY中でも走行中でも可。
- ゴール取消: FINISH演出 (確定猶予3秒) の実装は**「ログ即書き込み + undo-goalでそのログを削除して on_course 復帰」方式**とする (書き込みを3秒遅延させる方式は禁止 — クラッシュ時にゴール打刻が失われるため)。猶予中: undo-goal はログ削除 + queue行を on_course/t_start維持のまま復帰。猶予経過後 (queue行がdone確定後) は409を返し、ログ管理タブでの削除+手動再構成で救済。
- 走行中止: on_course → canceled、ログ生成なし。
- PT±: on_course中に増減可 (0未満ガード)、ログ生成時に引き継ぎ。ミスコース予約トグルも同様。

## 8. API一覧

認証境界: 公開 / U=ユーザー(クッキー) / A=運営(U + role=admin) / 内部(会場LANのみ)。
変更系は全て Sec-Fetch-Site 検査。エラー: 未認証401 / 権限403 / 状態矛盾409 / 秘匿404。

### ページ (公開)
```
GET /            monitor
GET /ranking  /register  /my  /admin      (my,adminは未認証時ログイン案内)
GET /a/{token}   トークンログイン → Set-Cookie → 302 /my  (不一致404)
GET /static/*    ETag + Cache-Control長期 (CFエッジキャッシュ対象)
```

### 公開API
```
GET  /api/stream?topics=…                SSE
GET  /api/ranking                        集計スナップショット (SSEと同一生成器)
GET  /api/queue                          キュー+on_course現在値
GET  /api/drivers                        一覧 (icon非含有)
GET  /api/vehicles                       一覧 (number,name,諸元,換算cc,クラス)
GET  /api/drivers/{id}/icon              JPEG (ETag)
GET  /api/combinations/{d}/{v}/logs      明細 (heat_no,raw,pt,final,invalid,順位)
GET  /api/settings                       公開サブセット
POST /api/register                       登録 (driver+車両新規or相乗り) → Set-Cookie。mode/openで403。RL
```

### ユーザーAPI (U)
```
GET    /api/my                           プロフィール+車両+履歴+キュー状態
PUT    /api/my/profile                   名前・区分
POST   /api/my/icon                      アイコン差替 (JPEG検証・再エンコード)
GET    /api/my/qr                        自分のログインQR PNG
POST   /api/my/vehicles                  車両追加。bodyで2形態:
                                         {vehicle_id}                       → 既存車両へ相乗り (即成立)
                                         {number,name,engine_type,          → 新規車両を作成し自分に自動紐づけ
                                          displacement_cc,forced_induction,   (登録フローの車両作成と同一内部関数。
                                          drivetrain_class_id}                号車番号重複は警告のみ、レート制限対象)
                                         ※諸元の事後編集は運営専権のまま (車両は相乗りで共有され得るリソースであり、
                                           諸元変更は他者の順位・クラスにも波及するため)
DELETE /api/my/vehicles/{id}             紐づけ解除 (メイン車両は409→先にメイン変更。最後の1台は必然的にメインなので
                                         常に1台以上の紐づけが保たれる)。entriesの削除のみで logs には触れない —
                                         過去の走行記録・順位・CSVは解除後も不変 (集計はlogs基準のため)
PUT    /api/my/main-vehicle              メイン車両変更 {vehicle_id}
POST   /api/my/queue                     自己投入 (末尾。省略時メイン車両)。self_entry=offで403、既にwaitingなら409
DELETE /api/my/queue                     自分のwaiting取り下げ
POST   /api/my/queue/launch              セルフ出走 (自分が先頭 AND sensorモード。他は409)
DELETE /api/my/queue/launch              出走取消 (自分のon_courseがREADYのみ。打刻済み409)
```

### 運営API (A) ※UI表示は「運営」、内部識別子はadmin
```
GET/POST /api/admin/users                一覧 / 運営登録
PUT      /api/admin/users/{id}           リネーム・区分
POST     /api/admin/users/{id}/reissue   トークン再発行 (旧即失効) → 新URL/QR返却
PUT      /api/admin/users/{id}/role      {role}。最後のadmin剥奪は409
POST/PUT/DELETE /api/admin/vehicles(/{id})
POST   /api/admin/queue                  追加 {driver_id,vehicle_id} (waiting重複409)
PUT    /api/admin/queue/{id}             並替 {position}
DELETE /api/admin/queue/{id}             出走中止 (canceled)
POST   /api/admin/course                 出走 (先頭→on_course。manualモード時は打刻含む {client_ms})
POST   /api/admin/course/finish          大型ゴール (走行中先頭FIFO確定) {client_ms}
POST   /api/admin/course/{id}/finish     個別完走 {client_ms} (2台時の順序指定)
DELETE /api/admin/course/{id}            走行中止
POST   /api/admin/course/{id}/undo-start スタート取消 → waiting先頭
POST   /api/admin/course/{id}/undo-goal  ゴール取消 → RUNNING復帰 (猶予中のみ、他409)
PUT    /api/admin/course/{id}/pt         {delta:±1} (0未満ガード)
PUT    /api/admin/course/{id}/mc         トグル
GET    /api/admin/logs?page=…            全ログ (未割当含む)
POST   /api/admin/logs                   手動追加 (timestamp_ms指定可, source=manual)
PUT    /api/admin/logs/{id}              編集 (走者/車両/pt/mc → edited_at更新)
DELETE /api/admin/logs/{id}              論理削除
PUT    /api/admin/logs/{id}/assign       未割当→走者割当
GET/PUT /api/admin/settings              設定 (変更でsettings SSE発火)
PUT    /api/admin/registration           {open:bool}
GET    /api/admin/export?type=ranking|combination|logs&(フィルタ…)   CSV
GET    /api/admin/sensors                センサー状態初期値
```

すべての運営変更操作は audit テーブルに `driver_id` 付きで記録。

### 内部 (LANのみ、Tunnel非公開)
```
UDP :9999                                ESP32トリガー / ハートビート
GET /api/internal/sensor-config          ESP32起動時設定 (lockout_ms等)。送信元IP制限
UDP :123                                 chrony (アプリ外、RPiにインストール)
```

## 9. CSV仕様 (UTF-8 BOM付き)

フィルタパラメータはランキングAPIと完全共用 (`class_driver=現役&drivetrain=2WD&disp=~1600cc&driver_id=&vehicle_id=&heat=`)。

- `ranking`: 順位, ドライバー, 区分, 号車, 車両, 換算cc, 排気量クラス, 駆動, ベスト, セカンド, 走行本数, PT計, 状態
- `combination` (要driver_id+vehicle_id): ヒートNo順に1走行1行 — heat, raw, pt, final, 状態, **フィルタ内順位** (その1本のfinalを現フィルタの全有効走行と比較した順位)
- `logs`: 全カラム生データ (検証・バックアップ用)

## 10. 画面仕様

**モック `docs/timemon-mockup.html` が正**。要点のみ:

- 5画面: register(初回のみ・タブ外) / monitor / ranking / my / admin(運営roleのみタブ表示)。下部タブ、900px以上でPC版 (ヘッダー最上段、ナビは左サイドバー縦並び[選択中は左ボーダー+背景ハイライト]、コンテンツ2カラムgrid、走行中タイマー拡大。ナビ非表示時=未ログインはサイドバー列が自動で潰れて全幅)。
- monitor: 走行中カード(0〜2枚スタック、READY/RUNNING 1/10秒/FINISH 1/1000を2.5〜3秒フラッシュ、ミスコース帯、PT表示) + NEXT(先頭3) + 直近リザルト10件(アイコン・H番号・手計測✍️印・MC打消線)。**カードタップで全画面ピットボード**: スマホ縦持ちのみCSS90°回転で横全画面 (横長画面=PC・横持ちは非回転、⇅ボタンも非表示)、フォントは最大桁ダミー`8:88.888`実測で幅96%固定(桁増で縮まない)、縦持ち時のみ⇅180°反転ボタン、確定後3秒で自動クローズ。
- ranking: フィルタ = 軸名付きチップ3行(ドライバー/排気量/駆動方式) + 走者指定/車両指定プルダウン。絞り込みはクライアント側(連番振り直しのみ)。行タップでドリルダウンモーダル(REST)。無効は末尾グレー。フィルタ状態はURLクエリ同期。
- my: プロフィール(編集/ログインQR) / 出走キューカード(状態機械: 未投入→並ぶ / n番目→取り下げ / 先頭×sensor→出走▶+出走中止 / READY→出走取消 / 走行中→応援表示) / 紐づけ車両(カード全体タップでメイン切替・枠ハイライト、[＋車両を追加]→モーダルで「新規登録/既存に相乗り」切替、新規は登録フォームと同じ諸元入力+換算ccプレビュー、各カードに✕=紐づけ解除[確認ダイアログ付き]。メイン車両のカードには✕自体を表示しない — 解除不可を操作不能で表現。API側の409はUI迂回への防衛として維持) / 走行履歴。
- admin 5タブ: 出走(計測モード切替seg、出走ボタン[sensor長押し/manual即時]、manualのみ大型ゴールボタン、on_courseカード[完走|PT+|PT−|スタート取消|ミスコース|走行中止|ゴール取消]、キュー[ドラッグ並替・出走中止]、追加フォーム[メイン車両自動補完]) / ログ(未割当黄帯、PT±/MC/編集/削除、手動追加) / ユーザ(1行: アバター,名前,✏️,再発行→確認モーダル[新URL1タップコピー+QR表示],運営ボタン[ハイライト=有効]。※運営による現行QR再掲は廃止 — 紛失対応は常に再発行。本人の再表示はマイページの「ログインQR」で自足) / 車両(諸元・換算cc・クラス・紐づけ一覧) / 設定(センサーパネル、イベント設定、CSV、受付終了)。
- 登録フォーム: 区分seg / 車両=新規(号車番号+車名+エンジン方式seg[EVでcc・過給機が畳まれる]+実cc+過給機seg+**換算cc/クラスのリアルタイム表示**+駆動seg) or 既存選択 / アイコン(Cropper.js 1:1→128px JPEG)。

## 11. ESP32ファームウェア (/firmware、PlatformIO + Arduinoフレームワーク)

- 基板: ESP32 DevKit (goal側はWROOM-32UE+外部アンテナ)。start/goal同一ファーム、`config.h` で `SENSOR_ID`/SSID/RPi IPを焼き分け (config.h.exampleをコミット、実物gitignore)。
- 起動: WiFi接続 → SNTP同期(chrony@RPi) → `/api/internal/sensor-config` でlockout取得 → 就緒。**同期完了までトリガー送信禁止**、状態LED(未同期=点滅/就緒=点灯)。
- 入力: 光電センサ NPNオープンコレクタを 10kΩプルアップ(3.3V)で受け (100Ω直列+3.6Vツェナー保険)。GPIO FALLING割り込み → **ISRは `esp_timer_get_time()` をリングバッファに積むだけ**。
- デバウンス: 最初のエッジのみ採用、以後 lockout_ms (default 800) 内は破棄。打刻は最初のエッジなので精度に影響なし。
- 送信: ループ側で `{sensor_id, boot_id(起動毎乱数), seq, timestamp_us}` をUDP 3連送(50ms間隔)。
- ハートビート: 5秒毎 (seq, ntp_offset付き)。SNTP再同期は起動時+1時間毎。
- CI: `pio run`、`firmware/**` 変更時のみ。

## 12. CI (.gitlab-ci.yml)

```yaml
stages: [verify, build, release]
variables: { GOFLAGS: "-mod=vendor", CGO_ENABLED: "0" }

verify:
  stage: verify
  image: golang:1.22
  script:
    - test -z "$(gofmt -l .)"
    - go vet ./...
    - go test -race -count=1 ./...

firmware:
  stage: verify
  image: python:3.12-slim
  script: [ "pip install platformio", "cd firmware && pio run" ]
  rules: [ { changes: ["firmware/**/*"] } ]
  artifacts: { paths: ["firmware/.pio/build/*/firmware.bin"] }

build:
  stage: build
  image: golang:1.22
  parallel: { matrix: [ { GOARCH: [arm64, amd64] } ] }
  script:
    - GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${CI_COMMIT_SHORT_SHA}" -o timemon-linux-${GOARCH} ./cmd/timemon
  artifacts: { paths: ["timemon-linux-*"], expire_in: 30 days }

release:
  stage: release
  rules: [ { if: "$CI_COMMIT_TAG" } ]
  script:
    - |
      for f in timemon-linux-*; do
        curl --fail --header "JOB-TOKEN: ${CI_JOB_TOKEN}" --upload-file "$f" \
          "${CI_API_V4_URL}/projects/${CI_PROJECT_ID}/packages/generic/timemon/${CI_COMMIT_TAG}/${f}"
      done
```

デプロイは手動 (RPiで `curl -O` → systemd unit再起動)。イベント当日の自動差替は禁止。

## 13. 実装フェーズ (推奨順)

1. **domain**: convertedCC / finalMS / タイブレーク比較器 / ランキング集計 / ヒート導出。テーブル駆動テスト (ロータリーターボ、閾値660ちょうど、EV、セカンド無し、同タイム3段、pt_mode両方×MC)。
2. **store + settings/defaults.json**: スキーマ作成、シード、単一writer。
3. **auth + setup + register/my**: トークン発行〜クッキー〜role、ブートストラップ(§2.1)、登録フロー。
4. **SSEハブ + ranking/queue配信**: スナップショット保持・gzip・time。
5. **queue/course状態機械 + 手計測モード**: この時点でセンサー無しの完全な計時システムとして動く (最初の実地テスト可能点)。
6. **timing (UDP受信・ペアリング) + sensor_events + orphan**。
7. **admin残り (ログ編集・ユーザ・車両・CSV・監査)**。
8. **ファームウェア + 実機結合**。

各画面HTMLはモックからスタイル・構造を移植してよい (モックのJSはデモ用スタブなので、SSE購読・fetch呼び出しに置換)。

## 14. 受け入れ基準 (抜粋)

- 50クライアント同時SSE接続で、1走行確定→全クライアントのランキング反映が数秒以内 (gzip有効時バースト60KB程度)。
- 回線切断→復帰でランキング・キュー表示が自動で完全復元される (手動リロード不要)。
- センサー全滅時、手計測モードだけで大会運営が完遂できる。
- 諸元・設定 (係数、PT秒数、PTモード) を途中変更しても、全順位・CSVが再計算で即座に整合する。
- ESP32再起動 (バッテリー交換) 後、操作なしで自動復帰 (SNTP再同期→LED点灯)。
- 未割当ログ・孤児トリガーが1件も黙って消えない (必ずorphan警告に出る)。
- 空DBから: 起動ログのSetup URL → イベント作成 → 最初の運営誕生 → 参加者登録開始、まで一本道で通る。
- LAN直 (http) アクセスでスタート係端末がログイン・出走操作できる (Secureクッキー問題が再発しないこと)。

## 付録: 運用環境メモ (実装には直接関係しないが背景として)

- RPi5 + ドコモHR02 + mineo 1.5Mbps + Cloudflare Tunnel。CF側: /static とアイコンにCache Rule、/admin系にAccess。
- センサー電源: 12V鉛バッテリー(80D23L) → ヒューズ2A → センサー直結 + DC-DC(入力下限9V以下品)→5V→ESP32。電装のみIP65小箱、バッテリー外置き。状態LEDを箱外に。
- RPi5は5V/5A級の専用電源必須 (ブラウンアウト→SD破損防止)。chrony・systemd unit・Nextcloudクライアント(snapshots/同期)はプロビジョニング手順書側で管理。
