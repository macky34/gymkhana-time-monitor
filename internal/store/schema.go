package store

// schemaSQL is the event database schema. Originally embedded verbatim from
// the Architecture wiki page (DBスキーマ); it has
// since been reworked for the multi-event design (one server/DB can hold
// several events, at most one of which is ever 'active' at a time).
// drivers/vehicles/entries/class_defs remain event-independent, global
// assets; queue/logs/sensor_events/audit belong to (or, for the latter two,
// may optionally reference) one event. CREATE TABLE is CREATE TABLE IF NOT
// EXISTS so Open can apply this unconditionally on every startup; existing
// databases are not migrated (fresh-DB assumption for this design revision).
const schemaSQL = `
-- イベント (1行 = 1イベント)。作成 = この行のINSERT。status='active'は
-- 部分ユニークインデックスにより常に高々1行 (同時に複数のアクティブイベント
-- は作れない)。
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',           -- 'active' | 'closed'
  created_at_ms INTEGER NOT NULL,
  closed_at_ms INTEGER,                            -- NULL until closed
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_events_single_active ON events(status) WHERE status = 'active';

-- クラスラベル (driver軸・drivetrain軸のみ。排気量クラスはeventsから導出)
-- イベント横断のグローバル資産 (drivers/vehicles/entriesと同様)。
CREATE TABLE IF NOT EXISTS class_defs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  axis TEXT NOT NULL,                              -- 'driver' | 'drivetrain'
  label TEXT NOT NULL,
  sort_order INTEGER NOT NULL
);

-- ドライバー = ユーザー (認証主体)
CREATE TABLE IF NOT EXISTS drivers (
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

CREATE TABLE IF NOT EXISTS vehicles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  number INTEGER NOT NULL,                         -- 号車番号 (表示「＃3 アルトワークス」)。重複はDB制約で禁止しない — 登録・編集UIで警告表示のみ (運用ミスだが登録を詰まらせない)
  name TEXT NOT NULL,
  engine_type TEXT NOT NULL,                       -- 'gasoline' | 'diesel' | 'rotary' | 'ev'
  displacement_cc INTEGER,                         -- EVはNULL
  forced_induction INTEGER NOT NULL DEFAULT 0,     -- bool (EVは常に0)
  drivetrain_class_id INTEGER NOT NULL REFERENCES class_defs(id),
  icon BLOB,                                       -- 128x128 JPEG。NULL可
  is_deleted INTEGER NOT NULL DEFAULT 0
);

-- 紐づけ (N:M、相乗り=マルチエントリー)
CREATE TABLE IF NOT EXISTS entries (
  driver_id INTEGER NOT NULL REFERENCES drivers(id),
  vehicle_id INTEGER NOT NULL REFERENCES vehicles(id),
  PRIMARY KEY (driver_id, vehicle_id)
);

-- 出走キュー + コース状態 (状態機械の実体)。イベントに属する。
CREATE TABLE IF NOT EXISTS queue (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL REFERENCES events(id),
  driver_id INTEGER NOT NULL REFERENCES drivers(id),
  vehicle_id INTEGER NOT NULL REFERENCES vehicles(id),
  position REAL,                                   -- waiting内の並び (同一event_id内)。挿入は間の実数。隣接差が1e-9未満になったらそのイベントのwaiting全体を1.0刻みでリナンバー (稀)
  status TEXT NOT NULL DEFAULT 'waiting',          -- 'waiting' | 'on_course' | 'done' | 'canceled'
  t_start_us INTEGER,                              -- スタート打刻(μs)。on_courseでNULL=READY(センサー待ち)
  pt_count INTEGER NOT NULL DEFAULT 0,             -- 走行中付与分。ログ生成時に引き継ぐ
  mc_flag INTEGER NOT NULL DEFAULT 0,              -- ミスコース予約
  created_by INTEGER REFERENCES drivers(id)        -- 自己投入の監査用
);

CREATE INDEX IF NOT EXISTS idx_queue_event_status ON queue(event_id, status);

-- イベントに属する。
CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL REFERENCES events(id),
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

CREATE INDEX IF NOT EXISTS idx_logs_event ON logs(event_id);

-- 生トリガー全件保存 (ペアリングと独立のセーフティネット)。event_idはアクティブ
-- イベントが無い間に受信したトリガーではNULL (デデュープ台帳としてのみ機能し、
-- その場合ログ生成はスキップされる)。
CREATE TABLE IF NOT EXISTS sensor_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER REFERENCES events(id),
  sensor_id TEXT NOT NULL,                         -- 'start' | 'goal'
  boot_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  timestamp_us INTEGER NOT NULL,                   -- ESP32打刻 (chrony時刻系)
  received_at INTEGER NOT NULL,
  UNIQUE (sensor_id, boot_id, seq)                 -- 3連送の重複排除
);

-- 運営操作の監査ログ。event_idは操作時点のアクティブイベント (無ければNULL)。
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER REFERENCES events(id),
  at_ms INTEGER NOT NULL,
  driver_id INTEGER,                               -- 操作者
  action TEXT NOT NULL,                            -- 'log.edit' 'queue.launch' 'user.reissue' 等
  detail TEXT                                      -- JSON
);
`
