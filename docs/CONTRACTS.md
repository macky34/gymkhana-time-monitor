# 実装契約 (CONTRACTS) — 並行実装のための凍結仕様 v1

正: `plan/DESIGN.md` (以下「設計書」)。本書は並行作業のために**先に凍結する**インターフェース・JSON形状・ファイル所有権のみを定める。矛盾時は設計書が勝つが、変更する場合は必ずPM承認を経て本書を更新すること。

- モジュールパス: `timemon`
- Go 1.22+ / `net/http` ServeMux (メソッド+パスパターン) / 外部依存は `modernc.org/sqlite` と `github.com/skip2/go-qrcode` のみ
- フロントは Vanilla JS/CSS。各画面HTMLに `<style>`/`<script>` 直書き (共有JSファイルは作らない。例外: `/web/static/cropper.min.js` セルフホスト)
- ユーザー入力のDOM反映は `textContent` 系のみ (innerHTML禁止)
- Windows開発機のGoは `C:\Program Files\Go\bin\go.exe` (PATH未反映の場合はフルパスで呼ぶ)

## ファイル所有権 (ウェーブ1)

| 担当 | 書いてよい場所 | 禁止 |
|---|---|---|
| Agent A (domain) | `internal/domain/**` | go.mod変更 (依存ゼロ厳守) |
| Agent B (store) | `internal/store/**`, `go.mod`/`go.sum` への依存追加 (sqliteのみ) | domain以外のimport追加 |
| Agent C (templates) | `web/templates/**`, `web/static/**` | Goコード全般 |

`cmd/`, `internal/web/`, `internal/timing/` はウェーブ2以降。ウェーブ1では誰も触らない。

## 1. internal/domain (純関数のみ、I/Oなし、依存ゼロ)

```go
package domain

type EngineType string // "gasoline" | "diesel" | "rotary" | "ev"

type Coefficients struct { // settings.coefficients のJSONと同形
    TurboGasoline float64 `json:"turbo_gasoline"`
    TurboDiesel   float64 `json:"turbo_diesel"`
    Rotary        float64 `json:"rotary"`
    Supercharger  float64 `json:"supercharger"`
}

type DispClass struct { // settings.displacement_classes のJSONと同形
    Label string `json:"label"`
    MaxCC *int   `json:"max_cc"` // null = 無制限受け皿
}

// EVは (0,false)。それ以外は換算cc(切り捨て整数)と true。
// rotary: cc×Rotary×(fi ? TurboGasoline : 1) / gasoline: cc×(fi ? TurboGasoline : 1) / diesel: cc×(fi ? TurboDiesel : 1)
func ConvertedCC(cc int, engine EngineType, forcedInduction bool, c Coefficients) (int, bool)

// ok=false (EV) → "EV"。それ以外は昇順で converted<=MaxCC の最初のLabel (MaxCC=nil は常にマッチ)。
func DispClassOf(convertedCC int, ok bool, classes []DispClass) string

// ptMode: "add" | "invalidate"
func FinalMS(rawMS, ptCount int, isMC bool, ptMode string, ptPenaltyMS int) (finalMS int, invalid bool)

type ComboKey struct{ DriverID, VehicleID int64 }

type RunRow struct { // logs 1行 (is_deleted=0のみ渡される)
    LogID       int64
    Combo       ComboKey
    RawMS       int
    PTCount     int
    IsMC        bool
    TimestampMS int64
}

// 組み合わせ毎の timestamp_ms,LogID 順連番 (MCも消費、欠番なし)。key=LogID, val=ヒートNo(1始まり)
func HeatNumbers(runs []RunRow) map[int64]int

type ComboMeta struct { // タイブレーク用メタ (呼び手=web層がstoreから構築)
    ConvertedCC int  // EVは無視される
    IsEV        bool // true → 換算∞扱い
}

type Standing struct {
    Combo       ComboKey
    BestMS      *int   // 有効走行なし=nil
    SecondMS    *int
    BestLogID   *int64
    BestAtMS    int64 // ベスト走行のtimestamp_ms (先出しタイブレーク用)
    Runs        int   // 全走行本数
    ValidRuns   int
    PTTotal     int
    Invalid     bool  // 有効走行ゼロ → 末尾グレー群
}

// 設計書§4.3の全順序でソート済みStandingを返す (順位計算はここ1箇所)。
// heat>0 なら「各комбоのヒートheatの1本」だけで同比較 (§4.4)。heat=0で通常。
func Rank(runs []RunRow, meta map[ComboKey]ComboMeta, ptMode string, ptPenaltyMS int, heat int) []Standing
```

テスト必須ケース: ロータリーターボ / 換算660ちょうど / EV / セカンド無しは下位 / 同タイム3段タイブレーク / pt_mode両方×MC / heat指定。

## 2. internal/store (唯一のwriter、書き込みは単一コネクション+mutexで直列化)

スキーマは設計書§2のSQLを**そのまま**使う (`schema.go` に定数で埋め込み)。`Open` 時に無ければ作成。接続毎PRAGMA: `journal_mode=WAL; busy_timeout=5000; foreign_keys=ON`。

```go
package store // import "timemon/internal/domain" のみ可

func Open(path string) (*Store, error)
func (s *Store) Close() error

// --- settings / bootstrap ---
type SettingsRow struct { // settingsテーブル1行と同形。Coefficients/DispClassesはdomain型にパース済み
    EventName string; TimingMode string; PTMode string; PTPenaltyMS int
    HeatRanking bool; RegistrationMode string; RegistrationOpen bool
    QueueSelfEntry bool; MaxCourseTimeSec int; SensorLockoutMS int
    Coef domain.Coefficients; DispClasses []domain.DispClass
}
func (s *Store) GetSettings() (SettingsRow, bool, error) // ok=false → 未セットアップ
func (s *Store) SeedEvent(set SettingsRow, driverClasses, dtClasses []string) error // class_defsも作成
func (s *Store) UpdateSettings(set SettingsRow) error

type ClassDef struct{ ID int64; Axis, Label string; SortOrder int }
func (s *Store) ListClassDefs(axis string) ([]ClassDef, error) // axis="" で全部

// --- drivers ---
type Driver struct{ ID int64; Name string; DriverClassID int64; Token string; Role string; MainVehicleID *int64; HasIcon bool }
func (s *Store) CreateDriver(name string, classID int64, token, role string) (int64, error)
func (s *Store) GetDriverByToken(token string) (Driver, bool, error) // 呼び手が定数時間比較… ではなくtoken完全一致SELECT+存在秘匿404
func (s *Store) GetDriver(id int64) (Driver, bool, error)
func (s *Store) ListDrivers() ([]Driver, error) // is_deleted=0
func (s *Store) UpdateDriver(id int64, name string, classID int64) error
func (s *Store) SetRole(id int64, role string) error
func (s *Store) CountAdmins() (int, error)
func (s *Store) ReissueToken(id int64, newToken string) error
func (s *Store) SetIcon(id int64, jpeg []byte) error
func (s *Store) GetIcon(id int64) ([]byte, bool, error)
func (s *Store) SetMainVehicle(driverID, vehicleID int64) error

// --- vehicles / entries ---
type Vehicle struct{ ID int64; Number int; Name string; Engine domain.EngineType; DisplacementCC *int; ForcedInduction bool; DrivetrainClassID int64 }
func (s *Store) CreateVehicle(v Vehicle) (int64, error)
func (s *Store) UpdateVehicle(v Vehicle) error
func (s *Store) DeleteVehicle(id int64) error // 論理削除
func (s *Store) GetVehicle(id int64) (Vehicle, bool, error)
func (s *Store) ListVehicles() ([]Vehicle, error)
func (s *Store) NumberInUse(number int, excludeID int64) (bool, error) // 重複警告用
func (s *Store) AddEntry(driverID, vehicleID int64) error
func (s *Store) DeleteEntry(driverID, vehicleID int64) error
func (s *Store) ListEntriesByDriver(driverID int64) ([]Vehicle, error)
func (s *Store) ListDriversByVehicle(vehicleID int64) ([]Driver, error)

// --- queue / course (状態プリミティブ。ビジネス判断はweb/timing側) ---
type QueueRow struct{ ID int64; DriverID, VehicleID int64; Position float64; Status string; TStartUS *int64; PTCount int; MCFlag bool; CreatedBy *int64 }
func (s *Store) ListQueue(status string) ([]QueueRow, error) // position順(waiting) / id順(on_course)
func (s *Store) Enqueue(driverID, vehicleID int64, createdBy *int64) (int64, error) // 末尾。waiting重複はerr
func (s *Store) Reorder(id int64, position float64) error   // 必要時リナンバーは内部で
func (s *Store) SetQueueStatus(id int64, status string) error
func (s *Store) SetStart(id int64, tStartUS *int64) error   // nil で打刻解除
func (s *Store) SetPT(id int64, delta int) (int, error)     // 0未満ガード、新値返す
func (s *Store) SetMC(id int64, on bool) error
func (s *Store) GetQueueRow(id int64) (QueueRow, bool, error)

// --- logs ---
type LogRow struct{ ID int64; DriverID, VehicleID *int64; RawMS, PTCount int; IsMC bool; TimestampMS int64; Source string; EditedAt *int64; IsDeleted bool }
func (s *Store) InsertLog(l LogRow) (int64, error)
func (s *Store) UpdateLog(l LogRow) error // edited_at含め呼び手がセット
func (s *Store) SoftDeleteLog(id int64) error
func (s *Store) HardDeleteLog(id int64) error // undo-goal専用 (猶予中の物理削除)
func (s *Store) GetLog(id int64) (LogRow, bool, error)
func (s *Store) ListLogs(limit, offset int) ([]LogRow, int, error) // 全件(削除含む)+総数、timestamp_ms DESC
func (s *Store) ListRuns() ([]domain.RunRow, error) // is_deleted=0 AND driver/vehicle NOT NULL → ランキング入力
func (s *Store) ListRunsByCombo(d, v int64) ([]domain.RunRow, error)
func (s *Store) ListUnassignedLogs() ([]LogRow, error)

// --- sensor / audit / snapshot ---
func (s *Store) InsertSensorEvent(sensorID string, bootID, seq int64, tsUS int64, receivedAt int64) (bool, error) // false=重複
func (s *Store) AppendAudit(atMS int64, driverID *int64, action, detailJSON string) error
func (s *Store) VacuumInto(path string) error
```

## 3. HTTP API JSON形状 (フロントとサーバの共通契約)

- エラー: ステータスコード + `{"error":"メッセージ"}`。秘匿は素の404。
- 認証クッキー: 名前 **`tm_session`**、値=トークン。httpOnly, SameSite=Lax, TLS時のみSecure。
- 変更系: `Sec-Fetch-Site` が存在し `cross-site`/`same-site` なら403。
- ms単位はすべて整数ミリ秒。タイム表示 `m:ss.mmm` への整形はクライアント側。

### SSE `GET /api/stream?topics=...`
`event: <topic>` / `data: <JSON>`。接続時に購読トピックの現在値を即送出。gzip。

```jsonc
// event: ranking  (GET /api/ranking も同一形状)
{"rows":[{
  "driver":{"id":1,"name":"山田"},
  "driver_class":"現役",
  "vehicle":{"id":1,"number":1,"name":"EF9シビック","engine":"gasoline",
             "converted_cc":1595,"disp_class":"~1600cc","dt_class":"2WD"}, // EV: converted_cc=null, disp_class="EV"
  "best_ms":79882,"second_ms":81030,"best_log_id":41, // 無ければ null
  "runs":6,"valid_runs":6,"pt_total":1,"invalid":false
}]}
// event: on_course
{"cars":[{"queue_id":12,"driver":{"id":3,"name":"高橋"},"vehicle":{"id":5,"number":5,"name":"FD3S"},
  "t_start_us":1720000000000000,          // null = READY(センサー待ち)
  "pt_count":1,"mc_flag":false,
  "finish":null}]}                         // 確定演出中: {"fin_ms":83456,"until_ms":<server_ms+3000>}
// event: queue
{"items":[{"queue_id":9,"driver":{"id":2,"name":"田中"},"vehicle":{"id":2,"number":2,"name":"NDロードスター"}}]} // position順
// event: sensor_status
{"sensors":[{"sensor_id":"start","last_seen_ms":1720000000000,"loss_rate":0.0,"ntp_offset_ms":0.4}]}
// event: orphan (admin購読)
{"items":[{"kind":"unassigned_log","log_id":42,"at_ms":1720000000000,"detail":"raw 1:24.882 sensor"},
          {"kind":"orphan_start","at_ms":1720000000000,"detail":"on_course該当なし"}]}
// event: settings (公開サブセット)
{"event_name":"...","timing_mode":"sensor","pt_mode":"add","pt_penalty_ms":5000,
 "heat_ranking":false,"registration_mode":"public","registration_open":true,
 "queue_self_entry":true,"max_course_time_sec":180,
 "disp_classes":["~660cc","~1600cc","無制限","EV"],   // EVは登録車両にEVがある時のみ出現(導出)
 "driver_classes":[{"id":1,"label":"現役"}],"dt_classes":[{"id":4,"label":"2WD"}]}
// event: time (25-30s毎, keep-alive兼務)
{"server_ms":1720000000000}
```

### 公開REST
```jsonc
POST /api/register
  req: {"name":"佐藤 花子","driver_class_id":1,"icon_b64":null,
        "vehicle":{"vehicle_id":3}}                        // 相乗り
     |{"vehicle":{"number":3,"name":"アルトワークス","engine_type":"gasoline",
        "displacement_cc":658,"forced_induction":true,"drivetrain_class_id":4}}
  res: 200 {"driver_id":5} + Set-Cookie tm_session   (mode/openで403, RL 429)
GET /api/queue   → {"waiting":[<queueと同形>],"on_course":[<on_courseと同形>]}
GET /api/drivers → {"drivers":[{"id":1,"name":"山田","driver_class":"現役","has_icon":true}]}
GET /api/vehicles→ {"vehicles":[{"id":1,"number":1,"name":"EF9シビック","engine":"gasoline","displacement_cc":1595,
                    "forced_induction":false,"converted_cc":1595,"disp_class":"~1600cc","dt_class":"2WD",
                    "drivers":[{"id":1,"name":"山田"}]}]}
GET /api/combinations/{d}/{v}/logs?<rankingと同じフィルタ>
  → {"driver":{...},"vehicle":{...},"runs":[{"heat":1,"raw_ms":84310,"pt_count":0,"is_mc":false,
     "final_ms":84310,"invalid":false,"rank_in_filter":4,"timestamp_ms":...,"source":"sensor"}]}
GET /api/settings → settingsトピックと同形
```
ランキング系フィルタ (GET /api/ranking, CSV, combinations の共通クエリ):
`class_driver=現役&drivetrain=2WD&disp=~1600cc&driver_id=1&vehicle_id=2&heat=2` (すべて任意)

### ユーザーREST (§8のパス通り)
```jsonc
GET /api/my → {"driver":{"id":5,"name":"佐藤 花子","driver_class_id":1,"role":"user","has_icon":true},
  "main_vehicle_id":3,
  "vehicles":[/* /api/vehiclesの要素と同形 */],
  "queue":{"state":"none"},                        // none|waiting|ready|running|finish
  //        waiting: {"state":"waiting","position":3,"queue_id":9}
  //        ready/running/finish: {"state":"running","queue_id":12,"t_start_us":...,"pt_count":0,"mc_flag":false}
  "runs":[/* combinationsのrunsと同形+vehicle_id */]}
PUT  /api/my/profile      {"name":"...","driver_class_id":1}
POST /api/my/icon         {"icon_b64":"..."} (JPEG検証→128x128再エンコード)
GET  /api/my/qr           → PNG (image/png)
POST /api/my/vehicles     {"vehicle_id":2} | {"number":8,...新規形状}  → {"vehicle_id":8}
DELETE /api/my/vehicles/{id}
PUT  /api/my/main-vehicle {"vehicle_id":2}
POST /api/my/queue        {"vehicle_id":3}   // 省略時メイン車両
DELETE /api/my/queue
POST /api/my/queue/launch     // セルフ出走 (先頭×sensorのみ)
DELETE /api/my/queue/launch   // READY中のみ取消
```

### 運営REST (§8のパス・動詞通り。bodyの追加凍結分)
```jsonc
POST /api/admin/course            {"client_ms":1720000000000}     // manual時のみclient_ms意味あり
POST /api/admin/course/finish     {"client_ms":...}
POST /api/admin/course/{id}/finish{"client_ms":...}
PUT  /api/admin/course/{id}/pt    {"delta":1}  → {"pt_count":2}
PUT  /api/admin/course/{id}/mc    {}           → {"mc_flag":true}  // トグル
POST /api/admin/queue             {"driver_id":1,"vehicle_id":2}
PUT  /api/admin/queue/{id}        {"position":2.5}
POST /api/admin/logs              {"driver_id":1,"vehicle_id":2,"raw_ms":83456,"pt_count":0,"is_mc":false,"timestamp_ms":null}
PUT  /api/admin/logs/{id}         {"driver_id":...,"vehicle_id":...,"raw_ms":...,"pt_count":...,"is_mc":...}
PUT  /api/admin/logs/{id}/assign  {"driver_id":1,"vehicle_id":2}
POST /api/admin/users             {"name":"...","driver_class_id":1}  → {"driver_id":9,"login_url":"https://.../a/<token>"}
POST /api/admin/users/{id}/reissue → {"login_url":"...","qr_png_b64":"..."}
PUT  /api/admin/users/{id}/role   {"role":"admin"}
PUT  /api/admin/registration      {"open":false}
GET  /api/admin/export?type=ranking|combination|logs&<フィルタ>  → text/csv (UTF-8 BOM)
GET  /api/admin/sensors           → sensor_statusトピックと同形
GET  /api/admin/logs?page=1       → {"logs":[LogRow同形+driver/vehicle名],"total":123,"unassigned":[...]}
```

### ページ / テンプレートデータ
`GET /` monitor, `/ranking`, `/register`, `/my`, `/admin`, `/setup?t=`, `/a/{token}`。
html/template に渡すデータは全ページ共通:
```go
type PageData struct {
    EventName string
    Authed    bool
    IsAdmin   bool
    MyID      int64  // 未認証0
    SetupMode bool   // setup.htmlのみtrue
}
```
動的データは全てJSの fetch/SSE で取得 (テンプレート埋め込みはこの5値のみ)。

## 4. フロント実装規約 (Agent C)

- 見た目・挙動は `docs/timemon-mockup.html` (v4) を忠実に移植。1画面=1ファイル: `monitor.html` `ranking.html` `register.html` `my.html` `admin.html` `setup.html`。
- 各ファイルは完全なHTML文書 (`<style>`/`<script>` 直書き)。ヘッダー(イベント名+LIVE)・下部タブ/PCサイドバーは各ページに複製し、タブは実リンク (`<a href="/">` 等)。現在ページを `.on` 表示。`{{.IsAdmin}}` がfalseなら運営タブ非表示、`{{.Authed}}` false時はナビ簡略化 (モックの `nav.hide` 相当は登録ページのみ)。
- SSE: `new EventSource('/api/stream?topics=...')`。購読分け: monitor=`ranking,on_course,queue,time` / ranking=`ranking,time` / my=`queue,on_course,time` / admin=全部。スナップショット丸ごと差し替え描画。`time` で `offset=server_ms-Date.now()` を保持し、走行中タイマーは `(Date.now()+offset)-t_start_us/1000` をrAF描画 (表示1/10秒)。EventSource切断は自動再接続に任せる。
- 手計測打刻は body に `{"client_ms": Date.now()+offset}` を載せる (offset未確定なら省略)。
- 出走ボタン: sensor=長押し0.5秒 (`pointerdown`+タイマー)、manual=即時タップ。
- 表示整形: `fmt3(ms)` → `m:ss.mmm` (切り捨て済み値をそのまま)、走行中 `fmt1(ms)` → `m:ss.s`。
- ピットボード全画面: モックの `#fsview` 実装 (縦持ちのみ90°回転、`8:88.888` ダミー実測で幅96%固定、⇅反転、確定3秒後クローズ) をそのまま移植。
- アイコン: 登録/マイページで Cropper.js (`/static/cropper.min.js`+`cropper.min.css` セルフホスト) → 128x128 JPEG → base64でPOST。
- ランキングのフィルタ状態はURLクエリ同期 (`history.replaceState`)。絞り込み+連番振り直しのみクライアントで行う (ソート順はサーバ提供のまま)。

## 5. ウェーブ計画

| ウェーブ | タスク | 依存 |
|---|---|---|
| 1 | A: domain+テスト / B: store+テスト / C: templates+static | なし (本書のみ) |
| 2 | D: web基盤 (server, auth, setup, register/my, ページ配信, アイコン) / E: SSEハブ+スナップショット生成+公開API | 1 |
| 3 | F: queue/course状態機械+手計測+undo (web) / G: timing UDP+ペアリング+orphan+sensor_status | 2 |
| 4 | H: admin残り (logs/users/vehicles/settings/CSV/audit/QR) / I: main.go統合+embed+vendor+クロスビルド | 3 |
| 5 | J: firmware (PlatformIO, コードレビューのみ) + .gitlab-ci.yml / PM: E2E検証 | 4 |

各ウェーブ完了時にPMが `go vet ./... && go test ./...` + コードレビュー + 実機E2E (サーバ起動→ブラウザ操作、UDP模擬スクリプト) を実施。
